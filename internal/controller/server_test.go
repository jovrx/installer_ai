package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"remoteops/internal/security"
	"remoteops/pkg/protocol"
	"strings"
	"sync"
	"testing"
	"time"
)

const admin = "operator_token_0123456789abcdef0123456789abcdef"

func newTestServer(t *testing.T, path string) *Server {
	t.Helper()
	s, err := New(Options{DBPath: path, AdminToken: admin, ControllerURL: "http://100.64.0.1:9080", Catalog: []protocol.CatalogEntry{{ID: "git", Version: "2.51.0", Installer: "exe", URL: "https://example.com/git.exe", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}, PeerResolver: PeerResolverFunc(func(context.Context, *http.Request) (Peer, error) {
		return Peer{Address: "100.64.0.2", NodeID: "n1"}, nil
	}), NodeCleaner: NodeCleanerFunc(func(context.Context, string) error { return errors.New("cleanup unavailable") })})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func call(h http.Handler, method, path string, in any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(in)
	r := httptest.NewRequest(method, "http://localhost"+path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	if cookie == nil {
		r.Header.Set("Authorization", "Bearer "+admin)
	} else {
		r.AddCookie(cookie)
		r.Header.Set("X-CSRF-Token", csrf)
		r.Header.Set("Origin", "http://localhost")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func decode[T any](t *testing.T, w *httptest.ResponseRecorder, status int) T {
	t.Helper()
	if w.Code != status {
		t.Fatalf("want %d got %d: %s", status, w.Code, w.Body.String())
	}
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func paired(t *testing.T, s *Server) protocol.RegisterResponse {
	c := decode[protocol.CreateSessionResponse](t, call(s.Handler(), "POST", "/api/v1/sessions", protocol.CreateSessionRequest{Scope: protocol.Scope{AllowedTasks: []protocol.TaskType{protocol.InspectSystem, protocol.InstallPackage}, AllowedPackages: []string{"git"}}}, nil, ""), 201)
	in := protocol.RegisterRequest{ProtocolVersion: protocol.Version, SessionID: c.Session.ID, PairingToken: c.Pairing.PairingToken, Hostname: "win", OS: "windows", Port: protocol.WorkerPort}
	b, _ := json.Marshal(in)
	r := httptest.NewRequest("POST", "http://localhost/api/v1/worker/register", bytes.NewReader(b))
	security.SignRequest(r, b, c.Pairing.PairingToken, "worker-to-controller")
	w := httptest.NewRecorder()
	s.WorkerHandler().ServeHTTP(w, r)
	rr := decode[protocol.RegisterResponse](t, w, 200)
	if err := security.VerifyResponse(w.Result(), w.Body.Bytes(), c.Pairing.PairingToken, "worker-to-controller", r.Header.Get(security.HeaderNonce)); err != nil {
		t.Fatal("registration response must use pairing key:", err)
	}
	r = httptest.NewRequest("POST", "http://localhost/api/v1/worker/register", bytes.NewReader(b))
	security.SignRequest(r, b, c.Pairing.PairingToken, "worker-to-controller")
	w = httptest.NewRecorder()
	s.WorkerHandler().ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatalf("reused pairing: %d", w.Code)
	}
	return rr
}
func TestApprovalIdempotencyAndCloseRevocation(t *testing.T) {
	s := newTestServer(t, filepath.Join(t.TempDir(), "state", "state.db"))
	rr := paired(t, s)
	spec := protocol.TaskSpec{SessionID: rr.Session.ID, DeviceID: rr.Device.ID, Type: protocol.InstallPackage, Package: &protocol.PackageRef{ID: "git", Version: "2.51.0"}, TimeoutSeconds: 60}
	create := protocol.CreateTaskRequest{Spec: spec, IdempotencyKey: "operation-001"}
	if w := call(s.Handler(), "POST", "/api/v1/tasks", create, nil, ""); w.Code != 403 {
		t.Fatalf("unapproved install: %d %s", w.Code, w.Body.String())
	}
	p := decode[protocol.Plan](t, call(s.Handler(), "POST", "/api/v1/plans", spec, nil, ""), 201)
	a := decode[protocol.Approval](t, call(s.Handler(), "POST", "/api/v1/approvals", protocol.CreateApprovalRequest{PlanID: p.ID}, nil, ""), 201)
	if w := call(s.Handler(), "POST", "/api/v1/approvals/"+a.ID+"/approve", struct{}{}, nil, ""); w.Code != 403 {
		t.Fatal("bearer approved")
	}
	login := call(s.Handler(), "POST", "/api/v1/auth/login", protocol.LoginRequest{Token: admin}, nil, "")
	lr := decode[protocol.LoginResponse](t, login, 200)
	cookie := login.Result().Cookies()[0]
	if w := call(s.Handler(), "POST", "/api/v1/approvals/"+a.ID+"/approve", struct{}{}, cookie, ""); w.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	decode[protocol.Approval](t, call(s.Handler(), "POST", "/api/v1/approvals/"+a.ID+"/approve", struct{}{}, cookie, lr.CSRFToken), 200)
	create.ApprovalID = a.ID
	changed := create
	changed.Spec.TimeoutSeconds = 61
	if w := call(s.Handler(), "POST", "/api/v1/tasks", changed, nil, ""); w.Code != 403 {
		t.Fatalf("changed approval accepted: %d", w.Code)
	}
	first := decode[protocol.Task](t, call(s.Handler(), "POST", "/api/v1/tasks", create, nil, ""), 201)
	second := decode[protocol.Task](t, call(s.Handler(), "POST", "/api/v1/tasks", create, nil, ""), 201)
	if first.ID != second.ID {
		t.Fatal("duplicate execution scheduled")
	}
	if w := call(s.Handler(), "POST", "/api/v1/tasks", changed, nil, ""); w.Code != 409 {
		t.Fatal("same idempotency key accepted changed parameters", w.Code)
	}
	consumed := create
	consumed.IdempotencyKey = "operation-002"
	if w := call(s.Handler(), "POST", "/api/v1/tasks", consumed, nil, ""); w.Code != 403 {
		t.Fatal("approval consumed twice", w.Code)
	}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Go(func() {
			w := call(s.Handler(), "POST", "/api/v1/tasks", create, nil, "")
			if w.Code != 201 {
				t.Error(w.Code)
			}
		})
	}
	wg.Wait()
	decode[protocol.Session](t, call(s.Handler(), "POST", "/api/v1/sessions/"+rr.Session.ID+"/close", struct{}{}, nil, ""), 200)
	if w := call(s.Handler(), "POST", "/api/v1/tasks", create, nil, ""); w.Code != 409 {
		t.Fatal("closed session accepted task", w.Code)
	}
}
func TestRestartAndExpiration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "state.db")
	s := newTestServer(t, path)
	rr := paired(t, s)
	spec := protocol.TaskSpec{SessionID: rr.Session.ID, DeviceID: rr.Device.ID, Type: protocol.InspectSystem, TimeoutSeconds: 60}
	task := decode[protocol.Task](t, call(s.Handler(), "POST", "/api/v1/tasks", protocol.CreateTaskRequest{Spec: spec, IdempotencyKey: "inspect-001"}, nil, ""), 201)
	s.mu.Lock()
	task.State = protocol.TaskRunning
	s.put("tasks", task.ID, task)
	s.mu.Unlock()
	s.Close()
	s = newTestServer(t, path)
	got := decode[protocol.Task](t, call(s.Handler(), "GET", "/api/v1/tasks/"+task.ID, nil, nil, ""), 200)
	if got.State != protocol.Unknown {
		t.Fatal("running task not unknown after restart", got.State)
	}
	s.mu.Lock()
	var rec sessionRecord
	s.get("sessions", rr.Session.ID, &rec)
	rec.Session.ExpiresAt = time.Now().Add(-time.Second)
	s.put("sessions", rec.Session.ID, rec)
	s.mu.Unlock()
	if w := call(s.Handler(), "POST", "/api/v1/tasks", protocol.CreateTaskRequest{Spec: spec, IdempotencyKey: "inspect-002"}, nil, ""); w.Code != 409 {
		t.Fatal("expired session allowed task")
	}
}

func TestManagementRequiresBearerSchemeAndChecksLoginOrigin(t *testing.T) {
	s := newTestServer(t, filepath.Join(t.TempDir(), "state", "state.db"))
	r := httptest.NewRequest("GET", "http://localhost/api/v1/config", nil)
	r.Header.Set("Authorization", admin)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatal("authorization without Bearer scheme accepted")
	}
	body, _ := json.Marshal(protocol.LoginRequest{Token: admin})
	r = httptest.NewRequest("POST", "http://localhost/api/v1/auth/login", bytes.NewReader(body))
	r.Header.Set("Origin", "http://attacker.example")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal("cross-origin login accepted")
	}
}

func TestAgentCredentialCannotLogin(t *testing.T) {
	agentToken := "agent_token_0123456789abcdef0123456789abcdef"
	s, err := New(Options{DBPath: filepath.Join(t.TempDir(), "state", "state.db"), AdminToken: admin, AgentToken: agentToken})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w := call(s.Handler(), "POST", "/api/v1/auth/login", protocol.LoginRequest{Token: agentToken}, nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatal("MCP credential obtained browser approval cookie")
	}
	r := httptest.NewRequest("GET", "http://localhost/api/v1/config", nil)
	r.Header.Set("Authorization", "Bearer "+agentToken)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatal("MCP token cannot access management API")
	}
	r = httptest.NewRequest("POST", "http://localhost/api/v1/approvals/any/approve", strings.NewReader("{}"))
	r.Header.Set("Authorization", "Bearer "+agentToken)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal("MCP token approved")
	}
	body, _ := json.Marshal(protocol.CreateSessionRequest{Scope: protocol.Scope{AllowedTasks: []protocol.TaskType{protocol.InspectSystem}}})
	r = httptest.NewRequest("POST", "http://localhost/api/v1/sessions", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+agentToken)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	events := decode[[]protocol.AuditEvent](t, call(s.Handler(), "GET", "/api/v1/audit-events", nil, nil, ""), 200)
	if len(events) == 0 || events[len(events)-1].Actor != "agent" {
		t.Fatal("MCP operation audit attributed to operator", events)
	}
}
