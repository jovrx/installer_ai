package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"remoteops/internal/security"
	"remoteops/pkg/protocol"
)

type executorFunc func(context.Context, protocol.TaskSpec, protocol.CatalogEntry, func(string, string)) (protocol.TaskResult, error)

func (f executorFunc) Execute(c context.Context, s protocol.TaskSpec, e protocol.CatalogEntry, l func(string, string)) (protocol.TaskResult, error) {
	return f(c, s, e, l)
}
func testServer(t *testing.T, e Executor) *Server {
	t.Helper()
	s, err := New(Options{Session: protocol.Session{ID: "sess_test", State: protocol.Online, ExpiresAt: time.Now().Add(time.Hour), Scope: protocol.Scope{AllowedTasks: []protocol.TaskType{protocol.InspectSystem, protocol.InstallPackage}, AllowedPackages: []string{"git"}}}, DeviceID: "dev_test", SessionToken: "test-secret", Catalog: []protocol.CatalogEntry{{ID: "git", Name: "Git", Version: "2.50.0", Installer: "winget", WingetID: "Git.Git", Source: "winget"}}, Executor: e})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func dispatch(install bool) protocol.TaskDispatch {
	sp := protocol.TaskSpec{SessionID: "sess_test", DeviceID: "dev_test", Type: protocol.InspectSystem, TimeoutSeconds: 2}
	if install {
		sp.Type = protocol.InstallPackage
		sp.Package = &protocol.PackageRef{ID: "git", Version: "2.50.0"}
	}
	d := protocol.TaskDispatch{Task: protocol.Task{ID: "task_test", Spec: sp, IdempotencyKey: "idempotent_test", Digest: protocol.TaskDigest(sp)}}
	if install {
		d.Task.ApprovalID = "apr_test"
		d.ApprovalDigest = d.Task.Digest
	}
	return d
}
func rpc(t *testing.T, s *Server, method, path string, v any, signed bool) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(v)
	if v == nil {
		b = nil
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	if signed {
		security.SignRequest(r, b, "test-secret", "controller-to-worker")
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if signed {
		if err := security.VerifyResponse(w.Result(), w.Body.Bytes(), "test-secret", "controller-to-worker", r.Header.Get(security.HeaderNonce)); err != nil {
			t.Fatal(err)
		}
	}
	return w
}
func TestSignedValidation(t *testing.T) {
	s := testServer(t, executorFunc(func(context.Context, protocol.TaskSpec, protocol.CatalogEntry, func(string, string)) (protocol.TaskResult, error) {
		return protocol.TaskResult{}, nil
	}))
	if w := rpc(t, s, "GET", "/health", nil, false); w.Code != 401 {
		t.Fatalf("unsigned status %d", w.Code)
	}
	for _, mut := range []func(*protocol.TaskDispatch){func(d *protocol.TaskDispatch) {
		d.Task.Spec.DeviceID = "other"
		d.Task.Digest = protocol.TaskDigest(d.Task.Spec)
	}, func(d *protocol.TaskDispatch) { d.Task.Digest = "wrong" }, func(d *protocol.TaskDispatch) { d.ApprovalDigest = "wrong" }, func(d *protocol.TaskDispatch) {
		d.Task.Spec.Package.ID = "other"
		d.Task.Digest = protocol.TaskDigest(d.Task.Spec)
		d.ApprovalDigest = d.Task.Digest
	}} {
		d := dispatch(true)
		mut(&d)
		if w := rpc(t, s, "POST", "/v1/tasks", d, true); w.Code < 400 {
			t.Fatalf("invalid dispatch accepted: %s", w.Body)
		}
	}
	r := httptest.NewRequest("GET", "/health", nil)
	security.SignRequest(r, nil, "test-secret", "controller-to-worker")
	s.Handler().ServeHTTP(httptest.NewRecorder(), r)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("nonce replay accepted")
	}
	s.Close()
	if w := rpc(t, s, "POST", "/v1/tasks", dispatch(false), true); w.Code != 409 {
		t.Fatalf("closed worker status %d", w.Code)
	}
}
func TestConcurrentDuplicateInstall(t *testing.T) {
	var calls atomic.Int32
	done := make(chan struct{})
	s := testServer(t, executorFunc(func(c context.Context, _ protocol.TaskSpec, _ protocol.CatalogEntry, emit func(string, string)) (protocol.TaskResult, error) {
		calls.Add(1)
		emit("stdout", "Authorization: Bearer top-secret")
		<-done
		return protocol.TaskResult{Verified: true}, nil
	}))
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			w := rpc(t, s, "POST", "/v1/tasks", dispatch(true), true)
			if w.Code != 200 && w.Code != 202 {
				t.Errorf("dispatch status %d: %s", w.Code, w.Body)
			}
		})
	}
	wg.Wait()
	close(done)
	for range 100 {
		w := rpc(t, s, "GET", "/v1/tasks/task_test", nil, true)
		var task protocol.Task
		json.Unmarshal(w.Body.Bytes(), &task)
		if task.State.Terminal() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Fatalf("replayed install executed %d times", calls.Load())
	}
	w := rpc(t, s, "GET", "/v1/tasks/task_test/logs", nil, true)
	if strings.Contains(w.Body.String(), "top-secret") {
		t.Fatal("credential leaked")
	}
	d := dispatch(true)
	d.Task.Spec.TimeoutSeconds = 3
	d.Task.Digest = protocol.TaskDigest(d.Task.Spec)
	d.ApprovalDigest = d.Task.Digest
	if w := rpc(t, s, "POST", "/v1/tasks", d, true); w.Code != 409 {
		t.Fatal("changed duplicate accepted")
	}
}
func TestCancelAndTimeout(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "cancel"}[cancel], func(t *testing.T) {
			started := make(chan struct{})
			s := testServer(t, executorFunc(func(c context.Context, _ protocol.TaskSpec, _ protocol.CatalogEntry, _ func(string, string)) (protocol.TaskResult, error) {
				close(started)
				<-c.Done()
				return protocol.TaskResult{}, c.Err()
			}))
			d := dispatch(false)
			d.Task.Spec.TimeoutSeconds = 1
			d.Task.Digest = protocol.TaskDigest(d.Task.Spec)
			rpc(t, s, "POST", "/v1/tasks", d, true)
			<-started
			if cancel {
				rpc(t, s, "POST", "/v1/tasks/task_test/cancel", nil, true)
			}
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				w := rpc(t, s, "GET", "/v1/tasks/task_test", nil, true)
				var task protocol.Task
				json.Unmarshal(w.Body.Bytes(), &task)
				if task.State.Terminal() {
					want := protocol.TimedOut
					if cancel {
						want = protocol.Cancelled
					}
					if task.State != want {
						t.Fatalf("state %s", task.State)
					}
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("task did not terminate")
		})
	}
}

func TestLogsBoundEncodedBytes(t *testing.T) {
	finished := make(chan struct{})
	s := testServer(t, executorFunc(func(_ context.Context, _ protocol.TaskSpec, _ protocol.CatalogEntry, emit func(string, string)) (protocol.TaskResult, error) {
		emit("stdout", strings.Repeat("\x00", 2048))
		close(finished)
		return protocol.TaskResult{}, nil
	}))
	s.opt.LogMaxBytes = 1024
	rpc(t, s, "POST", "/v1/tasks", dispatch(false), true)
	<-finished
	w := rpc(t, s, "GET", "/v1/tasks/task_test/logs", nil, true)
	if w.Body.Len() > 1024 {
		t.Fatalf("encoded logs exceed budget: %d", w.Body.Len())
	}
	var page protocol.LogsPage
	json.Unmarshal(w.Body.Bytes(), &page)
	if !page.Truncated {
		t.Fatal("truncation invisible")
	}
}
