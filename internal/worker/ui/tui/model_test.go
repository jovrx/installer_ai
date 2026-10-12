package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"remoteops/internal/worker"
	"remoteops/internal/worker/client"
	"remoteops/internal/worker/events"
	"remoteops/pkg/protocol"
)

func fixtureApp(t *testing.T, connect client.Connector) (*client.App, string) {
	t.Helper()
	b := protocol.PairingBundle{ProtocolVersion: protocol.Version, ControllerName: "测试控制方", ControllerURL: "http://controller:9080", SessionID: "sess_test", PairingToken: "opaque-private-pairing", ExpiresAt: time.Now().Add(time.Hour), AuthorizationSeconds: 3600, Scope: protocol.Scope{AllowedTasks: []protocol.TaskType{protocol.InspectSystem, protocol.InstallPackage}, AllowedPackages: []string{"git"}, AllowAdmin: true}, Catalog: []protocol.CatalogEntry{{ID: "git", Name: "Git", Version: "2.51.0", Installer: "winget", WingetID: "Git.Git", Source: "winget", RequiresAdmin: true}}}
	b.CatalogDigest = protocol.Digest(b.Catalog)
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "pairing.json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	a := client.New(context.Background(), client.Options{Connect: connect})
	t.Cleanup(func() { a.Disconnect("local_closed"); a.Wait() })
	return a, path
}
func key(m *model, k tea.KeyMsg) tea.Cmd { _, cmd := m.Update(k); return cmd }
func runeKey(s string) tea.KeyMsg        { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }
func apply(m *model, cmd tea.Cmd) {
	if cmd != nil {
		m.Update(cmd())
	}
}
func load(t *testing.T, m *model, path string) {
	t.Helper()
	key(m, runeKey(path))
	apply(m, key(m, tea.KeyMsg{Type: tea.KeyEnter}))
}

func TestFileEnterShowsScopeWithoutConsent(t *testing.T) {
	var calls atomic.Int32
	a, path := fixtureApp(t, func(context.Context, protocol.PairingBundle, []byte) (client.Connection, error) {
		calls.Add(1)
		return nil, fmt.Errorf("offline")
	})
	m := newModel(a)
	load(t, m, path)
	view := m.View()
	for _, want := range []string{"测试控制方", "Git 2.51.0", "管理员", "同意"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q in %s", want, view)
		}
	}
	key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if calls.Load() != 0 || strings.Contains(m.View(), "Auth Key") {
		t.Fatal("file/consent Enter silently accepted")
	}
}

func TestPreloadedInvalidPairingDisplaysRecoveryError(t *testing.T) {
	a, _ := fixtureApp(t, nil)
	if err := a.LoadPairing(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("expected missing file")
	}
	m := newModel(a)
	m.Update(snapshotMsg(a.Snapshot()))
	if !strings.Contains(m.View(), "无法读取配对文件") || !strings.Contains(m.View(), "路径") {
		t.Fatal("preloaded invalid pairing error hidden")
	}
}

func TestPasswordMaskedAndPastedShortcutsNeverExecute(t *testing.T) {
	var calls atomic.Int32
	a, path := fixtureApp(t, func(context.Context, protocol.PairingBundle, []byte) (client.Connection, error) {
		calls.Add(1)
		return nil, fmt.Errorf("offline")
	})
	m := newModel(a)
	load(t, m, path)
	key(m, runeKey("y"))
	key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("tskey-auth-SUPERPRIVATEcdl?"), Paste: true})
	if strings.Contains(m.View(), "SUPERPRIVATE") || strings.Contains(m.View(), "tskey-auth-") || !strings.Contains(m.View(), "••••") {
		t.Fatal("password exposed or unmasked")
	}
	if calls.Load() != 0 || a.Snapshot().State != "ready" {
		t.Fatal("paste executed shortcut")
	}
	key(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.input) != 0 || strings.Contains(m.View(), "SUPERPRIVATE") {
		t.Fatal("submitted key retained")
	}
}

func TestTextEntryIgnoresDashboardShortcutsAndClearsBackingBuffer(t *testing.T) {
	a, path := fixtureApp(t, nil)
	m := newModel(a)
	for _, s := range []string{"c", "d", "l", "?", "h"} {
		key(m, runeKey(s))
	}
	if m.screen != pathScreen || a.Snapshot().State != "idle" {
		t.Fatal("text entry executed a shortcut")
	}
	key(m, tea.KeyMsg{Type: tea.KeyCtrlU})
	load(t, m, path)
	key(m, runeKey("y"))
	key(m, runeKey("tskey-auth-private"))
	backing := m.input
	key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if strings.Trim(string(backing), "\x00") != "" || strings.Contains(m.View(), "private") {
		t.Fatal("escaped credential buffer retained")
	}
}

func TestPastedConsentAndDisconnectCannotAuthorize(t *testing.T) {
	a, path := fixtureApp(t, nil)
	m := newModel(a)
	load(t, m, path)
	key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y"), Paste: true})
	if m.screen != consentScreen || a.Snapshot().State != "ready" {
		t.Fatal("pasted consent accepted")
	}
}

func TestSnapshotRefreshPreservesRejectedConsentAndRePairInput(t *testing.T) {
	a, path := fixtureApp(t, nil)
	m := newModel(a)
	load(t, m, path)
	key(m, runeKey("n"))
	m.Update(snapshotMsg(a.Snapshot()))
	if m.screen != pathScreen {
		t.Fatal("snapshot reopened rejected consent")
	}
	a.Disconnect("local_closed")
	m.screen = dashboardScreen
	m.Update(snapshotMsg(a.Snapshot()))
	key(m, runeKey("r"))
	key(m, runeKey("new-path"))
	m.Update(snapshotMsg(a.Snapshot()))
	if m.screen != pathScreen || !strings.Contains(m.View(), "new-path") {
		t.Fatalf("snapshot interrupted re-pair input: screen=%v input=%q view=%s", m.screen, m.input, m.View())
	}
}

func TestCtrlCImmediatelyDisconnectsEveryStep(t *testing.T) {
	for _, step := range []screen{pathScreen, loadingScreen, consentScreen, keyScreen, dashboardScreen} {
		t.Run(fmt.Sprint(step), func(t *testing.T) {
			a, _ := fixtureApp(t, nil)
			m := newModel(a)
			m.screen = step
			m.input = []byte("private")
			key(m, tea.KeyMsg{Type: tea.KeyCtrlC})
			if a.Snapshot().State != "disconnected" || len(m.input) != 0 {
				t.Fatal("Ctrl+C did not synchronously clean up")
			}
		})
	}
}

func TestCtrlCDuringConnectingCancelsBlockedConnector(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	a, path := fixtureApp(t, func(ctx context.Context, _ protocol.PairingBundle, _ []byte) (client.Connection, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	})
	m := newModel(a)
	load(t, m, path)
	key(m, runeKey("y"))
	key(m, runeKey("tskey-auth-private"))
	key(m, tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("did not connect")
	}
	key(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("connection not cancelled")
	}
}

type testConn struct {
	mu     sync.Mutex
	done   chan struct{}
	cancel string
	tasks  []worker.LocalTaskView
}

func (c *testConn) Snapshot() []worker.LocalTaskView {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]worker.LocalTaskView(nil), c.tasks...)
}
func (c *testConn) Session() protocol.Session {
	return protocol.Session{ExpiresAt: time.Now().Add(time.Hour)}
}
func (c *testConn) CancelTask(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancel = id
	return nil
}
func (c *testConn) Disconnect(string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.done:
	default:
		close(c.done)
	}
}
func (c *testConn) Done() <-chan struct{} { return c.done }
func (c *testConn) Wait()                 { <-c.done }
func (c *testConn) EndReason() string     { return "local_closed" }

type blockedSnapshotConn struct {
	*testConn
	entered, release chan struct{}
}

func (c *blockedSnapshotConn) Snapshot() []worker.LocalTaskView {
	close(c.entered)
	<-c.release
	return nil
}

func TestSlowSnapshotDoesNotBlockEmergencyUpdate(t *testing.T) {
	c := &blockedSnapshotConn{testConn: &testConn{done: make(chan struct{})}, entered: make(chan struct{}), release: make(chan struct{})}
	a, path := fixtureApp(t, func(context.Context, protocol.PairingBundle, []byte) (client.Connection, error) { return c, nil })
	if err := a.LoadPairing(path); err != nil {
		t.Fatal(err)
	}
	if err := a.ConsentAndConnect([]byte("tskey-auth-private")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for a.CancelTask("probe") != nil {
		if time.Now().After(deadline) {
			t.Fatal("not online")
		}
		time.Sleep(time.Millisecond)
	}
	m := newModel(a)
	_, cmd := m.Update(tickMsg(time.Now()))
	finished := make(chan struct{})
	go func() { cmd(); close(finished) }()
	defer func() { close(c.release); <-finished }()
	select {
	case <-c.entered:
	case <-time.After(time.Second):
		t.Fatal("snapshot did not start")
	}
	m.Update(snapshotMsg(client.Snapshot{State: "connecting"}))
	first := ansi.Strip(m.View())
	m.Update(activityMsg(m.now.Add(250 * time.Millisecond)))
	second := ansi.Strip(m.View())
	if first == second || !strings.Contains(first, "⠙") || !strings.Contains(second, "⠹") {
		t.Fatal("slow snapshot stopped independent activity frames")
	}
	updated := make(chan struct{})
	go func() { key(m, tea.KeyMsg{Type: tea.KeyCtrlC}); close(updated) }()
	select {
	case <-updated:
	case <-time.After(time.Second):
		t.Fatal("slow snapshot blocked Ctrl+C")
	}
	select {
	case <-c.done:
	default:
		t.Fatal("Ctrl+C did not disconnect during snapshot")
	}
}

func TestDashboardSelectionCancellationHistoryAndSafeLogs(t *testing.T) {
	c := &testConn{done: make(chan struct{})}
	for i := 0; i < 100; i++ {
		c.tasks = append(c.tasks, worker.LocalTaskView{Task: protocol.Task{ID: fmt.Sprintf("task-%d", i), State: protocol.TaskRunning, Spec: protocol.TaskSpec{Type: protocol.InstallPackage, Package: &protocol.PackageRef{Version: "2.51.0"}}}, PackageName: "Git", Progress: events.Progress{Stage: "downloading", CompletedBytes: 2 << 20}, Logs: []protocol.LogEntry{{Message: "私有日志\x1b[31m\r\n"}}})
	}
	a, path := fixtureApp(t, func(context.Context, protocol.PairingBundle, []byte) (client.Connection, error) { return c, nil })
	if err := a.LoadPairing(path); err != nil {
		t.Fatal(err)
	}
	if err := a.ConsentAndConnect([]byte("tskey-auth-private")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for a.Snapshot().State != "online" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	m := newModel(a)
	m.Update(snapshotMsg(a.Snapshot()))
	key(m, tea.KeyMsg{Type: tea.KeyDown})
	apply(m, key(m, runeKey("c")))
	c.mu.Lock()
	id := c.cancel
	c.mu.Unlock()
	if id != "task-1" {
		t.Fatalf("cancelled %q instead of selected task", id)
	}
	if strings.Contains(m.View(), "私有日志") {
		t.Fatal("logs not collapsed")
	}
	key(m, runeKey("l"))
	if !strings.Contains(m.View(), "私有日志") || strings.Contains(m.View(), "\x1b") || strings.Contains(m.View(), "\r") {
		t.Fatal("logs absent or controls leaked")
	}
	if !strings.Contains(m.View(), "已下载 2.0 MB") || strings.Contains(m.View(), "0%") {
		t.Fatal("unknown total pretends percentage")
	}
	key(m, runeKey("h"))
	for i := 0; i < 99; i++ {
		key(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	apply(m, key(m, runeKey("c")))
	c.mu.Lock()
	id = c.cancel
	c.mu.Unlock()
	if id != "task-99" {
		t.Fatalf("expanded history unreachable: %q", id)
	}
	key(m, runeKey("d"))
	if a.Snapshot().State != "online" || !strings.Contains(m.View(), "断开") {
		t.Fatal("disconnect not confirmed")
	}
	key(m, runeKey("y"))
	if a.Snapshot().State != "disconnected" {
		t.Fatal("disconnect confirmation failed")
	}
}

func TestNarrowConsentScrollAndChineseCellWidth(t *testing.T) {
	a, _ := fixtureApp(t, nil)
	m := newModel(a)
	s := client.Snapshot{State: "ready", Controller: strings.Repeat("中文控制方", 15), Authorization: time.Hour}
	for i := 0; i < 35; i++ {
		s.Scope = append(s.Scope, fmt.Sprintf("软件%d %s", i, strings.Repeat("中文", 20)))
	}
	m.Update(snapshotMsg(s))
	for _, size := range []tea.WindowSizeMsg{{Width: 60, Height: 18}, {Width: 100, Height: 30}} {
		m.Update(size)
		m.scroll = 0
		view := m.View()
		lines := strings.Split(view, "\n")
		if len(lines) > size.Height {
			t.Fatalf("%d rows exceeds %d", len(lines), size.Height)
		}
		for _, line := range lines {
			if lipgloss.Width(line) > size.Width {
				t.Fatalf("cell overflow %d: %s", lipgloss.Width(line), line)
			}
		}
		key(m, runeKey("y"))
		if strings.Contains(m.View(), "Auth Key") {
			t.Fatal("accepted unread scope")
		}
		key(m, tea.KeyMsg{Type: tea.KeyEnd})
		if !strings.Contains(m.View(), "软件34") {
			t.Fatal("final scope unreachable")
		}
		key(m, runeKey("y"))
		if !strings.Contains(m.View(), "Auth Key") {
			t.Fatal("cannot consent after scrolling")
		}
		m.screen = consentScreen
	}
}

func TestHelpAppearsWithoutScrollingPastHistory(t *testing.T) {
	a, _ := fixtureApp(t, nil)
	m := newModel(a)
	s := client.Snapshot{State: "online"}
	for i := 0; i < 20; i++ {
		s.Tasks = append(s.Tasks, worker.LocalTaskView{Task: protocol.Task{ID: fmt.Sprint(i), State: protocol.TaskRunning}, PackageName: "软件"})
	}
	m.Update(snapshotMsg(s))
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 18})
	key(m, runeKey("?"))
	if !strings.Contains(m.View(), "帮助：") || !strings.Contains(m.View(), "PgUp") {
		t.Fatal("help hidden below task history")
	}
	key(m, runeKey("?"))
	if !strings.Contains(m.View(), "选中任务") {
		t.Fatal("cannot return to dashboard")
	}
}

func TestPathPasteDoesNotRunCommandsOrSubmitNewline(t *testing.T) {
	a, _ := fixtureApp(t, nil)
	m := newModel(a)
	key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("cdl?\npath"), Paste: true})
	if a.Snapshot().State != "idle" || m.screen != pathScreen || strings.Contains(m.View(), "日志") {
		t.Fatal("path paste ran commands")
	}
	if strings.Contains(string(m.input), "\n") {
		t.Fatal("multiline paste not made single line")
	}
}

func TestPlainRefusesRedirectedConsent(t *testing.T) {
	a, path := fixtureApp(t, nil)
	data := filepath.Join(t.TempDir(), "stdin.txt")
	if err := os.WriteFile(data, []byte(path+"\ny\ntskey-auth-private\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out bytes.Buffer
	if err = runPlain(context.Background(), a, f, &out); err == nil || a.Snapshot().State != "idle" {
		t.Fatal("redirected consent accepted")
	}
	if !strings.Contains(out.String(), "交互终端") {
		t.Fatal("plain refusal lacks useful message")
	}
}

func TestTerminalCapabilitiesAndNoColor(t *testing.T) {
	t.Setenv("TERM", "dumb")
	if !plainTerminal(true, true, false) {
		t.Fatal("dumb terminal uses fullscreen")
	}
	t.Setenv("TERM", "xterm-256color")
	if plainTerminal(true, true, false) || !plainTerminal(false, true, false) || !plainTerminal(true, true, true) {
		t.Fatal("terminal fallback incorrect")
	}
	t.Setenv("NO_COLOR", "")
	a, _ := fixtureApp(t, nil)
	m := newModel(a)
	if strings.Contains(m.View(), "\x1b") {
		t.Fatal("NO_COLOR emitted escapes")
	}
}

func TestPlainPrintsStageChangesAndFinalResults(t *testing.T) {
	s := client.Snapshot{State: "online", Tasks: []worker.LocalTaskView{{Task: protocol.Task{ID: "1", State: protocol.TaskRunning}, Progress: events.Progress{Stage: "awaiting_uac"}}}}
	var out bytes.Buffer
	p := plainReporter{seen: map[string]string{}}
	p.print(&out, s)
	p.print(&out, s)
	if strings.Count(out.String(), "Windows 权限窗口") != 1 {
		t.Fatal("stage missing or duplicated")
	}
	s.Tasks[0].Task.State = protocol.Succeeded
	s.Tasks[0].Task.Result = &protocol.TaskResult{Verified: true, Version: "2.51.0", RebootRequired: true}
	p.print(&out, s)
	if !strings.Contains(out.String(), "版本已验证") || !strings.Contains(out.String(), "手动重启") {
		t.Fatal("final result lacks verification/reboot guidance")
	}
}

func TestPlainTaskNumbersSurviveInsertionReorderingAndEviction(t *testing.T) {
	aTask := worker.LocalTaskView{Task: protocol.Task{ID: "a", State: protocol.TaskRunning}, PackageName: "软件A", Logs: []protocol.LogEntry{{Message: "A专属日志"}}}
	bTask := worker.LocalTaskView{Task: protocol.Task{ID: "b", State: protocol.TaskRunning}, PackageName: "软件B", Logs: []protocol.LogEntry{{Message: "B专属日志"}}}
	cTask := worker.LocalTaskView{Task: protocol.Task{ID: "c", State: protocol.TaskRunning}, PackageName: "软件C"}
	conn := &testConn{done: make(chan struct{}), tasks: []worker.LocalTaskView{aTask, bTask}}
	app, path := fixtureApp(t, func(context.Context, protocol.PairingBundle, []byte) (client.Connection, error) { return conn, nil })
	if err := app.LoadPairing(path); err != nil {
		t.Fatal(err)
	}
	if err := app.ConsentAndConnect([]byte("tskey-auth-fixture")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for app.CancelTask("probe") != nil {
		if time.Now().After(deadline) {
			t.Fatal("not online")
		}
		time.Sleep(time.Millisecond)
	}
	var out bytes.Buffer
	p := plainReporter{}
	p.print(&out, app.Snapshot())
	conn.mu.Lock()
	conn.tasks = []worker.LocalTaskView{cTask, bTask, aTask}
	conn.mu.Unlock()
	latest := app.Snapshot()
	out.Reset()
	p.print(&out, latest)
	if !strings.Contains(out.String(), "任务 3 · 软件C") {
		t.Fatalf("new task reused old number: %s", out.String())
	}
	p.taskCommand(app, &out, latest, []string{"c", "1"})
	conn.mu.Lock()
	cancel := conn.cancel
	conn.mu.Unlock()
	if cancel != "a" {
		t.Fatalf("old number targeted %q instead of a", cancel)
	}
	conn.mu.Lock()
	conn.tasks = []worker.LocalTaskView{aTask, cTask, bTask}
	conn.mu.Unlock()
	latest = app.Snapshot()
	out.Reset()
	p.history(&out, latest, 100)
	for _, want := range []string{"1. 软件A", "3. 软件C", "2. 软件B"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("history changed stable number: %s", out.String())
		}
	}
	out.Reset()
	p.taskCommand(app, &out, latest, []string{"l", "2"})
	if !strings.Contains(out.String(), "B专属日志") || strings.Contains(out.String(), "A专属日志") {
		t.Fatal("log numbering retargeted")
	}
	conn.mu.Lock()
	conn.tasks = []worker.LocalTaskView{cTask, bTask}
	conn.cancel = ""
	conn.mu.Unlock()
	out.Reset()
	p.taskCommand(app, &out, app.Snapshot(), []string{"c", "1"})
	conn.mu.Lock()
	cancel = conn.cancel
	conn.mu.Unlock()
	if cancel != "" || !strings.Contains(out.String(), "不在当前列表") {
		t.Fatal("evicted number targeted replacement or was not rejected")
	}
}

func TestEscapeClosesHelpAndLogsAndReturnsDashboard(t *testing.T) {
	app, _ := fixtureApp(t, nil)
	m := newModel(app)
	m.Update(snapshotMsg(client.Snapshot{State: "online", Tasks: []worker.LocalTaskView{{Task: protocol.Task{ID: "a", State: protocol.TaskRunning}, PackageName: "Git", Logs: []protocol.LogEntry{{Message: "独立日志"}}}}}))
	key(m, runeKey("?"))
	key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if !strings.Contains(m.View(), "选中任务") || strings.Contains(m.View(), "帮助：") {
		t.Fatal("Escape did not dismiss help")
	}
	key(m, runeKey("l"))
	key(m, tea.KeyMsg{Type: tea.KeyEnd})
	key(m, tea.KeyMsg{Type: tea.KeyEsc})
	if strings.Contains(m.View(), "独立日志") || !strings.Contains(m.View(), "日志已折叠") {
		t.Fatal("Escape did not collapse logs/reset scroll")
	}
}

func TestFramedTerminalLayoutsColorAndNoColor(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	os.Unsetenv("NO_COLOR")
	app, _ := fixtureApp(t, nil)
	m := newModel(app)
	// Use a local renderer profile to model a capable terminal, without changing
	// the process-global Lip Gloss renderer used by other packages.
	m.renderer.SetColorProfile(termenv.TrueColor)
	scope := client.Snapshot{State: "ready", Controller: strings.Repeat("中文控制方", 8), Authorization: time.Hour}
	for i := 0; i < 35; i++ {
		scope.Scope = append(scope.Scope, fmt.Sprintf("软件%d %s", i, strings.Repeat("中文", 20)))
	}
	dashboard := client.Snapshot{State: "online", Tasks: []worker.LocalTaskView{{Task: protocol.Task{ID: "a", State: protocol.TaskRunning}, PackageName: "Git", Progress: events.Progress{Stage: "downloading", CompletedBytes: 1 << 20, TotalBytes: 4 << 20}}}}
	for _, state := range []client.Snapshot{{State: "idle"}, scope, dashboard} {
		m.initialized = false
		m.screen = pathScreen
		m.scroll = 0
		m.Update(snapshotMsg(state))
		for _, size := range []tea.WindowSizeMsg{{Width: 60, Height: 18}, {Width: 100, Height: 30}} {
			m.Update(size)
			colored := m.View()
			if !strings.Contains(colored, "\x1b[") || !strings.Contains(ansi.Strip(colored), "╭") || !strings.Contains(ansi.Strip(colored), "╰") {
				t.Fatal("graphical colored frame missing")
			}
			for _, line := range strings.Split(colored, "\n") {
				if lipgloss.Width(line) > size.Width {
					t.Fatal("colored frame overflows")
				}
			}
			if len(strings.Split(colored, "\n")) > size.Height {
				t.Fatal("frame height overflows")
			}
			os.Setenv("NO_COLOR", "")
			plain := m.View()
			if strings.Contains(plain, "\x1b") || !strings.Contains(plain, "╭") || !strings.Contains(plain, "╰") {
				t.Fatal("NO_COLOR loses frame or retains escapes")
			}
			if plain != ansi.Strip(colored) {
				t.Fatal("NO_COLOR changed graphical structure")
			}
			for _, line := range strings.Split(plain, "\n") {
				if lipgloss.Width(line) > size.Width {
					t.Fatal("NO_COLOR frame overflows")
				}
			}
			os.Unsetenv("NO_COLOR")
		}
	}
}

func TestGraphicalDownloadRatioAndTerminalPrecedence(t *testing.T) {
	app, _ := fixtureApp(t, nil)
	m := newModel(app)
	m.Update(snapshotMsg(client.Snapshot{State: "online", Tasks: []worker.LocalTaskView{{Task: protocol.Task{ID: "a", State: protocol.TaskRunning}, PackageName: "Git", Progress: events.Progress{Stage: "downloading", CompletedBytes: 1 << 20, TotalBytes: 4 << 20}}}}))
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "██████░░░░░░░░░░░░░░░░░░") || !strings.Contains(view, "25%") {
		t.Fatal("download lacks actual byte-proportional graphical bar")
	}
	m.snapshot.Tasks[0].Task.State = protocol.Succeeded
	view = ansi.Strip(m.View())
	if strings.Contains(view, "█") || strings.Contains(view, "25%") {
		t.Fatal("terminal result did not override stale graphical progress")
	}
}

func TestTickChangesIndeterminateActivityWithoutInstallPercent(t *testing.T) {
	for _, stage := range []string{"downloading", "installing", "connecting"} {
		t.Run(stage, func(t *testing.T) {
			app, _ := fixtureApp(t, nil)
			m := newModel(app)
			s := client.Snapshot{State: "online"}
			start := m.now.Add(-3 * time.Second)
			if stage == "connecting" {
				s.State = "connecting"
			} else {
				s.Tasks = []worker.LocalTaskView{{Task: protocol.Task{ID: "a", State: protocol.TaskRunning, StartedAt: &start}, PackageName: "Git", Progress: events.Progress{Stage: stage, CompletedBytes: 1 << 20}}}
				if stage == "installing" {
					s.Tasks[0].Progress.TotalBytes = 4 << 20
				}
			}
			m.Update(snapshotMsg(s))
			first := ansi.Strip(m.View())
			m.Update(activityMsg(m.now.Add(250 * time.Millisecond)))
			second := ansi.Strip(m.View())
			if first == second || !strings.Contains(first, "⠋") || !strings.Contains(second, "⠙") {
				t.Fatal("indeterminate activity does not tick")
			}
			if strings.Contains(second, "%") || strings.Contains(second, "█") {
				t.Fatal("indeterminate stage manufactured percentage/bar")
			}
			if stage != "connecting" && !strings.Contains(second, "已用") {
				t.Fatal("elapsed missing")
			}
		})
	}
}
