// Command schema exports OpenAPI 3.1 as JSON, which is also valid YAML.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"remoteops/pkg/protocol"
)

type builder struct{ schemas map[string]any }

func (b *builder) schema(t reflect.Type) any {
	if t.Kind() == reflect.Pointer {
		return map[string]any{"anyOf": []any{b.schema(t.Elem()), map[string]any{"type": "null"}}}
	}
	if t == reflect.TypeFor[time.Time]() {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	switch t.Kind() {
	case reflect.String:
		s := map[string]any{"type": "string"}
		switch t.Name() {
		case "TaskType":
			s["enum"] = []string{"inspect_system", "install_package"}
		case "TaskState":
			s["enum"] = []string{"pending_approval", "queued", "running", "succeeded", "failed", "cancelled", "timed_out", "unknown"}
		case "SessionState":
			s["enum"] = []string{"pending_pair", "connecting", "online", "running", "revoking", "closed", "cleanup_pending", "expired", "failed"}
		}
		return s
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": b.schema(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": b.schema(t.Elem())}
	case reflect.Struct:
		name := t.Name()
		if name == "" {
			return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
		}
		if _, ok := b.schemas[name]; !ok {
			props := map[string]any{}
			required := []string{}
			b.schemas[name] = map[string]any{}
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				tag := strings.Split(f.Tag.Get("json"), ",")
				if tag[0] == "" || tag[0] == "-" {
					continue
				}
				props[tag[0]] = b.schema(f.Type)
				if len(tag) == 1 {
					required = append(required, tag[0])
				}
			}
			b.schemas[name] = map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
		}
		return map[string]any{"$ref": "#/components/schemas/" + name}
	default:
		panic("unsupported schema type: " + t.String())
	}
}
func document() map[string]any {
	b := builder{map[string]any{}}
	paths := map[string]any{}
	management := []any{map[string]any{"operatorBearer": []string{}}, map[string]any{"browserSession": []string{}}}
	browser := []any{map[string]any{"browserSession": []string{}}}
	signed := []any{map[string]any{"privateSignature": []string{}}}
	add := func(path, method, summary string, in, out any, status string, auth []any) {
		operation := map[string]any{"summary": summary, "security": auth, "responses": map[string]any{status: map[string]any{"description": "Success", "content": map[string]any{"application/json": map[string]any{"schema": b.schema(reflect.TypeOf(out))}}}, "default": map[string]any{"description": "Error: invalid_request, unauthorized, forbidden, session_expired, approval_required, conflict, not_found, permission_denied or execution error", "content": map[string]any{"application/json": map[string]any{"schema": b.schema(reflect.TypeFor[protocol.ErrorEnvelope]())}}}}}
		if in != nil {
			operation["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": b.schema(reflect.TypeOf(in))}}}
		}
		if strings.Contains(path, "{id}") {
			operation["parameters"] = []any{map[string]any{"name": "id", "in": "path", "required": true, "schema": map[string]any{"type": "string"}}}
		}
		if strings.HasSuffix(path, "/logs") {
			operation["parameters"] = append(operation["parameters"].([]any), map[string]any{"name": "after", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 0, "default": 0}}, map[string]any{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 500, "default": 100}})
		}
		if strings.HasPrefix(path, "/v1/") || path == "/health" {
			operation["servers"] = []any{map[string]any{"url": "http://{worker}:9081", "variables": map[string]any{"worker": map[string]any{"default": "100.64.0.2", "description": "Validated session-bound tailnet peer"}}}}
		}
		if strings.HasPrefix(path, "/api/v1/worker/") {
			operation["servers"] = []any{map[string]any{"url": "http://{controller}:9080", "variables": map[string]any{"controller": map[string]any{"default": "100.64.0.1"}}}}
		}
		if paths[path] == nil {
			paths[path] = map[string]any{}
		}
		paths[path].(map[string]any)[method] = operation
	}
	add("/api/v1/auth/login", "post", "Operator login; sets HttpOnly SameSite=Strict cookie", protocol.LoginRequest{}, protocol.LoginResponse{}, "200", []any{})
	add("/api/v1/auth/logout", "post", "Destroy login session", struct{}{}, struct{}{}, "200", browser)
	add("/api/v1/sessions", "get", "List sessions", nil, []protocol.Session{}, "200", management)
	add("/api/v1/sessions", "post", "Create one-use pairing and scoped authorization", protocol.CreateSessionRequest{}, protocol.CreateSessionResponse{}, "201", management)
	add("/api/v1/sessions/{id}", "get", "Session status", nil, protocol.Session{}, "200", management)
	add("/api/v1/sessions/{id}/pairing", "post", "Browser operator only; rotate unused pairing, invalidate previous file and preserve deadline and authorization", struct{}{}, protocol.PairingBundle{}, "200", browser)
	add("/api/v1/sessions/{id}/close", "post", "Revoke before asynchronous node cleanup", struct{}{}, protocol.Session{}, "200", management)
	add("/api/v1/sessions/{id}/retry-cleanup", "post", "Retry failed node deletion", struct{}{}, protocol.Session{}, "200", management)
	add("/api/v1/devices", "get", "List devices", nil, []protocol.Device{}, "200", management)
	add("/api/v1/devices/{id}", "get", "Get device and last verified environment", nil, protocol.Device{}, "200", management)
	add("/api/v1/catalog", "get", "Read pinned approved software catalog", nil, []protocol.CatalogEntry{}, "200", management)
	add("/api/v1/network-status", "get", "Read local Tailscale connectivity without exposing credentials", nil, protocol.NetworkStatus{}, "200", management)
	add("/api/v1/config", "get", "Read settings, excluding all credentials", nil, protocol.Config{}, "200", management)
	add("/api/v1/config", "put", "Set bounded configuration", protocol.Config{}, protocol.Config{}, "200", management)
	add("/api/v1/audit-events", "get", "Read redacted audit events", nil, []protocol.AuditEvent{}, "200", management)
	add("/api/v1/plans", "get", "List plans", nil, []protocol.Plan{}, "200", management)
	add("/api/v1/plans", "post", "Validate TaskSpec and propose changes, without execution", protocol.TaskSpec{}, protocol.Plan{}, "201", management)
	add("/api/v1/approvals", "get", "List approval evidence", nil, []protocol.Approval{}, "200", management)
	add("/api/v1/approvals", "post", "Request human approval", protocol.CreateApprovalRequest{}, protocol.Approval{}, "201", management)
	add("/api/v1/approvals/{id}/approve", "post", "Cookie operator only; binds digest and expires; bearer Agent cannot approve", struct{}{}, protocol.Approval{}, "200", browser)
	add("/api/v1/approvals/{id}/reject", "post", "Operator rejection", struct{}{}, protocol.Approval{}, "200", browser)
	add("/api/v1/tasks", "get", "List tasks", nil, []protocol.Task{}, "200", management)
	add("/api/v1/tasks", "post", "Atomically consume approval and enforce scoped idempotency", protocol.CreateTaskRequest{}, protocol.Task{}, "201", management)
	add("/api/v1/tasks/{id}", "get", "Task outcome; unknown installations must not be replayed", nil, protocol.Task{}, "200", management)
	add("/api/v1/tasks/{id}/logs", "get", "Bounded redacted logs by sequence cursor", nil, protocol.LogsPage{}, "200", management)
	add("/api/v1/tasks/{id}/cancel", "post", "Cancel controlled process tree without claiming rollback", struct{}{}, protocol.Task{}, "200", management)
	add("/api/v1/worker/register", "post", "One-use pairing; peer resolved by Tailscale WhoIs; response signed with pairing token", protocol.RegisterRequest{}, protocol.RegisterResponse{}, "200", signed)
	add("/api/v1/worker/heartbeat", "post", "Session-bound heartbeat and current authorization", protocol.HeartbeatRequest{}, protocol.Session{}, "200", signed)
	add("/api/v1/worker/close", "post", "Worker initiated revocation", protocol.HeartbeatRequest{}, protocol.Session{}, "200", signed)
	add("/health", "get", "Session-authenticated minimal health", nil, map[string]string{}, "200", signed)
	add("/v1/tasks", "post", "Validate structured approval proof and start once; replay returns existing Task", protocol.TaskDispatch{}, protocol.Task{}, "202", signed)
	add("/v1/tasks/{id}", "get", "Local task state", nil, protocol.Task{}, "200", signed)
	add("/v1/tasks/{id}/logs", "get", "Local bounded log cursor", nil, protocol.LogsPage{}, "200", signed)
	add("/v1/tasks/{id}/cancel", "post", "Cancel local task", struct{}{}, protocol.Task{}, "200", signed)
	add("/v1/session/close", "post", "Immediately stop RPC acceptance and cancel work", struct{}{}, struct{}{}, "200", signed)
	return map[string]any{"openapi": "3.1.0", "info": map[string]any{"title": "RemoteOps", "version": "1.0.0", "description": "Normative DTOs generated from pkg/protocol. Management writes with cookie require Origin and X-CSRF-Token. Private requests/responses include X-RemoteOps-Time, X-RemoteOps-Nonce and X-RemoteOps-Signature, with v1 HMAC-SHA256 canonicalization, 60s freshness and direction-separated keys."}, "servers": []any{map[string]any{"url": "http://127.0.0.1:8080"}}, "security": management, "paths": paths, "components": map[string]any{"schemas": b.schemas, "securitySchemes": map[string]any{"operatorBearer": map[string]any{"type": "http", "scheme": "bearer"}, "browserSession": map[string]any{"type": "apiKey", "in": "cookie", "name": "remoteops_session"}, "privateSignature": map[string]any{"type": "apiKey", "in": "header", "name": "X-RemoteOps-Signature", "description": "HMAC with required timestamp and one-use nonce; see docs/architecture/protocol.md"}}}}
}
func main() {
	data, err := json.MarshalIndent(document(), "", "  ")
	if err != nil {
		panic(err)
	}
	target := "api/openapi.yaml"
	if len(os.Args) > 1 {
		target = os.Args[1]
	}
	if err = os.WriteFile(target, append(data, '\n'), 0644); err != nil {
		panic(err)
	}
	fmt.Println("Generated", target)
}
