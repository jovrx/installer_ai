package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"remoteops/internal/security"
	"remoteops/pkg/protocol"
)

type pausedTaskWriter struct {
	*httptest.ResponseRecorder
	started, release chan struct{}
}

func (w *pausedTaskWriter) WriteHeader(status int) {
	close(w.started)
	<-w.release
	w.ResponseRecorder.WriteHeader(status)
}

func TestPausedSignedResponseDoesNotBlockLocalCancelOrClose(t *testing.T) {
	for _, route := range []string{"task", "remote_cancel", "logs", "missing", "unsupported", "submit", "duplicate", "idempotency_alias", "conflict", "used_approval"} {
		t.Run(route, func(t *testing.T) {
			started := make(chan struct{}, 1)
			s := testServer(t, executorFunc(func(ctx context.Context, _ protocol.TaskSpec, _ protocol.CatalogEntry, emit func(string, string)) (protocol.TaskResult, error) {
				emit("stdout", "activity")
				started <- struct{}{}
				<-ctx.Done()
				return protocol.TaskResult{}, ctx.Err()
			}))
			d := dispatch(true)
			d.Task.Spec.TimeoutSeconds = 30
			d.Task.Digest = protocol.TaskDigest(d.Task.Spec)
			d.ApprovalDigest = d.Task.Digest
			if w := rpc(t, s, "POST", "/v1/tasks", d, true); w.Code != 202 {
				t.Fatal(w.Body.String())
			}
			<-started
			method, path, want, value := "GET", "/v1/tasks/task_test", 200, any(nil)
			switch route {
			case "remote_cancel":
				method, path = "POST", "/v1/tasks/task_test/cancel"
			case "logs":
				path = "/v1/tasks/task_test/logs"
			case "missing":
				path, want = "/v1/tasks/missing", 404
			case "unsupported":
				method, want = "DELETE", 405
			case "submit":
				method, path, want = "POST", "/v1/tasks", 202
				d.Task.ID = "task_new"
				d.Task.IdempotencyKey = "idempotent_new"
				d.Task.ApprovalID = "apr_new"
				value = d
			case "duplicate":
				method, path, value = "POST", "/v1/tasks", d
			case "idempotency_alias":
				method, path = "POST", "/v1/tasks"
				d.Task.ID = "task_alias"
				value = d
			case "conflict":
				method, path, want = "POST", "/v1/tasks", 409
				d.Task.Spec.TimeoutSeconds = 31
				d.Task.Digest = protocol.TaskDigest(d.Task.Spec)
				d.ApprovalDigest = d.Task.Digest
				value = d
			case "used_approval":
				method, path, want = "POST", "/v1/tasks", 409
				d.Task.ID = "task_new"
				d.Task.IdempotencyKey = "idempotent_new"
				value = d
			}
			var body []byte
			if value != nil {
				body, _ = json.Marshal(value)
			}
			r := httptest.NewRequest(method, path, bytes.NewReader(body))
			if err := security.SignRequest(r, body, "test-secret", "controller-to-worker"); err != nil {
				t.Fatal(err)
			}
			w := &pausedTaskWriter{ResponseRecorder: httptest.NewRecorder(), started: make(chan struct{}), release: make(chan struct{})}
			handled := make(chan struct{})
			go func() { s.Handler().ServeHTTP(w, r); close(handled) }()
			defer func() {
				close(w.release)
				<-handled
				s.Close()
				s.Wait()
				if w.Code != want {
					t.Errorf("status %d want %d: %s", w.Code, want, w.Body)
				}
				if err := security.VerifyResponse(w.Result(), w.Body.Bytes(), "test-secret", "controller-to-worker", r.Header.Get(security.HeaderNonce)); err != nil {
					t.Error(err)
				}
			}()
			select {
			case <-w.started:
			case <-time.After(time.Second):
				t.Fatal("response write did not start")
			}
			cancelled := make(chan error, 1)
			go func() { cancelled <- s.CancelLocalTask("task_test") }()
			select {
			case err := <-cancelled:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("paused response blocked local cancellation")
			}
			closed := make(chan struct{})
			go func() { s.CloseWithReason("local_closed"); close(closed) }()
			select {
			case <-closed:
			case <-time.After(500 * time.Millisecond):
				t.Fatal("paused response blocked local close")
			}
		})
	}
}

// Removing progress routing, cancellation precedence, or snapshot copies breaks this test.
func TestLocalProgressDuringSignedExecution(t *testing.T) {
	started := make(chan func(string, string), 1)
	release := make(chan struct{})
	s := testServer(t, executorFunc(func(ctx context.Context, _ protocol.TaskSpec, _ protocol.CatalogEntry, emit func(string, string)) (protocol.TaskResult, error) {
		emit("stdout", "token=private")
		emit("progress", `{"stage":"downloading","completed_bytes":12,"total_bytes":30}`)
		started <- emit
		<-release
		return protocol.TaskResult{Verified: true, Info: &protocol.SystemInfo{Software: map[string]string{"git": "1"}}}, nil
	}))
	defer close(release)
	if w := rpc(t, s, "POST", "/v1/tasks", dispatch(true), true); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	emit := <-started
	views := s.LocalStatus()
	if len(views) != 1 || views[0].PackageName != "Git" || views[0].Progress.Stage != "downloading" || views[0].Progress.CompletedBytes != 12 || views[0].Task.State != protocol.TaskRunning {
		t.Fatalf("live view: %+v", views)
	}
	if len(views[0].Logs) != 1 || strings.Contains(views[0].Logs[0].Message, "private") {
		t.Fatalf("logs: %+v", views[0].Logs)
	}
	views[0].Task.Spec.Package.ID = "mutated"
	views[0].Logs[0].Message = "mutated"
	if next := s.LocalStatus()[0]; next.Task.Spec.Package.ID != "git" || next.Logs[0].Message == "mutated" {
		t.Fatal("snapshot aliases worker state")
	}
	emit("progress", `{"stage":"installing","completed_bytes":-1}`)
	if s.LocalStatus()[0].Progress.Stage != "downloading" {
		t.Fatal("invalid progress accepted")
	}
	if err := s.CancelLocalTask("missing"); err == nil {
		t.Fatal("missing task accepted")
	}
	if err := s.CancelLocalTask("task_test"); err != nil {
		t.Fatal(err)
	}
	emit("progress", `{"stage":"installing"}`)
	if s.LocalStatus()[0].Progress.Stage != "cancelling" {
		t.Fatal("late progress overwrote cancellation")
	}
	// Release explicitly so completion and a later callback can be inspected.
	release <- struct{}{}
	s.Close()
	s.Wait()
	emit("progress", `{"stage":"installing"}`)
	final := s.LocalStatus()[0]
	if final.Task.State != protocol.Cancelled || final.Progress.Stage != "cancelled" {
		t.Fatalf("terminal view: %+v", final)
	}
	final.Task.Result.Info.Software["git"] = "mutated"
	if s.LocalStatus()[0].Task.Result.Info.Software["git"] != "1" {
		t.Fatal("result aliases worker state")
	}
}

func TestLocalHistoryBoundedAndStable(t *testing.T) {
	s := testServer(t, executorFunc(func(context.Context, protocol.TaskSpec, protocol.CatalogEntry, func(string, string)) (protocol.TaskResult, error) {
		return protocol.TaskResult{}, nil
	}))
	for i := 0; i < 105; i++ {
		d := dispatch(false)
		d.Task.ID = fmt.Sprintf("task_%03d", i)
		d.Task.IdempotencyKey = fmt.Sprintf("idempotent_%03d", i)
		if w := rpc(t, s, "POST", "/v1/tasks", d, true); w.Code != 202 {
			t.Fatal(w.Body.String())
		}
	}
	s.Close()
	s.Wait()
	a, b := s.LocalStatus(), s.LocalStatus()
	if len(a) != 100 {
		t.Fatalf("history length %d", len(a))
	}
	if a[0].Task.ID != "task_104" || a[99].Task.ID != "task_005" {
		t.Fatalf("history endpoints: %s %s", a[0].Task.ID, a[99].Task.ID)
	}
	for i := range a {
		if a[i].Task.ID != b[i].Task.ID {
			t.Fatal("unstable ordering")
		}
	}
}

func TestCloseReasonsAreFirstAndCancelTasks(t *testing.T) {
	for _, reason := range []string{"expired", "heartbeat_lost", "controller_closed", "local_closed"} {
		t.Run(reason, func(t *testing.T) {
			started := make(chan struct{})
			s := testServer(t, executorFunc(func(ctx context.Context, _ protocol.TaskSpec, _ protocol.CatalogEntry, emit func(string, string)) (protocol.TaskResult, error) {
				close(started)
				<-ctx.Done()
				return protocol.TaskResult{}, ctx.Err()
			}))
			rpc(t, s, "POST", "/v1/tasks", dispatch(false), true)
			<-started
			switch reason {
			case "expired":
				s.mu.Lock()
				s.opt.Session.ExpiresAt = time.Now().Add(-time.Second)
				s.mu.Unlock()
				rpc(t, s, "GET", "/health", nil, true)
			case "heartbeat_lost":
				s.mu.Lock()
				s.lastController = time.Now().Add(-time.Hour)
				s.mu.Unlock()
				rpc(t, s, "GET", "/health", nil, true)
			case "controller_closed":
				rpc(t, s, "POST", "/v1/session/close", nil, true)
			default:
				s.CloseWithReason(reason)
			}
			s.Wait()
			s.Close()
			s.CloseWithReason("other")
			if s.EndReason() != reason || s.LocalStatus()[0].Progress.Stage != "cancelled" {
				t.Fatalf("reason %s view %+v", s.EndReason(), s.LocalStatus())
			}
		})
	}
}
