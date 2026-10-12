package controller

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"remoteops/internal/security"
	"remoteops/pkg/protocol"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCompletedRPCPreservesNewHeartbeat(t *testing.T) {
	s := newTestServer(t, filepath.Join(t.TempDir(), "state", "state.db"))
	rr := paired(t, s)
	now := time.Now()
	s.opts.Now = func() time.Time { return now }
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.opts.HTTPClient = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		if r.URL.Path == "/v1/tasks" {
			var dispatch protocol.TaskDispatch
			json.NewDecoder(r.Body).Decode(&dispatch)
			close(entered)
			<-release
			dispatch.Task.State = protocol.Succeeded
			security.WriteSignedJSON(w, r, 200, dispatch.Task, rr.SessionToken, "controller-to-worker")
		} else {
			security.WriteSignedJSON(w, r, 200, protocol.LogsPage{Entries: []protocol.LogEntry{}}, rr.SessionToken, "controller-to-worker")
		}
		return w.Result(), nil
	})}
	spec := protocol.TaskSpec{SessionID: rr.Session.ID, DeviceID: rr.Device.ID, Type: protocol.InspectSystem, TimeoutSeconds: 60}
	decode[protocol.Task](t, call(s.Handler(), "POST", "/api/v1/tasks", protocol.CreateTaskRequest{Spec: spec, IdempotencyKey: "heartbeat-001"}, nil, ""), 201)
	go func() { s.cycle(context.Background()); close(finished) }()
	<-entered
	s.mu.Lock()
	now = now.Add(10 * time.Second)
	s.mu.Unlock()
	b, _ := json.Marshal(protocol.HeartbeatRequest{SessionID: rr.Session.ID, DeviceID: rr.Device.ID})
	r := httptest.NewRequest("POST", "http://localhost/api/v1/worker/heartbeat", strings.NewReader(string(b)))
	security.SignRequest(r, b, rr.SessionToken, "worker-to-controller")
	w := httptest.NewRecorder()
	s.WorkerHandler().ServeHTTP(w, r)
	if w.Code != 200 {
		close(release)
		<-finished
		t.Fatal(w.Code)
	}
	heartbeat := decode[protocol.Session](t, w, 200).LastHeartbeat
	close(release)
	<-finished
	got := decode[protocol.Session](t, call(s.Handler(), "GET", "/api/v1/sessions/"+rr.Session.ID, nil, nil, ""), 200)
	if !got.LastHeartbeat.Equal(heartbeat) {
		t.Fatal("completed RPC overwrote concurrent heartbeat", got.LastHeartbeat, heartbeat)
	}
}
func TestUnknownOutcomeOnlyPollsAndLogsAreBounded(t *testing.T) {
	s := newTestServer(t, filepath.Join(t.TempDir(), "state", "state.db"))
	rr := paired(t, s)
	s.config.LogMaxBytes = 1024
	calls, polls := 0, 0
	var remote protocol.Task
	verifier := security.NewVerifier(rr.SessionToken, "controller-to-worker")
	s.opts.HTTPClient = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if err := verifier.Verify(r, body); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		if r.Method == "POST" && r.URL.Path == "/v1/tasks" {
			calls++
			var dispatch protocol.TaskDispatch
			json.Unmarshal(body, &dispatch)
			remote = dispatch.Task
			remote.State = protocol.Queued
			security.WriteSignedJSON(w, r, 500, protocol.ErrorEnvelope{Error: protocol.APIError{Code: "lost_result", Message: "result lost"}}, rr.SessionToken, "controller-to-worker")
		} else if strings.HasSuffix(r.URL.Path, "/logs") {
			security.WriteSignedJSON(w, r, 200, protocol.LogsPage{Entries: []protocol.LogEntry{{Sequence: 1, Message: rr.SessionToken}, {Sequence: 2, Message: "Authorization: Bearer secret"}, {Sequence: 3, Message: strings.Repeat("x", 2048)}}, NextAfter: 3}, rr.SessionToken, "controller-to-worker")
		} else {
			polls++
			if polls > 1 {
				remote.State = protocol.Succeeded
			}
			security.WriteSignedJSON(w, r, 200, remote, rr.SessionToken, "controller-to-worker")
		}
		return w.Result(), nil
	})}
	spec := protocol.TaskSpec{SessionID: rr.Session.ID, DeviceID: rr.Device.ID, Type: protocol.InspectSystem, TimeoutSeconds: 60}
	task := decode[protocol.Task](t, call(s.Handler(), "POST", "/api/v1/tasks", protocol.CreateTaskRequest{Spec: spec, IdempotencyKey: "inspect-001"}, nil, ""), 201)
	s.cycle(context.Background())
	got := decode[protocol.Task](t, call(s.Handler(), "GET", "/api/v1/tasks/"+task.ID, nil, nil, ""), 200)
	if got.State != protocol.Unknown {
		t.Fatal("uncertain dispatch not unknown")
	}
	s.cycle(context.Background())
	s.cycle(context.Background())
	if calls != 1 {
		t.Fatalf("unknown task was redispatched %d times", calls)
	}
	logs := decode[protocol.LogsPage](t, call(s.Handler(), "GET", "/api/v1/tasks/"+task.ID+"/logs", nil, nil, ""), 200)
	if !logs.Truncated {
		t.Fatal("truncation not visible")
	}
	b, _ := json.Marshal(logs)
	if strings.Contains(string(b), rr.SessionToken) || strings.Contains(string(b), "Bearer secret") {
		t.Fatal("log credential leaked")
	}
	if len(b) > 1200 {
		t.Fatal("log limit exceeded")
	}
}

func TestBlockedDispatchDoesNotBlockRevocationOrManagement(t *testing.T) {
	s := newTestServer(t, filepath.Join(t.TempDir(), "state", "state.db"))
	rr := paired(t, s)
	unrelated := paired(t, s)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.opts.HTTPClient = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" && r.URL.Path == "/v1/tasks" {
			close(entered)
			<-release
		}
		w := httptest.NewRecorder()
		security.WriteSignedJSON(w, r, 500, protocol.ErrorEnvelope{Error: protocol.APIError{Code: "unavailable", Message: "unavailable"}}, rr.SessionToken, "controller-to-worker")
		return w.Result(), nil
	})}
	spec := protocol.TaskSpec{SessionID: rr.Session.ID, DeviceID: rr.Device.ID, Type: protocol.InspectSystem, TimeoutSeconds: 60}
	decode[protocol.Task](t, call(s.Handler(), "POST", "/api/v1/tasks", protocol.CreateTaskRequest{Spec: spec, IdempotencyKey: "blocked-001"}, nil, ""), 201)
	go func() { s.cycle(context.Background()); close(finished) }()
	<-entered
	defer func() { close(release); <-finished }()
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response <- call(s.Handler(), "POST", "/api/v1/sessions/"+rr.Session.ID+"/close", struct{}{}, nil, "")
	}()
	select {
	case w := <-response:
		closed := decode[protocol.Session](t, w, 200)
		if closed.ClosedAt == nil || closed.State.Active() {
			t.Fatal("authority was not immediately revoked")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked dispatch stalled session revocation")
	}
	go func() { response <- call(s.Handler(), "GET", "/api/v1/config", nil, nil, "") }()
	select {
	case w := <-response:
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked dispatch stalled management")
	}
	body, _ := json.Marshal(protocol.HeartbeatRequest{SessionID: unrelated.Session.ID, DeviceID: unrelated.Device.ID})
	req := httptest.NewRequest("POST", "http://localhost/api/v1/worker/heartbeat", strings.NewReader(string(body)))
	security.SignRequest(req, body, unrelated.SessionToken, "worker-to-controller")
	go func() { w := httptest.NewRecorder(); s.WorkerHandler().ServeHTTP(w, req); response <- w }()
	select {
	case w := <-response:
		if w.Code != 200 {
			t.Fatal("unrelated heartbeat rejected", w.Code, w.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("blocked dispatch stalled unrelated heartbeat")
	}
}

func TestHeartbeatExpiryAndCleanupRetry(t *testing.T) {
	s := newTestServer(t, filepath.Join(t.TempDir(), "state", "state.db"))
	rr := paired(t, s)
	now := time.Now()
	s.opts.Now = func() time.Time { return now }
	s.opts.HTTPClient = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		security.WriteSignedJSON(w, r, 200, struct{}{}, rr.SessionToken, "controller-to-worker")
		return w.Result(), nil
	})}
	now = now.Add(46 * time.Second)
	s.cycle(context.Background())
	closed := decode[protocol.Session](t, call(s.Handler(), "GET", "/api/v1/sessions/"+rr.Session.ID, nil, nil, ""), 200)
	if closed.State != protocol.CleanupPending || closed.ClosedAt == nil || closed.CleanupError == "" {
		t.Fatal("heartbeat loss did not revoke and expose cleanup error", closed)
	}
	events := decode[[]protocol.AuditEvent](t, call(s.Handler(), "GET", "/api/v1/audit-events", nil, nil, ""), 200)
	workerSeen, systemSeen := false, false
	for _, e := range events {
		if e.Action == "worker_registered" {
			workerSeen = e.Actor == "worker"
		}
		if e.Action == "session_revoked" {
			systemSeen = e.Actor == "system"
		}
	}
	if !workerSeen || !systemSeen {
		t.Fatal("worker registration or automatic expiry attributed incorrectly", events)
	}
	s.mu.Lock()
	_, cached := s.verifiers[rr.Session.ID]
	s.mu.Unlock()
	if cached {
		t.Fatal("revoked verifier retained")
	}
	s.opts.NodeCleaner = NodeCleanerFunc(func(context.Context, string) error { return nil })
	decode[protocol.Session](t, call(s.Handler(), "POST", "/api/v1/sessions/"+rr.Session.ID+"/retry-cleanup", struct{}{}, nil, ""), 200)
	s.cycle(context.Background())
	closed = decode[protocol.Session](t, call(s.Handler(), "GET", "/api/v1/sessions/"+rr.Session.ID, nil, nil, ""), 200)
	if closed.State != protocol.Closed || closed.CleanupError != "" {
		t.Fatal("cleanup retry did not complete", closed)
	}
}

func TestExpiredPairingNeverRegisters(t *testing.T) {
	s := newTestServer(t, filepath.Join(t.TempDir(), "state", "state.db"))
	c := decode[protocol.CreateSessionResponse](t, call(s.Handler(), "POST", "/api/v1/sessions", protocol.CreateSessionRequest{Scope: protocol.Scope{AllowedTasks: []protocol.TaskType{protocol.InspectSystem}}}, nil, ""), 201)
	s.opts.Now = func() time.Time { return c.Pairing.ExpiresAt.Add(time.Second) }
	in := protocol.RegisterRequest{ProtocolVersion: protocol.Version, SessionID: c.Session.ID, PairingToken: c.Pairing.PairingToken, Port: protocol.WorkerPort, Hostname: "expired"}
	b, _ := json.Marshal(in)
	r := httptest.NewRequest("POST", "http://localhost/api/v1/worker/register", strings.NewReader(string(b)))
	security.SignRequest(r, b, c.Pairing.PairingToken, "worker-to-controller")
	w := httptest.NewRecorder()
	s.WorkerHandler().ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatal("expired pairing accepted", w.Code)
	}
	if err := security.VerifyResponse(w.Result(), w.Body.Bytes(), c.Pairing.PairingToken, "worker-to-controller", r.Header.Get(security.HeaderNonce)); err != nil {
		t.Fatal("expiry error was not signed")
	}
}
func TestWorkerHeartbeatReplayAndCrossDeviceRejection(t *testing.T) {
	s := newTestServer(t, filepath.Join(t.TempDir(), "state", "state.db"))
	rr := paired(t, s)
	body, _ := json.Marshal(protocol.HeartbeatRequest{SessionID: rr.Session.ID, DeviceID: rr.Device.ID})
	req := httptest.NewRequest("POST", "http://localhost/api/v1/worker/heartbeat", strings.NewReader(string(body)))
	security.SignRequest(req, body, rr.SessionToken, "worker-to-controller")
	w := httptest.NewRecorder()
	s.WorkerHandler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	replay := req.Clone(req.Context())
	replay.Body = io.NopCloser(strings.NewReader(string(body)))
	w = httptest.NewRecorder()
	s.WorkerHandler().ServeHTTP(w, replay)
	if w.Code != 401 {
		t.Fatal("heartbeat replay accepted")
	}
	body, _ = json.Marshal(protocol.HeartbeatRequest{SessionID: rr.Session.ID, DeviceID: "wrong-device"})
	req = httptest.NewRequest("POST", "http://localhost/api/v1/worker/heartbeat", strings.NewReader(string(body)))
	security.SignRequest(req, body, rr.SessionToken, "worker-to-controller")
	w = httptest.NewRecorder()
	s.WorkerHandler().ServeHTTP(w, req)
	if w.Code != 409 {
		t.Fatal("cross device heartbeat accepted")
	}
}

func TestHeartbeatNonceRemainsConsumedAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "state.db")
	s := newTestServer(t, path)
	rr := paired(t, s)
	b, _ := json.Marshal(protocol.HeartbeatRequest{SessionID: rr.Session.ID, DeviceID: rr.Device.ID})
	r := httptest.NewRequest("POST", "http://localhost/api/v1/worker/heartbeat", strings.NewReader(string(b)))
	security.SignRequest(r, b, rr.SessionToken, "worker-to-controller")
	w := httptest.NewRecorder()
	s.WorkerHandler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	s.Close()
	s = newTestServer(t, path)
	r.Body = io.NopCloser(strings.NewReader(string(b)))
	w = httptest.NewRecorder()
	s.WorkerHandler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("restarted controller accepted consumed nonce", w.Code)
	}
}
