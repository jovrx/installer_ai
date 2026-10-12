package privilege

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"remoteops/pkg/protocol"
)

func TestPausedHelperCallbackDoesNotBlockCancellationOrState(t *testing.T) {
	for _, kind := range []string{"progress", "stdout", "legacy_result"} {
		t.Run(kind, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			channel := newHelperChannel(helperRequest(), "proof", func(string, string) {
				if calls.Add(1) == 1 {
					close(started)
					<-release
				}
			})
			post := func(path, body string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", path, strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer proof")
				w := httptest.NewRecorder()
				channel.ServeHTTP(w, r)
				return w
			}
			if w := post("/claim", "null"); w.Code != 200 {
				t.Fatal(w.Code)
			}
			path, body := "/events", `{"stream":"stdout","message":"activity"}`
			if kind == "progress" {
				body = `{"stream":"progress","message":"{\"stage\":\"installing\"}"}`
			}
			if kind == "legacy_result" {
				path = "/result"
				body = `{"result":{"verified":true},"logs":[{"stream":"stdout","message":"first"},{"stream":"stdout","message":"second"}]}`
			}
			handled := make(chan *httptest.ResponseRecorder, 1)
			go func() { handled <- post(path, body) }()
			defer func() {
				close(release)
				w := <-handled
				if w.Code != 204 {
					t.Errorf("event/result status %d", w.Code)
				}
				if calls.Load() != 1 {
					t.Errorf("late or cancelled callback forwarded: %d", calls.Load())
				}
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("callback did not start")
			}
			state := make(chan *httptest.ResponseRecorder, 1)
			go func() { state <- post("/state", "null") }()
			select {
			case w := <-state:
				if w.Code != 200 || strings.Contains(w.Body.String(), `"cancelled":true`) {
					t.Fatalf("state %d %s", w.Code, w.Body)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("paused callback blocked state polling")
			}
			cancelled := make(chan struct{})
			go func() { channel.cancel(); close(cancelled) }()
			select {
			case <-cancelled:
			case <-time.After(500 * time.Millisecond):
				t.Fatal("paused callback blocked cancellation")
			}
			go func() { state <- post("/state", "null") }()
			select {
			case w := <-state:
				if !strings.Contains(w.Body.String(), `"cancelled":true`) {
					t.Fatalf("cancellation not observable: %s", w.Body)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("cancelled state polling blocked")
			}
			late := make(chan *httptest.ResponseRecorder, 1)
			go func() { late <- post("/events", `{"stream":"stdout","message":"late"}`) }()
			select {
			case w := <-late:
				if w.Code != 409 {
					t.Fatalf("late event status %d", w.Code)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("late event rejection blocked")
			}
		})
	}
}

type pausedEventBody struct{ started, release chan struct{} }

func (b *pausedEventBody) Read([]byte) (int, error) { close(b.started); <-b.release; return 0, io.EOF }

func TestSlowEventBodyDoesNotDelayCancellation(t *testing.T) {
	channel := newHelperChannel(helperRequest(), "proof", nil)
	claim := httptest.NewRequest("POST", "/claim", nil)
	claim.Header.Set("Authorization", "Bearer proof")
	channel.ServeHTTP(httptest.NewRecorder(), claim)
	body := &pausedEventBody{started: make(chan struct{}), release: make(chan struct{})}
	request := httptest.NewRequest("POST", "/events", body)
	request.Header.Set("Authorization", "Bearer proof")
	handled := make(chan struct{})
	go func() { channel.ServeHTTP(httptest.NewRecorder(), request); close(handled) }()
	<-body.started
	cancelled := make(chan struct{})
	go func() { channel.cancel(); close(cancelled) }()
	select {
	case <-cancelled:
	case <-time.After(500 * time.Millisecond):
		close(body.release)
		<-handled
		<-cancelled
		t.Fatal("slow event body blocked cancellation")
	}
	close(body.release)
	<-handled
}

func helperRequest() Request {
	r := Request{Spec: protocol.TaskSpec{SessionID: "s", DeviceID: "d", Type: protocol.InstallPackage, Package: &protocol.PackageRef{ID: "git", Version: "1"}, RequiresAdmin: true, TimeoutSeconds: 30}, Entry: protocol.CatalogEntry{ID: "git", Version: "1", Installer: "winget", WingetID: "Git.Git", Source: "winget", RequiresAdmin: true}}
	r.Digest = protocol.TaskDigest(r.Spec)
	return r
}

func TestLiveHelperEventsAuthenticateBoundAndRespectState(t *testing.T) {
	var seen []string
	channel := newHelperChannel(helperRequest(), "proof", func(stream, message string) { seen = append(seen, stream+":"+message) })
	call := func(path, body, proof string) int {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+proof)
		w := httptest.NewRecorder()
		channel.ServeHTTP(w, r)
		return w.Code
	}
	event := `{"stream":"progress","message":"{\"stage\":\"installing\"}"}`
	if got := call("/events", event, "wrong"); got != 401 {
		t.Fatalf("unauthenticated %d", got)
	}
	if got := call("/events", event, "proof"); got != 409 {
		t.Fatalf("unclaimed %d", got)
	}
	if got := call("/claim", "null", "proof"); got != 200 {
		t.Fatalf("claim %d", got)
	}
	for _, bad := range []string{`{"stream":"command","message":"cmd"}`, `{"stream":"progress","message":"bad"}`, `{"stream":"stdout","message":"ok","spec":{}}`, `{"stream":"stdout","message":"ok"} {}`} {
		if got := call("/events", bad, "proof"); got != 400 {
			t.Fatalf("invalid event %d %s", got, bad)
		}
	}
	if got := call("/events", strings.Repeat("x", 8193), "proof"); got != 413 {
		t.Fatalf("oversized %d", got)
	}
	if got := call("/events", event, "proof"); got != 204 || len(seen) != 1 {
		t.Fatalf("progress %d seen %v", got, seen)
	}
	if got := call("/events", `{"stream":"stderr","message":"token=private"}`, "proof"); got != 204 {
		t.Fatal(got)
	}
	if strings.Contains(strings.Join(seen, ""), "private") {
		t.Fatal("helper secret leaked")
	}
	if got := call("/result", `{"result":{"verified":true},"logs":[{"stream":"stderr","message":"token=private"}]}`, "proof"); got != 204 {
		t.Fatal(got)
	}
	if len(seen) != 2 {
		t.Fatal("streamed result logs replayed")
	}
	if got := call("/events", event, "proof"); got != 409 {
		t.Fatalf("finished events %d", got)
	}
}

func TestHelperSessionStreamsBeforeResultAndObservesCancellation(t *testing.T) {
	var mu sync.Mutex
	logs := []string{}
	live := make(chan struct{}, 1)
	channel := newHelperChannel(helperRequest(), strings.Repeat("p", 40), func(stream, message string) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, stream+":"+message)
		if stream == "stdout" {
			select {
			case live <- struct{}{}:
			default:
			}
		}
	})
	srv := httptest.NewServer(channel)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		finished <- runHelperSession(ctx, descriptor{Endpoint: srv.URL, Proof: strings.Repeat("p", 40)}, func(taskCtx context.Context, r Request, emit func(string, string)) (protocol.TaskResult, error) {
			emit("progress", `{"stage":"installing"}`)
			emit("stdout", "Authorization: Bearer private\n"+strings.Repeat("<", 1800))
			<-taskCtx.Done()
			return protocol.TaskResult{}, taskCtx.Err()
		})
	}()
	select {
	case <-live:
	case <-ctx.Done():
		t.Fatal("no event before result")
	}
	select {
	case <-channel.done:
		t.Fatal("helper finished before cancellation")
	default:
	}
	channel.cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("cancellation not propagated")
	}
	select {
	case out := <-channel.done:
		if out.Error == "" {
			t.Fatal("missing cancellation result")
		}
	case <-ctx.Done():
		t.Fatal("no result")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logs) != 2 || strings.Contains(strings.Join(logs, ""), "private") {
		t.Fatalf("logs %v", logs)
	}
}

func TestHelperForwardingIsBoundedButProgressSurvivesLogLimit(t *testing.T) {
	logs, progress := 0, 0
	channel := newHelperChannel(helperRequest(), "proof", func(stream, message string) {
		if stream == "progress" {
			progress++
		} else {
			logs++
		}
	})
	post := func(path string, v any) {
		b, _ := json.Marshal(v)
		r := httptest.NewRequest("POST", path, bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer proof")
		w := httptest.NewRecorder()
		channel.ServeHTTP(w, r)
		if w.Code != 200 && w.Code != 204 {
			t.Fatal(w.Code)
		}
	}
	post("/claim", nil)
	for range 1000 {
		post("/events", helperEvent{Stream: "stdout", Message: strings.Repeat("x", 2000)})
	}
	post("/events", helperEvent{Stream: "progress", Message: `{"stage":"verifying_version"}`})
	if logs > 40 || logs == 0 || progress != 1 {
		t.Fatalf("logs %d progress %d", logs, progress)
	}
}
