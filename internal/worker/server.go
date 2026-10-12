// Package worker exposes a session-bound signed RPC listener. It never accepts commands.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"remoteops/internal/security"
	"remoteops/internal/worker/events"
	"remoteops/pkg/protocol"
)

type Executor interface {
	Execute(context.Context, protocol.TaskSpec, protocol.CatalogEntry, func(string, string)) (protocol.TaskResult, error)
}
type Options struct {
	Session                protocol.Session
	DeviceID, SessionToken string
	Catalog                []protocol.CatalogEntry
	Executor               Executor
	LogMaxBytes            int
	HeartbeatTTL           time.Duration
}
type record struct {
	progress  events.Progress
	task      protocol.Task
	logs      []protocol.LogEntry
	bytes     int
	truncated bool
	cancel    context.CancelFunc
}
type Server struct {
	opt             Options
	verifier        *security.Verifier
	mu              sync.Mutex
	tasks           map[string]*record
	keys, approvals map[string]string
	closed          bool
	endReason       string
	ctx             context.Context
	stop            context.CancelFunc
	execution       sync.Mutex
	lastController  time.Time
	wg              sync.WaitGroup
}

func New(opt Options) (*Server, error) {
	if opt.Session.ID == "" || opt.DeviceID == "" || (opt.Session.DeviceID != "" && opt.Session.DeviceID != opt.DeviceID) || opt.SessionToken == "" || !opt.Session.State.Active() || !time.Now().Before(opt.Session.ExpiresAt) {
		return nil, errors.New("active session identity and credential required")
	}
	if err := protocol.ValidateCatalog(opt.Catalog); err != nil {
		return nil, err
	}
	if err := protocol.ValidateScope(opt.Session.Scope, opt.Catalog); err != nil {
		return nil, err
	}
	// Detach all nested slices from caller-owned data: consent and catalog are immutable.
	b, _ := json.Marshal(opt.Session)
	json.Unmarshal(b, &opt.Session)
	b, _ = json.Marshal(opt.Catalog)
	json.Unmarshal(b, &opt.Catalog)
	if opt.Executor == nil {
		inspectionCatalog := []protocol.CatalogEntry{}
		for _, id := range opt.Session.Scope.AllowedPackages {
			if entry, ok := protocol.FindPackage(opt.Catalog, id, ""); ok {
				inspectionCatalog = append(inspectionCatalog, entry)
			}
		}
		opt.Executor = NativeExecutor{Catalog: inspectionCatalog}
	}
	if opt.LogMaxBytes <= 0 || opt.LogMaxBytes > protocol.MaxBodyBytes {
		opt.LogMaxBytes = protocol.MaxBodyBytes
	}
	if opt.HeartbeatTTL <= 0 {
		opt.HeartbeatTTL = 45 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{opt: opt, ctx: ctx, stop: cancel, verifier: security.NewVerifier(opt.SessionToken, "controller-to-worker"), tasks: map[string]*record{}, keys: map[string]string{}, approvals: map[string]string{}, lastController: time.Now()}
	s.wg.Go(func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				s.mu.Lock()
				expired := !time.Now().Before(s.opt.Session.ExpiresAt) || time.Since(s.lastController) > s.opt.HeartbeatTTL
				reason := s.inactiveReason()
				s.mu.Unlock()
				if expired {
					s.CloseWithReason(reason)
					return
				}
			}
		}
	})
	return s, nil
}
func (s *Server) Close() {
	s.CloseWithReason("local_closed")
}
func (s *Server) Done() <-chan struct{} { return s.ctx.Done() }
func (s *Server) Wait()                 { s.wg.Wait() }

// TouchController is called only after verification of a successful signed heartbeat.
func (s *Server) TouchController()      { s.mu.Lock(); s.lastController = time.Now(); s.mu.Unlock() }
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serve) }
func (s *Server) reply(w http.ResponseWriter, r *http.Request, status int, v any) {
	b, _ := json.Marshal(v)
	s.replyBytes(w, r, status, b)
}

// replyBytes writes an already detached response. Callers must release s.mu
// before signing or writing, so a slow peer cannot delay local cancellation.
func (s *Server) replyBytes(w http.ResponseWriter, r *http.Request, status int, b []byte) {
	security.SignResponse(w.Header(), status, b, s.opt.SessionToken, "controller-to-worker", r.Header.Get(security.HeaderNonce))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(b)
}
func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	s.reply(w, r, status, protocol.ErrorEnvelope{Error: protocol.APIError{Code: code, Message: security.Redact(message)}})
}
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(io.LimitReader(r.Body, protocol.MaxBodyBytes+1))
	if err != nil || len(b) > protocol.MaxBodyBytes {
		s.fail(w, r, 413, "invalid_request", "body exceeds limit")
		return
	}
	if err = s.verifier.Verify(r, b); err != nil {
		s.fail(w, r, 401, "unauthorized", "request authentication failed")
		return
	}
	s.mu.Lock()
	active := !s.closed && time.Now().Before(s.opt.Session.ExpiresAt) && time.Since(s.lastController) <= s.opt.HeartbeatTTL
	reason := s.inactiveReason()
	s.mu.Unlock()
	if !active {
		s.CloseWithReason(reason)
		s.fail(w, r, 409, "session_expired", "session authorization ended")
		return
	}
	if r.URL.Path == "/health" && r.Method == "GET" {
		s.reply(w, r, 200, map[string]string{"status": "ok", "session_id": s.opt.Session.ID, "device_id": s.opt.DeviceID})
		return
	}
	if r.URL.Path == "/v1/session/close" && r.Method == "POST" {
		s.CloseWithReason("controller_closed")
		s.reply(w, r, 200, struct{}{})
		return
	}
	if r.URL.Path == "/v1/tasks" && r.Method == "POST" {
		var d protocol.TaskDispatch
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.DisallowUnknownFields()
		if dec.Decode(&d) != nil || dec.Decode(new(any)) != io.EOF {
			s.fail(w, r, 400, "invalid_request", "invalid task dispatch")
			return
		}
		s.submit(w, r, d)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || len(parts) > 4 || parts[0] != "v1" || parts[1] != "tasks" {
		s.fail(w, r, 404, "not_found", "unknown RPC")
		return
	}
	s.mu.Lock()
	rec := s.tasks[parts[2]]
	if rec == nil {
		s.mu.Unlock()
		s.fail(w, r, 404, "not_found", "task not cached")
		return
	}
	if len(parts) == 3 && r.Method == "GET" {
		body, _ := json.Marshal(rec.task)
		s.mu.Unlock()
		s.replyBytes(w, r, 200, body)
		return
	}
	if len(parts) == 4 && parts[3] == "cancel" && r.Method == "POST" {
		if !rec.task.State.Terminal() {
			s.cancelRecord(rec)
		}
		body, _ := json.Marshal(rec.task)
		s.mu.Unlock()
		s.replyBytes(w, r, 200, body)
		return
	}
	if len(parts) == 4 && parts[3] == "logs" && r.Method == "GET" {
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 100
		}
		if limit > 500 {
			limit = 500
		}
		page := protocol.LogsPage{Entries: []protocol.LogEntry{}, NextAfter: after, Truncated: rec.truncated}
		for _, l := range rec.logs {
			if l.Sequence > after && len(page.Entries) < limit {
				page.Entries = append(page.Entries, l)
				page.NextAfter = l.Sequence
			}
		}
		body, _ := json.Marshal(page)
		s.mu.Unlock()
		s.replyBytes(w, r, 200, body)
		return
	}
	s.mu.Unlock()
	s.fail(w, r, 405, "invalid_request", "unsupported RPC method")
}
func (s *Server) submit(w http.ResponseWriter, r *http.Request, d protocol.TaskDispatch) {
	task := d.Task
	sp := task.Spec
	if sp.SessionID != s.opt.Session.ID || sp.DeviceID != s.opt.DeviceID {
		s.fail(w, r, 403, "scope_denied", "device or session mismatch")
		return
	}
	if err := protocol.ValidateSpec(sp, s.opt.Session.Scope, s.opt.Catalog); err != nil {
		s.fail(w, r, 403, "scope_denied", err.Error())
		return
	}
	digest := protocol.TaskDigest(sp)
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`).MatchString(task.ID) || (task.ApprovalID != "" && !regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`).MatchString(task.ApprovalID)) || task.Digest != digest || !protocol.ValidIdempotencyKey(task.IdempotencyKey) {
		s.fail(w, r, 400, "invalid_request", "invalid task identity or digest")
		return
	}
	if sp.Type == protocol.InstallPackage && (task.ApprovalID == "" || d.ApprovalDigest != digest) {
		s.fail(w, r, 403, "approval_required", "approval proof does not bind this task")
		return
	}
	s.mu.Lock()
	if s.closed || !time.Now().Before(s.opt.Session.ExpiresAt) || time.Since(s.lastController) > s.opt.HeartbeatTTL {
		s.mu.Unlock()
		s.fail(w, r, 409, "session_expired", "session closed")
		return
	}
	existing := s.tasks[task.ID]
	if existing == nil {
		existing = s.tasks[s.keys[task.IdempotencyKey]]
	}
	if existing != nil {
		if existing.task.Digest != digest || existing.task.IdempotencyKey != task.IdempotencyKey || existing.task.ApprovalID != task.ApprovalID {
			s.mu.Unlock()
			s.fail(w, r, 409, "conflict", "duplicate parameters differ")
			return
		}
		body, _ := json.Marshal(existing.task)
		s.mu.Unlock()
		s.replyBytes(w, r, 200, body)
		return
	}
	if len(s.tasks) >= 1000 {
		s.mu.Unlock()
		s.fail(w, r, 429, "capacity", "session task cache full")
		return
	}
	if task.ApprovalID != "" && s.approvals[task.ApprovalID] != "" {
		s.mu.Unlock()
		s.fail(w, r, 409, "conflict", "approval already consumed")
		return
	}
	task.State = protocol.Queued
	task.Result = nil
	task.ErrorCode = ""
	task.ErrorMessage = ""
	task.StartedAt = nil
	task.FinishedAt = nil
	task.CreatedAt = time.Now().UTC()
	ctx, cancel := context.WithTimeout(s.ctx, time.Duration(sp.TimeoutSeconds)*time.Second)
	rec := &record{task: task, progress: events.Progress{Stage: "queued"}, logs: []protocol.LogEntry{}, cancel: cancel}
	s.tasks[task.ID] = rec
	s.keys[task.IdempotencyKey] = task.ID
	if task.ApprovalID != "" {
		s.approvals[task.ApprovalID] = task.ID
	}
	s.wg.Go(func() { s.execute(ctx, rec) })
	body, _ := json.Marshal(task)
	s.mu.Unlock()
	s.replyBytes(w, r, 202, body)
}
func (s *Server) execute(ctx context.Context, r *record) {
	defer r.cancel()
	s.execution.Lock()
	defer s.execution.Unlock()
	s.mu.Lock()
	if s.closed || !time.Now().Before(s.opt.Session.ExpiresAt) || time.Since(s.lastController) > s.opt.HeartbeatTTL {
		r.cancel()
	}
	if ctx.Err() == nil {
		now := time.Now().UTC()
		r.task.State = protocol.TaskRunning
		r.task.StartedAt = &now
	}
	s.mu.Unlock()
	var result protocol.TaskResult
	err := ctx.Err()
	if err == nil {
		entry := protocol.CatalogEntry{}
		if r.task.Spec.Package != nil {
			entry, _ = protocol.FindPackage(s.opt.Catalog, r.task.Spec.Package.ID, r.task.Spec.Package.Version)
		}
		result, err = s.opt.Executor.Execute(ctx, r.task.Spec, entry, func(stream, message string) { s.log(r, stream, message) })
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	r.task.FinishedAt = &now
	r.task.Result = &result
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		r.task.State = protocol.TimedOut
		r.task.ErrorCode = "task_timeout"
	case ctx.Err() != nil:
		r.task.State = protocol.Cancelled
		r.task.ErrorCode = "cancelled"
	case err != nil:
		r.task.State = protocol.TaskFailed
		r.task.ErrorCode = "execution_failed"
		var api protocol.APIError
		if errors.As(err, &api) {
			r.task.ErrorCode = api.Code
		}
		r.task.ErrorMessage = security.Redact(err.Error())
		if len(r.task.ErrorMessage) > 65536 {
			r.task.ErrorMessage = r.task.ErrorMessage[:65536]
		}
	default:
		r.task.State = protocol.Succeeded
	}
	r.progress = events.Progress{Stage: string(r.task.State)}
}
func (s *Server) log(r *record, stream, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if stream == "progress" {
		if r.task.State.Terminal() || s.closed || r.progress.Stage == "cancelling" {
			return
		}
		p, err := events.Decode(message)
		if err == nil {
			r.progress = p
		}
		return
	}
	message = security.Redact(message)
	remaining := s.opt.LogMaxBytes - r.bytes - 128
	if remaining <= 0 {
		r.truncated = true
		return
	}
	if len(message) > remaining {
		message = message[:remaining]
		r.truncated = true
	}
	if stream != "stderr" {
		stream = "stdout"
	}
	entry := protocol.LogEntry{Sequence: int64(len(r.logs) + 1), Timestamp: time.Now().UTC(), Stream: stream, Message: message}
	encoded, _ := json.Marshal(entry)
	for len(encoded)+1 > remaining && len(entry.Message) > 0 {
		entry.Message = entry.Message[:len(entry.Message)/2]
		r.truncated = true
		encoded, _ = json.Marshal(entry)
	}
	if len(encoded)+1 > remaining {
		r.truncated = true
		return
	}
	r.bytes += len(encoded) + 1
	r.logs = append(r.logs, entry)
}
