// The elevated helper has no remote listener. An unprivileged loopback server
// releases one catalog-bound request to a helper holding a random local proof.
package privilege

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"remoteops/internal/security"
	"remoteops/internal/worker/events"
	"remoteops/pkg/protocol"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

type Request struct {
	Spec     protocol.TaskSpec     `json:"spec"`
	Entry    protocol.CatalogEntry `json:"entry"`
	Artifact string                `json:"artifact"`
	Digest   string                `json:"digest"`
}
type descriptor struct{ Endpoint, Proof string }
type response struct {
	Result protocol.TaskResult `json:"result"`
	Error  string              `json:"error,omitempty"`
	Logs   []protocol.LogEntry `json:"logs,omitempty"`
}

const helperEventMaxBytes = 8192

type helperEvent struct {
	Stream  string `json:"stream"`
	Message string `json:"message"`
}

type helperChannel struct {
	mu                                         sync.Mutex
	forwardMu                                  sync.Mutex
	request                                    Request
	proof                                      string
	emit                                       func(string, string)
	done                                       chan response
	claimed, finished, cancelled, streamedLogs bool
	logBytes                                   int
}

func newHelperChannel(request Request, proof string, emit func(string, string)) *helperChannel {
	return &helperChannel{request: request, proof: proof, emit: emit, done: make(chan response, 1)}
}

func (c *helperChannel) cancel() { c.mu.Lock(); c.cancelled = true; c.mu.Unlock() }

// All forwarded callbacks are serialized and log volume is bounded independently
// from progress. The channel can only convey informational events.
func (c *helperChannel) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" || req.Header.Get("Origin") != "" || subtle.ConstantTimeCompare([]byte(req.Header.Get("Authorization")), []byte("Bearer "+c.proof)) != 1 {
		http.Error(w, "unauthorized", 401)
		return
	}
	// Do not hold the state lock while reading a body: cancellation and state
	// polling must remain available even if an authenticated request is slow.
	var event helperEvent
	var out response
	if req.URL.Path == "/events" && !decodeHelperBody(w, req, helperEventMaxBytes, &event) {
		return
	}
	if req.URL.Path == "/result" && !decodeHelperBody(w, req, protocol.MaxBodyBytes, &out) {
		return
	}
	switch req.URL.Path {
	case "/state":
		c.mu.Lock()
		cancelled := c.cancelled
		c.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]bool{"cancelled": cancelled})
	case "/claim":
		c.mu.Lock()
		if c.claimed || c.cancelled || c.finished {
			c.mu.Unlock()
			http.Error(w, "already consumed", 409)
			return
		}
		c.claimed = true
		request := c.request // Immutable request; no mutation after channel construction.
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(request)
	case "/events":
		c.mu.Lock()
		active := c.claimed && !c.finished && !c.cancelled
		c.mu.Unlock()
		if !active {
			http.Error(w, "invalid helper state", 409)
			return
		}
		var progress events.Progress
		if event.Stream == "progress" {
			p, err := events.Decode(event.Message)
			if err != nil {
				http.Error(w, "invalid progress", 400)
				return
			}
			progress = p
		} else {
			if (event.Stream != "stdout" && event.Stream != "stderr") || len(event.Message) > 2048 || !utf8.ValidString(event.Message) {
				http.Error(w, "invalid event stream or message", 400)
				return
			}
			event.Message = sanitizeHelperLog(event.Message)
		}
		// Serialize callbacks independently of the state/cancellation mutex.
		// Recheck state after waiting so queued events cannot outlive revocation.
		c.forwardMu.Lock()
		c.mu.Lock()
		if !c.claimed || c.finished || c.cancelled {
			c.mu.Unlock()
			c.forwardMu.Unlock()
			http.Error(w, "invalid helper state", 409)
			return
		}
		forward := true
		if event.Stream != "progress" {
			c.streamedLogs = true
			event.Message, forward = c.prepareLogLocked(event.Message)
		}
		c.mu.Unlock()
		if forward && c.emit != nil {
			if event.Stream == "progress" {
				events.Emit(c.emit, progress)
			} else {
				c.emit(event.Stream, event.Message)
			}
		}
		c.forwardMu.Unlock()
		w.WriteHeader(204)
	case "/result":
		c.forwardMu.Lock()
		c.mu.Lock()
		if !c.claimed || c.finished {
			c.mu.Unlock()
			c.forwardMu.Unlock()
			http.Error(w, "invalid helper state", 409)
			return
		}
		// Legacy helper results retain their logs, while a streaming helper must
		// not replay logs already delivered during execution.
		replay := !c.streamedLogs && !c.cancelled
		c.finished = true
		c.mu.Unlock()
		if replay {
			for _, log := range out.Logs {
				if log.Stream == "stdout" || log.Stream == "stderr" {
					message := sanitizeHelperLog(log.Message)
					c.mu.Lock()
					forward := false
					if !c.cancelled {
						message, forward = c.prepareLogLocked(message)
					}
					c.mu.Unlock()
					if forward && c.emit != nil {
						c.emit(log.Stream, message)
					}
				}
			}
		}
		out.Logs = nil
		c.done <- out
		c.forwardMu.Unlock()
		w.WriteHeader(204)
	default:
		http.NotFound(w, req)
	}
}

func decodeHelperBody(w http.ResponseWriter, req *http.Request, max int, v any) bool {
	b, err := io.ReadAll(io.LimitReader(req.Body, int64(max)+1))
	if err != nil || len(b) > max {
		http.Error(w, "body exceeds limit", 413)
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if dec.Decode(v) != nil || dec.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid helper payload", 400)
		return false
	}
	return true
}

func sanitizeHelperLog(message string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, security.Redact(message))
}

// prepareLogLocked reserves the bounded budget under c.mu; callers perform
// sanitization and forwarding outside that mutex.
func (c *helperChannel) prepareLogLocked(message string) (string, bool) {
	remaining := (64 << 10) - c.logBytes - 128
	if remaining <= 0 {
		return "", false
	}
	if len(message) > 2048 {
		message = message[:2048]
	}
	if len(message) > remaining {
		message = message[:remaining]
	}
	message = strings.ToValidUTF8(message, "")
	c.logBytes += len(message) + 128
	return message, true
}

func SecureDirectory(path string) error { return secureDirectory(path) }
func ValidateRequest(r Request) error {
	if r.Digest != protocol.TaskDigest(r.Spec) || r.Spec.Type != protocol.InstallPackage || !r.Spec.RequiresAdmin || r.Spec.Package == nil || r.Spec.Package.ID != r.Entry.ID || r.Spec.Package.Version != r.Entry.Version || !r.Entry.RequiresAdmin {
		return errors.New("invalid elevated request proof")
	}
	if err := protocol.ValidateCatalog([]protocol.CatalogEntry{r.Entry}); err != nil {
		return err
	}
	if err := protocol.ValidateSpec(r.Spec, protocol.Scope{AllowedTasks: []protocol.TaskType{protocol.InstallPackage}, AllowedPackages: []string{r.Entry.ID}, AllowAdmin: true}, []protocol.CatalogEntry{r.Entry}); err != nil {
		return err
	}
	if r.Entry.Installer != "winget" && !filepath.IsAbs(r.Artifact) {
		return errors.New("artifact must use generated absolute path")
	}
	return nil
}
func Elevate(ctx context.Context, r Request, emit func(string, string)) (protocol.TaskResult, error) {
	if err := ValidateRequest(r); err != nil {
		return protocol.TaskResult{}, err
	}
	dir, err := os.MkdirTemp("", "remoteops-uac-")
	if err != nil {
		return protocol.TaskResult{}, err
	}
	defer os.RemoveAll(dir)
	if err = secureDirectory(dir); err != nil {
		return protocol.TaskResult{}, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return protocol.TaskResult{}, err
	}
	defer listener.Close()
	d := descriptor{Endpoint: "http://" + listener.Addr().String(), Proof: security.NewID("local")}
	channel := newHelperChannel(r, d.Proof, emit)
	done := channel.done
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, Handler: channel}
	go server.Serve(listener)
	defer server.Close()
	b, _ := json.Marshal(d)
	path := filepath.Join(dir, "request.json")
	if err = os.WriteFile(path, b, 0600); err != nil {
		return protocol.TaskResult{}, err
	}
	emit("stdout", "Administrator permission requested through local Windows UAC")
	// A UAC prompt may remain open. Cancelling must immediately revoke RPC even
	// while ShellExecuteEx awaits the local user's decision.
	launched := make(chan error, 1)
	go func() {
		handle, e := launchHelper(path)
		if e == nil {
			closeHelper(handle)
		}
		launched <- e
	}()
	select {
	case <-ctx.Done():
		channel.cancel()
		return protocol.TaskResult{}, ctx.Err()
	case err = <-launched:
	}
	if err != nil {
		return protocol.TaskResult{}, protocol.APIError{Code: "permission_denied", Message: "UAC permission unavailable or declined"}
	}
	select {
	case <-ctx.Done():
		// The ordinary user cannot kill an elevated process. The elevated helper
		// watches this controlled channel and kills its own full installer tree.
		channel.cancel()
		select {
		case <-done:
		case <-time.After(8 * time.Second):
		}
		return protocol.TaskResult{}, ctx.Err()
	case v := <-done:
		if v.Error != "" {
			return v.Result, errors.New(security.Redact(v.Error))
		}
		return v.Result, nil
	}
}

// RunHelper is only invoked by the same executable's local runas mode.
func RunHelper(ctx context.Context, path string, execute func(context.Context, Request, func(string, string)) (protocol.TaskResult, error)) error {
	if !Elevated() {
		return errors.New("helper requires an elevated token")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	var d descriptor
	dec := json.NewDecoder(io.LimitReader(f, 4096))
	dec.DisallowUnknownFields()
	err = dec.Decode(&d)
	f.Close()
	if err != nil {
		return err
	}
	os.Remove(path)
	u, err := url.Parse(d.Endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || len(d.Proof) < 32 {
		return errors.New("invalid local IPC descriptor")
	}
	return runHelperSession(ctx, d, execute)
}

// runHelperSession performs the production IPC protocol. RunHelper owns the
// elevated-token gate and descriptor validation before entering this function.
func runHelperSession(ctx context.Context, d descriptor, execute func(context.Context, Request, func(string, string)) (protocol.TaskResult, error)) error {
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect forbidden") }}
	call := func(endpoint string, v any) ([]byte, error) {
		// IPC is JSON, never embedded in HTML. Avoid expanding otherwise bounded
		// installer output containing '<' into six-byte HTML escape sequences.
		var buffer bytes.Buffer
		encoder := json.NewEncoder(&buffer)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(v); err != nil {
			return nil, err
		}
		b := buffer.Bytes()
		req, err := http.NewRequestWithContext(ctx, "POST", d.Endpoint+endpoint, bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+d.Proof)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxBodyBytes+1))
		if err != nil || len(body) > protocol.MaxBodyBytes {
			return nil, errors.New("IPC response exceeds limit")
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("local IPC status %d", resp.StatusCode)
		}
		return body, nil
	}
	body, err := call("/claim", nil)
	if err != nil {
		return err
	}
	var r Request
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&r); err != nil {
		return err
	}
	if err = ValidateRequest(r); err != nil {
		return err
	}
	taskCtx, cancel := context.WithTimeout(ctx, time.Duration(r.Spec.TimeoutSeconds)*time.Second)
	defer cancel()
	monitorDone := make(chan struct{})
	defer close(monitorDone)
	go func() {
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-monitorDone:
				return
			case <-taskCtx.Done():
				return
			case <-tick.C:
				body, e := call("/state", nil)
				var state struct {
					Cancelled bool `json:"cancelled"`
				}
				if e != nil || json.Unmarshal(body, &state) != nil || state.Cancelled {
					cancel()
					return
				}
			}
		}
	}()
	var eventsMu sync.Mutex
	logBytes := 0
	result, runErr := execute(taskCtx, r, func(stream, message string) {
		eventsMu.Lock()
		defer eventsMu.Unlock()
		if taskCtx.Err() != nil {
			return
		}
		if stream == "progress" {
			p, e := events.Decode(message)
			if e != nil {
				return
			}
			b, _ := json.Marshal(p)
			message = string(b)
		} else {
			if stream != "stdout" && stream != "stderr" {
				return
			}
			message = sanitizeHelperLog(message)
			remaining := (64 << 10) - logBytes - 128
			if remaining <= 0 {
				return
			}
			if len(message) > 2048 {
				message = message[:2048]
			}
			if len(message) > remaining {
				message = message[:remaining]
			}
			message = strings.ToValidUTF8(message, "")
			logBytes += len(message) + 128
		}
		if _, e := call("/events", helperEvent{Stream: stream, Message: message}); e != nil {
			cancel()
		}
	})
	// Wait for any concurrent emitter before posting the terminal result.
	eventsMu.Lock()
	defer eventsMu.Unlock()
	out := response{Result: result}
	if runErr != nil {
		out.Error = security.Redact(runErr.Error())
		if len(out.Error) > 65536 {
			out.Error = out.Error[:65536]
		}
	}
	_, err = call("/result", out)
	return err
}
