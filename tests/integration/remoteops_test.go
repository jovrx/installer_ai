package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"remoteops/integrations/mcp"
	"remoteops/internal/controller"
	"remoteops/internal/security"
	"remoteops/internal/worker"
	"remoteops/pkg/protocol"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type executor struct{ installs atomic.Int32 }

func (e *executor) Execute(ctx context.Context, s protocol.TaskSpec, c protocol.CatalogEntry, emit func(string, string)) (protocol.TaskResult, error) {
	emit("stdout", "token=private-value")
	emit("stdout", "controlled execution")
	if s.Type == protocol.InspectSystem {
		return protocol.TaskResult{Info: &protocol.SystemInfo{OS: "Windows simulation", CPU: "test CPU", Software: map[string]string{}}}, nil
	}
	e.installs.Add(1)
	code := 0
	return protocol.TaskResult{ExitCode: &code, Verified: true, Version: c.Version}, nil
}

type harness struct {
	server            *controller.Server
	api, registration *httptest.Server
	operator          *http.Client
	csrf              string
	worker            *worker.Server
	exec              *executor
	clean             atomic.Bool
	closedNodes       atomic.Int32
	cancel            context.CancelFunc
	agent, admin      string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{agent: strings.Repeat("a", 48), admin: strings.Repeat("o", 48), exec: &executor{}}
	client := &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "100.64.0.2:9081" || h.worker == nil {
			return nil, fmt.Errorf("untrusted or unavailable worker")
		}
		rec := httptest.NewRecorder()
		h.worker.Handler().ServeHTTP(rec, r)
		return rec.Result(), nil
	})}
	catalog := []protocol.CatalogEntry{{ID: "git", Name: "Git", Version: "2.51.0", Installer: "exe", URL: "https://example.org/git.exe", SHA256: strings.Repeat("a", 64)}}
	var err error
	h.server, err = controller.New(controller.Options{DBPath: filepath.Join(t.TempDir(), "state", "remoteops.db"), AdminToken: h.admin, AgentToken: h.agent, ControllerName: "Test operator", ControllerURL: "http://100.64.0.1:9080", Catalog: catalog, HTTPClient: client, TickInterval: 5 * time.Millisecond, PeerResolver: controller.PeerResolverFunc(func(context.Context, *http.Request) (controller.Peer, error) {
		return controller.Peer{Address: "100.64.0.2", NodeID: "123"}, nil
	}), NodeCleaner: controller.NodeCleanerFunc(func(context.Context, string) error {
		if !h.clean.Load() {
			return fmt.Errorf("simulated cleanup failure")
		}
		h.closedNodes.Add(1)
		return nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	h.api = httptest.NewServer(h.server.Handler())
	h.registration = httptest.NewServer(h.server.WorkerHandler())
	jar, _ := cookiejar.New(nil)
	h.operator = &http.Client{Jar: jar}
	t.Cleanup(func() {
		if h.cancel != nil {
			h.cancel()
		}
		if h.worker != nil {
			h.worker.Close()
			h.worker.Wait()
		}
		h.api.Close()
		h.registration.Close()
		h.server.Close()
	})
	var login protocol.LoginResponse
	h.request(t, true, "POST", "/auth/login", protocol.LoginRequest{Token: h.admin}, &login, 200)
	h.csrf = login.CSRFToken
	return h
}
func (h *harness) request(t *testing.T, operator bool, method, path string, in, out any, want int) {
	t.Helper()
	var body []byte
	if in != nil {
		body, _ = json.Marshal(in)
	}
	r, _ := http.NewRequest(method, h.api.URL+"/api/v1"+path, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	client := h.api.Client()
	if operator {
		client = h.operator
		r.Header.Set("Origin", h.api.URL)
		r.Header.Set("X-CSRF-Token", h.csrf)
	} else {
		r.Header.Set("Authorization", "Bearer "+h.agent)
	}
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != want {
		var e protocol.ErrorEnvelope
		json.NewDecoder(resp.Body).Decode(&e)
		t.Fatalf("%s %s status %d want %d: %+v", method, path, resp.StatusCode, want, e)
	}
	if out != nil {
		if err = json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
}
func (h *harness) pair(t *testing.T) protocol.RegisterResponse {
	t.Helper()
	var created protocol.CreateSessionResponse
	h.request(t, false, "POST", "/sessions", protocol.CreateSessionRequest{Scope: protocol.Scope{AllowedTasks: []protocol.TaskType{protocol.InspectSystem, protocol.InstallPackage}, AllowedPackages: []string{"git"}}}, &created, 201)
	// Changing a default after pairing must not widen already disclosed local consent.
	config := protocol.DefaultConfig()
	config.SessionTTLSeconds = 7200
	h.request(t, true, "PUT", "/config", config, nil, 200)
	in := protocol.RegisterRequest{ProtocolVersion: protocol.Version, SessionID: created.Session.ID, PairingToken: created.Pairing.PairingToken, Hostname: "test-pc", OS: "Windows simulated", ClientVersion: "1.0.0", Port: protocol.WorkerPort}
	var registered protocol.RegisterResponse
	if err := security.SignedJSON(t.Context(), h.registration.Client(), "POST", h.registration.URL+"/api/v1/worker/register", in.PairingToken, "worker-to-controller", in, &registered); err != nil {
		t.Fatal(err)
	}
	if registered.Session.ExpiresAt.After(time.Now().Add(time.Duration(created.Pairing.AuthorizationSeconds)*time.Second + time.Second)) {
		t.Fatal("configuration change widened pairing consent")
	}
	var err error
	h.worker, err = worker.New(worker.Options{Session: registered.Session, DeviceID: registered.Device.ID, SessionToken: registered.SessionToken, Catalog: registered.Catalog, Executor: h.exec})
	if err != nil {
		t.Fatal(err)
	}
	if err := security.SignedJSON(t.Context(), h.registration.Client(), "POST", h.registration.URL+"/api/v1/worker/register", in.PairingToken, "worker-to-controller", in, &registered); err == nil {
		t.Fatal("pairing credential reused")
	}
	ctx, cancel := context.WithCancel(t.Context())
	h.cancel = cancel
	h.server.Start(ctx)
	return registered
}
func (h *harness) waitTask(t *testing.T, id string) protocol.Task {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var task protocol.Task
		h.request(t, false, "GET", "/tasks/"+id, nil, &task, 200)
		if task.State.Terminal() {
			return task
		}
		select {
		case <-deadline.C:
			t.Fatalf("task did not finish: %+v", task)
		case <-ticker.C:
		}
	}
}
func TestPairInspectApproveInstallClose(t *testing.T) {
	h := newHarness(t)
	paired := h.pair(t)
	spec := protocol.TaskSpec{SessionID: paired.Session.ID, DeviceID: paired.Device.ID, Type: protocol.InspectSystem, TimeoutSeconds: 30}
	var check protocol.Task
	h.request(t, false, "POST", "/tasks", protocol.CreateTaskRequest{Spec: spec, IdempotencyKey: "inspect-once"}, &check, 201)
	check = h.waitTask(t, check.ID)
	if check.State != protocol.Succeeded || check.Result == nil || check.Result.Info == nil {
		t.Fatalf("inspection missing: %+v", check)
	}
	spec.Type = protocol.InstallPackage
	spec.Package = &protocol.PackageRef{ID: "git", Version: "2.51.0"}
	var plan protocol.Plan
	h.request(t, false, "POST", "/plans", spec, &plan, 201)
	var approval protocol.Approval
	h.request(t, false, "POST", "/approvals", protocol.CreateApprovalRequest{PlanID: plan.ID}, &approval, 201)
	h.request(t, false, "POST", "/approvals/"+approval.ID+"/approve", struct{}{}, nil, 403)
	h.request(t, false, "POST", "/auth/login", protocol.LoginRequest{Token: h.agent}, nil, 401)
	h.request(t, true, "POST", "/approvals/"+approval.ID+"/approve", struct{}{}, &approval, 200)
	in := protocol.CreateTaskRequest{Spec: spec, ApprovalID: approval.ID, IdempotencyKey: "install-once"}
	var task protocol.Task
	h.request(t, false, "POST", "/tasks", in, &task, 201)
	var replay protocol.Task
	h.request(t, false, "POST", "/tasks", in, &replay, 201)
	if replay.ID != task.ID {
		t.Fatal("Controller created duplicate install")
	}
	task = h.waitTask(t, task.ID)
	if task.State != protocol.Succeeded || task.Result == nil || !task.Result.Verified || h.exec.installs.Load() != 1 {
		t.Fatalf("installation failed or duplicated: %+v", task)
	}
	var logs protocol.LogsPage
	h.request(t, false, "GET", "/tasks/"+task.ID+"/logs?after=0&limit=100", nil, &logs, 200)
	if len(logs.Entries) == 0 {
		t.Fatal("missing live logs")
	}
	for _, entry := range logs.Entries {
		if strings.Contains(entry.Message, "private-value") {
			t.Fatal("credential leaked")
		}
	}
	tool := mcp.New(h.api.URL, h.agent, h.api.Client())
	raw, err := tool.Call(t.Context(), "list_devices", json.RawMessage(`{}`))
	if err != nil || !bytes.Contains(raw, []byte(paired.Device.ID)) {
		t.Fatalf("MCP not using same API: %s %v", raw, err)
	}
	var session protocol.Session
	h.request(t, false, "POST", "/sessions/"+paired.Session.ID+"/close", struct{}{}, &session, 200)
	if session.State.Active() {
		t.Fatal("close kept application authorization")
	}
	in.IdempotencyKey = "after-close"
	h.request(t, false, "POST", "/tasks", in, nil, 409)
	select {
	case <-h.worker.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Controller did not revoke Worker")
	}
	req := protocol.TaskDispatch{Task: task, ApprovalDigest: task.Digest}
	rec := httptest.NewRecorder()
	body, _ := json.Marshal(req)
	r, _ := http.NewRequest("POST", "http://100.64.0.2:9081/v1/tasks", bytes.NewReader(body))
	security.SignRequest(r, body, paired.SessionToken, "controller-to-worker")
	h.worker.Handler().ServeHTTP(rec, r)
	if rec.Code != 409 {
		t.Fatal("Worker accepted revoked session")
	}
	h.clean.Store(true)
	h.request(t, false, "POST", "/sessions/"+paired.Session.ID+"/retry-cleanup", struct{}{}, nil, 200)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for h.closedNodes.Load() == 0 {
		select {
		case <-deadline.C:
			t.Fatal("node cleanup did not retry")
		case <-tick.C:
		}
	}
}
