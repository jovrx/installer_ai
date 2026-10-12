// Package mcp adapts structured Agent tools to the authenticated Controller API.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"remoteops/internal/i18n"
	"remoteops/internal/security"
	"remoteops/pkg/protocol"
)

type Server struct {
	base, token string
	client      *http.Client
	language    i18n.Language
}

func New(base, token string, client *http.Client) *Server {
	return NewWithLanguage(base, token, client, i18n.Chinese)
}

// NewWithLanguage changes human text only. Protocol values and tool contracts are stable.
func NewWithLanguage(base, token string, client *http.Client, language i18n.Language) *Server {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Server{strings.TrimRight(base, "/"), token, client, language}
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema any             `json:"inputSchema"`
	Annotations map[string]bool `json:"annotations,omitempty"`
}

func object(properties map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}
func str() map[string]any { return map[string]any{"type": "string", "minLength": 1} }
func taskSchema() any {
	return object(map[string]any{"session_id": str(), "device_id": str(), "type": map[string]any{"type": "string", "enum": []string{"inspect_system", "install_package"}}, "package": object(map[string]any{"id": str(), "version": str()}, "id", "version"), "timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 7200}, "requires_admin": map[string]any{"type": "boolean"}}, "session_id", "device_id", "type", "timeout_seconds", "requires_admin")
}
func Tools() []Tool { return ToolsForLanguage(i18n.Chinese) }

func ToolsForLanguage(language i18n.Language) []Tool {
	read := map[string]bool{"readOnlyHint": true}
	change := map[string]bool{"readOnlyHint": false, "destructiveHint": true}
	return []Tool{
		{"list_devices", i18n.Text(language, "mcp.tool.list_devices"), object(map[string]any{}), read},
		{"get_device_info", i18n.Text(language, "mcp.tool.get_device_info"), object(map[string]any{"device_id": str()}, "device_id"), read},
		{"create_install_plan", i18n.Text(language, "mcp.tool.create_install_plan"), object(map[string]any{"spec": taskSchema()}, "spec"), read},
		{"request_task_approval", i18n.Text(language, "mcp.tool.request_task_approval"), object(map[string]any{"plan_id": str()}, "plan_id"), map[string]bool{"readOnlyHint": false, "destructiveHint": false}},
		{"install_software", i18n.Text(language, "mcp.tool.install_software"), object(map[string]any{"spec": taskSchema(), "approval_id": str(), "idempotency_key": map[string]any{"type": "string", "minLength": 8, "maxLength": 128}}, "spec", "approval_id", "idempotency_key"), change},
		{"get_task_status", i18n.Text(language, "mcp.tool.get_task_status"), object(map[string]any{"task_id": str()}, "task_id"), read},
		{"get_task_logs", i18n.Text(language, "mcp.tool.get_task_logs"), object(map[string]any{"task_id": str(), "after": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 500}}, "task_id"), read},
		{"cancel_task", i18n.Text(language, "mcp.tool.cancel_task"), object(map[string]any{"task_id": str()}, "task_id"), change},
		{"close_session", i18n.Text(language, "mcp.tool.close_session"), object(map[string]any{"session_id": str()}, "session_id"), change},
	}
}
func (s *Server) decode(args json.RawMessage, v any) error {
	if len(args) == 0 {
		args = []byte(`{}`)
	}
	d := json.NewDecoder(bytes.NewReader(args))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return s.failure("mcp.error.arguments")
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return s.failure("mcp.error.argument_object")
	}
	return nil
}
func validID(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func (s *Server) api(ctx context.Context, method, path string, in any) (json.RawMessage, error) {
	if s.token == "" {
		return nil, s.failure("mcp.error.authentication")
	}
	var data []byte
	var err error
	if in != nil {
		data, err = json.Marshal(in)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, s.base+"/api/v1"+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, s.failure("mcp.error.connection")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > protocol.MaxBodyBytes {
		return nil, s.failure("mcp.error.response_size")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e protocol.ErrorEnvelope
		if json.Unmarshal(body, &e) == nil && e.Error.Code != "" {
			key := "web.api." + e.Error.Code
			// Known browser/API categories share explanations; no server message body is displayed.
			explanation := i18n.Text(s.language, key)
			e.Error.Message = s.text("mcp.error.api", explanation, safeDiagnostic(e.Error.Code), safeDiagnostic(e.Error.RequestID))
			return nil, e.Error
		}
		return nil, s.failure("mcp.error.http", resp.StatusCode)
	}
	if !json.Valid(body) {
		return nil, s.failure("mcp.error.invalid_response")
	}
	return body, nil
}
func (s *Server) Call(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	switch name {
	case "list_devices":
		if err := s.decode(args, &struct{}{}); err != nil {
			return nil, err
		}
		return s.api(ctx, "GET", "/devices", nil)
	case "get_device_info":
		var a struct {
			DeviceID string `json:"device_id"`
		}
		if err := s.decode(args, &a); err != nil {
			return nil, err
		}
		if !validID(a.DeviceID) {
			return nil, s.failure("mcp.error.device_id")
		}
		data, err := s.api(ctx, "GET", "/devices/"+a.DeviceID, nil)
		if err != nil {
			return nil, err
		}
		var dev protocol.Device
		if err = json.Unmarshal(data, &dev); err != nil {
			return nil, err
		}
		spec := protocol.TaskSpec{SessionID: dev.SessionID, DeviceID: dev.ID, Type: protocol.InspectSystem, TimeoutSeconds: 120}
		data, err = s.api(ctx, "POST", "/tasks", protocol.CreateTaskRequest{Spec: spec, IdempotencyKey: security.NewID("inspect")})
		if err != nil {
			return nil, err
		}
		var task protocol.Task
		if err = json.Unmarshal(data, &task); err != nil {
			return nil, err
		}
		timer := time.NewTimer(15 * time.Second)
		defer timer.Stop()
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for !task.State.Terminal() && task.State != protocol.Unknown {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-timer.C:
				return json.Marshal(task)
			case <-tick.C:
				data, err = s.api(ctx, "GET", "/tasks/"+task.ID, nil)
				if err != nil {
					return nil, err
				}
				if err = json.Unmarshal(data, &task); err != nil {
					return nil, err
				}
			}
		}
		return json.Marshal(task)
	case "create_install_plan":
		var a struct {
			Spec protocol.TaskSpec `json:"spec"`
		}
		if err := s.decode(args, &a); err != nil {
			return nil, err
		}
		if !validID(a.Spec.SessionID) || !validID(a.Spec.DeviceID) {
			return nil, s.failure("mcp.error.session_device")
		}
		return s.api(ctx, "POST", "/plans", a.Spec)
	case "request_task_approval":
		var a protocol.CreateApprovalRequest
		if err := s.decode(args, &a); err != nil {
			return nil, err
		}
		if !validID(a.PlanID) {
			return nil, s.failure("mcp.error.plan_id")
		}
		return s.api(ctx, "POST", "/approvals", a)
	case "install_software":
		var a protocol.CreateTaskRequest
		if err := s.decode(args, &a); err != nil {
			return nil, err
		}
		if a.Spec.Type != protocol.InstallPackage || a.Spec.Package == nil || !validID(a.ApprovalID) || !protocol.ValidIdempotencyKey(a.IdempotencyKey) || !validID(a.Spec.SessionID) || !validID(a.Spec.DeviceID) {
			return nil, s.failure("mcp.error.installation")
		}
		return s.api(ctx, "POST", "/tasks", a)
	case "get_task_status", "cancel_task":
		var a struct {
			TaskID string `json:"task_id"`
		}
		if err := s.decode(args, &a); err != nil {
			return nil, err
		}
		if !validID(a.TaskID) {
			return nil, s.failure("mcp.error.task_id")
		}
		if name == "cancel_task" {
			return s.api(ctx, "POST", "/tasks/"+a.TaskID+"/cancel", struct{}{})
		}
		return s.api(ctx, "GET", "/tasks/"+a.TaskID, nil)
	case "get_task_logs":
		var a struct {
			TaskID string `json:"task_id"`
			After  int64  `json:"after"`
			Limit  int    `json:"limit"`
		}
		if err := s.decode(args, &a); err != nil {
			return nil, err
		}
		if !validID(a.TaskID) || a.After < 0 || a.Limit < 0 || a.Limit > 500 {
			return nil, s.failure("mcp.error.logs")
		}
		if a.Limit == 0 {
			a.Limit = 100
		}
		return s.api(ctx, "GET", fmt.Sprintf("/tasks/%s/logs?after=%d&limit=%d", a.TaskID, a.After, a.Limit), nil)
	case "close_session":
		var a struct {
			SessionID string `json:"session_id"`
		}
		if err := s.decode(args, &a); err != nil {
			return nil, err
		}
		if !validID(a.SessionID) {
			return nil, s.failure("mcp.error.session_id")
		}
		return s.api(ctx, "POST", "/sessions/"+a.SessionID+"/close", struct{}{})
	default:
		return nil, s.failure("mcp.error.unknown_tool")
	}
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (s *Server) text(key string, args ...any) string   { return i18n.Text(s.language, key, args...) }
func (s *Server) failure(key string, args ...any) error { return errors.New(s.text(key, args...)) }
func safeDiagnostic(value string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r >= 127 && r <= 159 {
			return -1
		}
		return r
	}, security.Redact(value))
}

func (s *Server) Serve(in io.Reader, out io.Writer) error {
	scan := bufio.NewScanner(in)
	scan.Buffer(make([]byte, 4096), protocol.MaxBodyBytes)
	enc := json.NewEncoder(out)
	initialized := false
	for scan.Scan() {
		line := scan.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			if err = enc.Encode(response{"2.0", json.RawMessage("null"), nil, &rpcError{-32700, s.text("mcp.error.json")}}); err != nil {
				return err
			}
			continue
		}
		if len(req.ID) == 0 {
			continue
		}
		res := response{JSONRPC: "2.0", ID: req.ID}
		if req.JSONRPC != "2.0" {
			res.Error = &rpcError{-32600, s.text("mcp.error.version")}
		} else {
			switch req.Method {
			case "initialize":
				var p struct {
					ProtocolVersion string `json:"protocolVersion"`
				}
				_ = json.Unmarshal(req.Params, &p)
				version := p.ProtocolVersion
				if version != "2025-06-18" && version != "2024-11-05" && version != "2025-03-26" {
					version = "2025-06-18"
				}
				initialized = true
				res.Result = map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}}, "serverInfo": map[string]string{"name": "remoteops", "version": "1.0.0"}, "instructions": s.text("mcp.instructions")}
			case "ping":
				res.Result = struct{}{}
			case "tools/list":
				if !initialized {
					res.Error = &rpcError{-32000, s.text("mcp.error.initialize")}
				} else {
					res.Result = map[string]any{"tools": ToolsForLanguage(s.language)}
				}
			case "tools/call":
				if !initialized {
					res.Error = &rpcError{-32000, s.text("mcp.error.initialize")}
					break
				}
				var p struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				}
				if err := json.Unmarshal(req.Params, &p); err != nil {
					res.Error = &rpcError{-32602, s.text("mcp.error.call")}
					break
				}
				ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
				data, err := s.Call(ctx, p.Name, p.Arguments)
				cancel()
				if err != nil {
					res.Result = map[string]any{"isError": true, "content": []any{map[string]string{"type": "text", "text": safeDiagnostic(err.Error())}}}
				} else {
					res.Result = map[string]any{"isError": false, "content": []any{map[string]string{"type": "text", "text": string(data)}}, "structuredContent": map[string]any{"data": data}}
				}
			default:
				res.Error = &rpcError{-32601, s.text("mcp.error.method")}
			}
		}
		if err := enc.Encode(res); err != nil {
			return err
		}
	}
	return scan.Err()
}
func ValidateLocalURL(target string) error {
	u, err := url.Parse(target)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("invalid Controller base URL")
	}
	host := u.Hostname()
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return errors.New("MCP must use the local Controller")
	}
	return nil
}
