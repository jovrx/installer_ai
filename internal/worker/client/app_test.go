package client

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"remoteops/internal/worker"
	"remoteops/pkg/protocol"
)

func pairingFixture(t *testing.T) protocol.PairingBundle {
	t.Helper()
	b := protocol.PairingBundle{ProtocolVersion: protocol.Version, ControllerName: "我的控制端", ControllerURL: "http://controller:9080", SessionID: "sess_test", PairingToken: "opaque-pairing-secret", ExpiresAt: time.Now().Add(time.Hour), AuthorizationSeconds: 3600, Scope: protocol.Scope{AllowedTasks: []protocol.TaskType{protocol.InspectSystem, protocol.InstallPackage}, AllowedPackages: []string{"git"}, AllowAdmin: true}, Catalog: []protocol.CatalogEntry{{ID: "git", Name: "Git", Version: "2.51.0", Installer: "winget", WingetID: "Git.Git", Source: "winget", RequiresAdmin: true}}}
	b.CatalogDigest = protocol.Digest(b.Catalog)
	if err := worker.ValidatePairing(b); err != nil {
		t.Fatal(err)
	}
	return b
}
func writePairing(t *testing.T, b protocol.PairingBundle) string {
	t.Helper()
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return writeFixture(t, string(data))
}
func writeFixture(t *testing.T, data string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pairing.json")
	if err := os.WriteFile(p, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func awaitState(t *testing.T, a *App, state string) Snapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s := a.Snapshot()
		if s.State == state {
			return s
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("wanted %s, got %+v", state, a.Snapshot())
	return Snapshot{}
}

// Only the externally connected transport is replaced. Pairing parsing, consent,
// state transitions and snapshot copying remain production behavior.
type testConnection struct {
	mu        sync.Mutex
	session   protocol.Session
	tasks     []worker.LocalTaskView
	done      chan struct{}
	reason    string
	waited    atomic.Bool
	cancelled string
}

func newConnection(b protocol.PairingBundle) *testConnection {
	return &testConnection{session: protocol.Session{ID: b.SessionID, DeviceID: "dev_test", State: protocol.Online, Scope: b.Scope, ExpiresAt: time.Now().Add(time.Hour)}, done: make(chan struct{})}
}
func (c *testConnection) Snapshot() []worker.LocalTaskView {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tasks
}
func (c *testConnection) Session() protocol.Session { return c.session }
func (c *testConnection) CancelTask(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancelled = id
	return nil
}
func (c *testConnection) Disconnect(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reason == "" {
		c.reason = reason
		close(c.done)
	}
}
func (c *testConnection) Done() <-chan struct{} { return c.done }
func (c *testConnection) Wait()                 { <-c.done; c.waited.Store(true) }
func (c *testConnection) EndReason() string     { c.mu.Lock(); defer c.mu.Unlock(); return c.reason }

func TestLoadingPairingRequiresExplicitConsent(t *testing.T) {
	var calls atomic.Int32
	a := New(context.Background(), Options{Connect: func(context.Context, protocol.PairingBundle, []byte) (Connection, error) {
		calls.Add(1)
		return nil, errors.New("should not connect")
	}})
	defer func() { a.Disconnect("local_closed"); a.Wait() }()
	if a.Snapshot().State != "idle" {
		t.Fatal(a.Snapshot())
	}
	if err := a.ConsentAndConnect([]byte("tskey-auth-before-consent")); err == nil {
		t.Fatal("missing consent boundary")
	}
	if err := a.LoadPairing(writePairing(t, pairingFixture(t))); err != nil {
		t.Fatal(err)
	}
	s := a.Snapshot()
	s.Scope[0] = "mutated"
	if calls.Load() != 0 || a.Snapshot().State != "ready" || a.Snapshot().Scope[0] == "mutated" {
		t.Fatal("load/read connected or leaked mutable scope")
	}
	if !strings.Contains(strings.Join(s.Scope, " "), "Git 2.51.0") {
		t.Fatal("scope lacks frozen display name/version")
	}
}

func TestRejectsInvalidPairingAndAllowsRecovery(t *testing.T) {
	b := pairingFixture(t)
	valid, _ := json.Marshal(b)
	expired := b
	expired.ExpiresAt = time.Now().Add(-time.Second)
	badDigest := b
	badDigest.CatalogDigest = "bad"
	for name, path := range map[string]string{"malformed": writeFixture(t, "{"), "trailing": writeFixture(t, string(valid)+" {}"), "unknown": writeFixture(t, strings.TrimSuffix(string(valid), "}")+`,"unexpected":true}`), "expired": writePairing(t, expired), "catalog": writePairing(t, badDigest), "oversize": writeFixture(t, string(valid)+strings.Repeat(" ", protocol.MaxBodyBytes))} {
		t.Run(name, func(t *testing.T) {
			a := New(context.Background(), Options{PairingPath: path})
			if a.Snapshot().State != "error" || a.Snapshot().Error == "" {
				t.Fatal("invalid input not reported")
			}
			if err := a.LoadPairing(writePairing(t, b)); err != nil || a.Snapshot().State != "ready" {
				t.Fatalf("recovery failed: %v", err)
			}
		})
	}
}

func TestDisconnectDefeatsLateConnectAndClearsKey(t *testing.T) {
	b := pairingFixture(t)
	c := newConnection(b)
	started := make(chan struct{})
	release := make(chan struct{})
	var seen []byte
	a := New(context.Background(), Options{Connect: func(ctx context.Context, got protocol.PairingBundle, key []byte) (Connection, error) {
		seen = key
		if got.PairingToken != b.PairingToken {
			t.Error("pairing not passed")
		}
		close(started)
		<-release
		return c, nil
	}})
	if err := a.LoadPairing(writePairing(t, b)); err != nil {
		t.Fatal(err)
	}
	key := []byte("tskey-auth-private")
	if err := a.ConsentAndConnect(key); err != nil {
		t.Fatal(err)
	}
	if strings.Trim(string(key), "\x00") != "" {
		t.Fatal("input key not cleared")
	}
	<-started
	if err := a.ConsentAndConnect([]byte("tskey-auth-second")); err == nil {
		t.Fatal("repeat connect accepted")
	}
	a.Disconnect("arbitrary tskey-auth-private")
	if a.Snapshot().State != "disconnected" {
		t.Fatal("disconnect not immediate")
	}
	// A fresh pairing may be loaded while the cancelled connector is still returning.
	if err := a.LoadPairing(writePairing(t, b)); err != nil {
		t.Fatal(err)
	}
	close(release)
	a.Wait()
	if a.Snapshot().State != "ready" || !c.waited.Load() || c.EndReason() != "local_closed" || strings.Trim(string(seen), "\x00") != "" {
		t.Fatal("late completion resurrected connection or leaked resources/key")
	}
}

func TestFailedConnectionCleanupAndSecretsNeverReachSnapshot(t *testing.T) {
	b := pairingFixture(t)
	b.ControllerName = "\x1b[31m" + b.PairingToken + " tskey-auth-display"
	c := newConnection(b)
	a := New(context.Background(), Options{Connect: func(context.Context, protocol.PairingBundle, []byte) (Connection, error) {
		return c, errors.New(b.PairingToken + " tskey-auth-private token=session-secret")
	}})
	if err := a.LoadPairing(writePairing(t, b)); err != nil {
		t.Fatal(err)
	}
	if err := a.ConsentAndConnect([]byte("tskey-auth-private")); err != nil {
		t.Fatal(err)
	}
	a.Wait()
	s := a.Snapshot()
	raw, _ := json.Marshal(s)
	for _, secret := range []string{b.PairingToken, "tskey-auth-display", "tskey-auth-private", "session-secret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("snapshot exposes %q", secret)
		}
	}
	if s.State != "error" || !c.waited.Load() || !strings.Contains(s.Error, "重新") {
		t.Fatal("failure cleanup/actionable message absent")
	}
	if err := a.ConsentAndConnect([]byte("tskey-auth-retry")); err == nil {
		t.Fatal("consumed pairing retried")
	}
	if err := a.LoadPairing(writePairing(t, pairingFixture(t))); err != nil {
		t.Fatal(err)
	}
}

func TestOnlineSnapshotDetachedAndEndReasonObservable(t *testing.T) {
	b := pairingFixture(t)
	c := newConnection(b)
	c.tasks = []worker.LocalTaskView{{PackageName: "Git", Task: protocol.Task{ID: "task_test", Spec: protocol.TaskSpec{Type: protocol.InstallPackage, Package: &protocol.PackageRef{ID: "git", Version: "2.51.0"}}, Result: &protocol.TaskResult{Info: &protocol.SystemInfo{Software: map[string]string{"Git": "2.51.0"}}}}, Logs: []protocol.LogEntry{{Message: "token=private\x1b\n"}}}}
	a := New(context.Background(), Options{Connect: func(context.Context, protocol.PairingBundle, []byte) (Connection, error) { return c, nil }})
	if err := a.LoadPairing(writePairing(t, b)); err != nil {
		t.Fatal(err)
	}
	if err := a.ConsentAndConnect([]byte("tskey-auth-private")); err != nil {
		t.Fatal(err)
	}
	s := awaitState(t, a, "online")
	s.Tasks[0].Task.Spec.Package.Version = "changed"
	s.Tasks[0].Task.Result.Info.Software["Git"] = "changed"
	s.Tasks[0].Logs[0].Message = "changed"
	fresh := a.Snapshot()
	if fresh.Tasks[0].Task.Spec.Package.Version != "2.51.0" || fresh.Tasks[0].Task.Result.Info.Software["Git"] != "2.51.0" || strings.Contains(fresh.Tasks[0].Logs[0].Message, "private") {
		t.Fatal("snapshot not detached/sanitized")
	}
	if err := a.LoadPairing(writePairing(t, b)); err == nil {
		t.Fatal("active pairing silently replaced")
	}
	if err := a.CancelTask("task_test"); err != nil {
		t.Fatal(err)
	}
	if c.cancelled != "task_test" {
		t.Fatal("cancel not forwarded")
	}
	c.Disconnect("expired")
	a.Wait()
	s = a.Snapshot()
	if s.State != "disconnected" || s.EndReason != "expired" || len(s.Tasks) != 1 || !strings.Contains(s.Notice, "到期") {
		t.Fatal(s)
	}
	if err := a.CancelTask("task_test"); err == nil {
		t.Fatal("cancel after closure allowed")
	}
	a.Disconnect("local_closed")
	if a.Snapshot().EndReason != "expired" {
		t.Fatal("end reason overwritten")
	}
}

func TestParentCancellationAndInvalidKey(t *testing.T) {
	b := pairingFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	a := New(ctx, Options{Connect: func(ctx context.Context, _ protocol.PairingBundle, _ []byte) (Connection, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	if err := a.LoadPairing(writePairing(t, b)); err != nil {
		t.Fatal(err)
	}
	key := []byte("invalid secret")
	if err := a.ConsentAndConnect(key); err == nil || strings.Trim(string(key), "\x00") != "" {
		t.Fatal("invalid key accepted/retained")
	}
	if err := a.ConsentAndConnect([]byte("tskey-auth-valid")); err != nil {
		t.Fatal(err)
	}
	<-started
	cancel()
	a.Wait()
	if a.Snapshot().State != "disconnected" {
		t.Fatal("parent cancellation not observed")
	}
}

type slowCleanupConnection struct {
	*testConnection
	release chan struct{}
}

func (c *slowCleanupConnection) Wait() { <-c.release; c.testConnection.Wait() }

func TestSessionEndVisibleBeforeSlowCleanupFinishes(t *testing.T) {
	b := pairingFixture(t)
	c := &slowCleanupConnection{testConnection: newConnection(b), release: make(chan struct{})}
	a := New(context.Background(), Options{Connect: func(context.Context, protocol.PairingBundle, []byte) (Connection, error) { return c, nil }})
	defer func() { close(c.release); a.Disconnect("local_closed"); a.Wait() }()
	if err := a.LoadPairing(writePairing(t, b)); err != nil {
		t.Fatal(err)
	}
	if err := a.ConsentAndConnect([]byte("tskey-auth-valid")); err != nil {
		t.Fatal(err)
	}
	awaitState(t, a, "online")
	c.Disconnect("heartbeat_lost")
	s := awaitState(t, a, "disconnected")
	if s.EndReason != "heartbeat_lost" || !strings.Contains(s.Notice, "失去联系") {
		t.Fatal(s)
	}
}

func TestConnectionFailureUsesActionableClassifiedError(t *testing.T) {
	b := pairingFixture(t)
	a := New(context.Background(), Options{Connect: func(context.Context, protocol.PairingBundle, []byte) (Connection, error) { return nil, errPlatform }})
	if err := a.LoadPairing(writePairing(t, b)); err != nil {
		t.Fatal(err)
	}
	if err := a.ConsentAndConnect([]byte("tskey-auth-valid")); err != nil {
		t.Fatal(err)
	}
	a.Wait()
	if !strings.Contains(a.Snapshot().Error, "Windows 10/11") {
		t.Fatal(a.Snapshot())
	}
}

type slowCallbackConnection struct {
	*testConnection
	callback string
	enabled  atomic.Bool
	entered  chan struct{}
	release  chan struct{}
}

func (c *slowCallbackConnection) block(name string) {
	if c.callback == name && c.enabled.Load() {
		close(c.entered)
		<-c.release
	}
}
func (c *slowCallbackConnection) Session() protocol.Session {
	c.block("session")
	return c.testConnection.Session()
}
func (c *slowCallbackConnection) Snapshot() []worker.LocalTaskView {
	c.block("snapshot")
	return c.testConnection.Snapshot()
}
func (c *slowCallbackConnection) CancelTask(id string) error {
	c.block("cancel")
	return c.testConnection.CancelTask(id)
}

func TestSlowConnectionCallbacksDoNotBlockDisconnect(t *testing.T) {
	for _, callback := range []string{"session", "snapshot", "cancel"} {
		t.Run(callback, func(t *testing.T) {
			b := pairingFixture(t)
			c := &slowCallbackConnection{testConnection: newConnection(b), callback: callback, entered: make(chan struct{}), release: make(chan struct{})}
			c.enabled.Store(callback == "session")
			a := New(context.Background(), Options{Connect: func(context.Context, protocol.PairingBundle, []byte) (Connection, error) { return c, nil }})
			defer func() { close(c.release); a.Disconnect("local_closed"); a.Wait() }()
			if err := a.LoadPairing(writePairing(t, b)); err != nil {
				t.Fatal(err)
			}
			if err := a.ConsentAndConnect([]byte("tskey-auth-valid")); err != nil {
				t.Fatal(err)
			}
			callbackDone := make(chan struct{})
			switch callback {
			case "snapshot":
				awaitState(t, a, "online")
				c.enabled.Store(true)
				go func() { a.Snapshot(); close(callbackDone) }()
			case "cancel":
				awaitState(t, a, "online")
				c.enabled.Store(true)
				go func() { _ = a.CancelTask("task_test"); close(callbackDone) }()
			default:
				close(callbackDone)
			}
			<-c.entered
			disconnected := make(chan struct{})
			go func() { a.Disconnect("local_closed"); close(disconnected) }()
			select {
			case <-disconnected:
			case <-time.After(time.Second):
				t.Fatal("slow callback blocked emergency disconnect")
			}
		})
	}
}

func TestCatalogueTaskNameKeepsLiteralPairingCredentialRedacted(t *testing.T) {
	b := pairingFixture(t)
	b.Catalog[0].Name = "软件 " + b.PairingToken
	b.CatalogDigest = protocol.Digest(b.Catalog)
	if err := worker.ValidatePairing(b); err != nil {
		t.Fatal(err)
	}
	c := newConnection(b)
	c.tasks = []worker.LocalTaskView{{PackageName: b.Catalog[0].Name, Task: protocol.Task{ID: "task_catalogue", Spec: protocol.TaskSpec{Type: protocol.InstallPackage, Package: &protocol.PackageRef{ID: "git", Version: "2.51.0"}}}}}
	a := New(context.Background(), Options{Connect: func(context.Context, protocol.PairingBundle, []byte) (Connection, error) { return c, nil }})
	defer func() { a.Disconnect("local_closed"); a.Wait() }()
	if err := a.LoadPairing(writePairing(t, b)); err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(a.Snapshot()); strings.Contains(string(raw), b.PairingToken) {
		t.Fatal("ready consent exposes pairing credential")
	}
	if err := a.ConsentAndConnect([]byte("tskey-auth-transient")); err != nil {
		t.Fatal(err)
	}
	s := awaitState(t, a, "online")
	if strings.Contains(s.Tasks[0].PackageName, b.PairingToken) || strings.Contains(TaskTitle(s.Tasks[0]), b.PairingToken) {
		t.Fatal("task name exposes opaque pairing credential after consent")
	}
	a.Disconnect("local_closed")
	a.Wait()
	if raw, _ := json.Marshal(a.Snapshot()); strings.Contains(string(raw), b.PairingToken) {
		t.Fatal("final task view exposes pairing credential")
	}
	if c.tasks[0].PackageName != b.Catalog[0].Name {
		t.Fatal("sanitization modified source catalogue/task data")
	}
}

func TestNestedSystemInformationSanitizedWithoutChangingExecutionIDs(t *testing.T) {
	b := pairingFixture(t)
	c := newConnection(b)
	info := &protocol.SystemInfo{OS: "Windows\x1b", Hostname: "token=host-private\n", CPU: "tskey-auth-cpu-private", MemoryBytes: 1024, Disks: []protocol.DiskInfo{{Name: "disk\u202e password=disk-private", TotalBytes: 100, FreeBytes: 50}}, GPUs: []protocol.GPUInfo{{Name: "GPU " + b.PairingToken, Driver: "secret=driver-private\r"}}, Software: map[string]string{"api_key=software-private\t": "tskey-auth-version-private", "Git\n": "2.51.0\u202e", b.PairingToken: "version " + b.PairingToken}, Elevated: false}
	c.tasks = []worker.LocalTaskView{{PackageName: "Git", Task: protocol.Task{ID: "task_raw\x1b", Spec: protocol.TaskSpec{SessionID: "sess_raw\n", DeviceID: "dev_raw\t", Type: protocol.InspectSystem, Package: &protocol.PackageRef{ID: "package_raw\x1b", Version: "2.51.0"}}, Result: &protocol.TaskResult{Verified: true, RebootRequired: true, Version: "token=result-private", Info: info}, ErrorMessage: "failure " + b.PairingToken}, Logs: []protocol.LogEntry{{Message: "log " + b.PairingToken}}}}
	a := New(context.Background(), Options{Connect: func(context.Context, protocol.PairingBundle, []byte) (Connection, error) { return c, nil }})
	defer func() { a.Disconnect("local_closed"); a.Wait() }()
	if err := a.LoadPairing(writePairing(t, b)); err != nil {
		t.Fatal(err)
	}
	if err := a.ConsentAndConnect([]byte("tskey-auth-transient")); err != nil {
		t.Fatal(err)
	}
	s := awaitState(t, a, "online")
	v := s.Tasks[0]
	got := v.Task.Result.Info
	texts := []string{got.OS, got.Hostname, got.CPU, got.Disks[0].Name, got.GPUs[0].Name, got.GPUs[0].Driver, v.Task.Result.Version, v.Task.ErrorMessage, v.Logs[0].Message}
	for key, value := range got.Software {
		texts = append(texts, key, value)
	}
	for _, text := range texts {
		for _, secret := range []string{b.PairingToken, "host-private", "cpu-private", "disk-private", "driver-private", "software-private", "version-private", "result-private"} {
			if strings.Contains(text, secret) {
				t.Fatalf("result text %q exposes %q", text, secret)
			}
		}
		for _, r := range text {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				t.Fatalf("result text %q retains control character", text)
			}
		}
	}
	if got.Software["Git"] != "2.51.0" || got.OS != "Windows" || got.MemoryBytes != 1024 || got.Disks[0].TotalBytes != 100 || !v.Task.Result.Verified || !v.Task.Result.RebootRequired {
		t.Fatal("sanitization removed harmless result data")
	}
	if v.Task.ID != c.tasks[0].Task.ID || v.Task.Spec.SessionID != c.tasks[0].Task.Spec.SessionID || v.Task.Spec.DeviceID != c.tasks[0].Task.Spec.DeviceID || v.Task.Spec.Package.ID != c.tasks[0].Task.Spec.Package.ID {
		t.Fatal("sanitization changed execution identifiers")
	}
	got.Software["Git"] = "changed"
	got.Disks[0].Name = "changed"
	if info.Software["Git\n"] != "2.51.0\u202e" || info.Disks[0].Name == "changed" {
		t.Fatal("snapshot mutated source inspection data")
	}
}
