package tui

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"remoteops/internal/i18n"
	"remoteops/internal/worker/client"
	"remoteops/pkg/protocol"
)

func TestTUILanguageSaveDoesNotBlockEmergencyKey(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	app := client.New(context.Background(), client.Options{SaveLanguage: func(i18n.Language) error { once.Do(func() { close(entered) }); <-release; return nil }})
	m := newModel(app)
	m.screen = consentScreen
	m.snapshot.State = "ready"
	m.consented = true
	m.input = []byte("pending-secret")
	backing := m.input
	key(m, tea.KeyMsg{Type: tea.KeyF2})
	returned := make(chan struct{})
	finished := make(chan tea.Msg, 1)
	go func() {
		cmd := key(m, runeKey("2"))
		close(returned)
		if cmd != nil {
			finished <- cmd()
		} else {
			finished <- nil
		}
	}()
	defer func() { close(release); <-finished; app.Disconnect("local_closed"); app.Wait() }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("save callback was not reached")
	}
	select {
	case <-returned:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("language choice blocked Update during persistence")
	}
	if m.consented || len(m.input) != 0 {
		t.Fatal("pending authorization was not reset before save finished")
	}
	for _, b := range backing {
		if b != 0 {
			t.Fatal("pending secret buffer was not cleared")
		}
	}
	quit := key(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if quit == nil {
		t.Fatal("Ctrl+C did not request quit during save")
	}
	if _, ok := quit().(tea.QuitMsg); !ok || app.Snapshot().State != "disconnected" {
		t.Fatal("Ctrl+C did not disconnect/quit before save release")
	}
}

func TestTUILanguageCompletionCannotUndoNewerChoice(t *testing.T) {
	var saved []i18n.Language
	app := client.New(context.Background(), client.Options{SaveLanguage: func(language i18n.Language) error { saved = append(saved, language); return nil }})
	m := newModel(app)
	key(m, tea.KeyMsg{Type: tea.KeyF2})
	oldCmd := key(m, runeKey("2"))
	oldCompletion := oldCmd()
	key(m, tea.KeyMsg{Type: tea.KeyF2})
	newCmd := key(m, runeKey("1"))
	m.Update(oldCompletion)
	if m.snapshot.Language != i18n.Chinese || !m.languagePending {
		t.Fatal("older completion undid the pending newer choice")
	}
	apply(m, newCmd)
	if m.snapshot.Language != i18n.Chinese || m.languagePending || app.Language() != i18n.Chinese {
		t.Fatal("latest completion did not apply")
	}
	key(m, tea.KeyMsg{Type: tea.KeyF2})
	obsoleteCmd := key(m, runeKey("2"))
	key(m, tea.KeyMsg{Type: tea.KeyF2})
	latestCmd := key(m, runeKey("1"))
	apply(m, latestCmd)
	apply(m, obsoleteCmd)
	if len(saved) != 3 || saved[2] != i18n.Chinese || !strings.Contains(m.View(), "选择配对文件") {
		t.Fatal("out-of-order commands persisted an obsolete locale")
	}
}

func TestTUILanguageCompletionAfterPairingLoadPreservesScopeAndConsentGate(t *testing.T) {
	var connects atomic.Int32
	app, path := fixtureApp(t, func(context.Context, protocol.PairingBundle, []byte) (client.Connection, error) {
		connects.Add(1)
		return nil, context.Canceled
	})
	m := newModel(app)
	key(m, tea.KeyMsg{Type: tea.KeyF2})
	held := key(m, runeKey("2"))()
	load(t, m, path)
	assertScope := func() {
		t.Helper()
		scope := strings.Join(m.snapshot.Scope, "\n")
		for _, want := range []string{
			"Inspect the system and authorized software versions",
			"Install authorized software (fixed versions)",
			"Git 2.51.0 (installation requires administrator privileges)",
			"Administrator privileges may be requested; confirm each elevation in the Windows permission dialog",
		} {
			if !strings.Contains(scope, want) {
				t.Fatalf("current pairing authorization missing %q: %s", want, scope)
			}
		}
	}
	assertScope()
	_, currentScope := m.Update(held)
	assertScope()
	key(m, tea.KeyMsg{Type: tea.KeyEnd})
	key(m, runeKey("y"))
	if m.screen != consentScreen || m.consented {
		t.Fatal("stale language completion enabled consent before current scope collection")
	}
	if currentScope == nil {
		t.Fatal("stale pairing completion did not request current localized authorization")
	}
	apply(m, currentScope)
	assertScope()
	if m.languagePending || m.scroll != 0 {
		t.Fatal("current authorization did not clear its gate and restart review")
	}
	key(m, tea.KeyMsg{Type: tea.KeyEnd})
	key(m, runeKey("y"))
	if m.screen != keyScreen || !m.consented || connects.Load() != 0 {
		t.Fatal("fresh authorization did not allow explicit consent without connecting")
	}
}

func TestTUIRefreshBeforePairingLoadCannotEraseAuthorization(t *testing.T) {
	app, path := fixtureApp(t, nil)
	m := newModel(app)
	held := m.refresh()()
	load(t, m, path)
	want := strings.Join(m.snapshot.Scope, "\n")
	m.Update(held)
	if want == "" || strings.Join(m.snapshot.Scope, "\n") != want || m.screen != consentScreen {
		t.Fatal("idle refresh replaced newly loaded authorization")
	}
}

func TestTUILanguageCompletionDuringPairingLoadCannotEnableConsent(t *testing.T) {
	app, path := fixtureApp(t, nil)
	m := newModel(app)
	key(m, runeKey(path))
	loadCmd := key(m, tea.KeyMsg{Type: tea.KeyEnter})
	key(m, tea.KeyMsg{Type: tea.KeyF2})
	apply(m, key(m, runeKey("2")))
	if !m.languagePending || m.screen != loadingScreen {
		t.Fatal("language completion trusted an authorization snapshot before pairing load")
	}
	_, localized := m.Update(loadCmd())
	if localized == nil || !m.languagePending {
		t.Fatal("pairing completion did not retain gate for current localized scope")
	}
	apply(m, localized)
	if m.languagePending || m.screen != consentScreen || len(m.snapshot.Scope) == 0 {
		t.Fatal("current localized pairing scope did not finish consent review")
	}
}

func TestTUILanguageChoiceAfterCapturedLoadPreservesCurrentPairingMetadata(t *testing.T) {
	app, path := fixtureApp(t, nil)
	m := newModel(app)
	key(m, runeKey(path))
	held := key(m, tea.KeyMsg{Type: tea.KeyEnter})()
	key(m, tea.KeyMsg{Type: tea.KeyF2})
	apply(m, key(m, runeKey("2")))
	_, currentScope := m.Update(held)
	if m.snapshot.Language != i18n.English || m.snapshot.Controller != "测试控制方" || !m.languagePending {
		t.Fatal("loaded metadata or selected language was lost while collecting authorization")
	}
	key(m, tea.KeyMsg{Type: tea.KeyEnd})
	key(m, runeKey("y"))
	if m.screen != consentScreen || m.consented {
		t.Fatal("wrong-locale loaded scope enabled consent")
	}
	apply(m, currentScope)
	if m.languagePending || m.snapshot.State != "ready" || m.snapshot.Controller != "测试控制方" || !strings.Contains(strings.Join(m.snapshot.Scope, "\n"), "Git 2.51.0 (installation requires administrator privileges)") {
		t.Fatal("current localized scope did not preserve loaded pairing metadata")
	}
}

func TestTUIOnlineBlockedSaveAllowsDisconnectAndIgnoresLateCompletion(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	_, path := fixtureApp(t, nil)
	conn := &testConn{done: make(chan struct{})}
	app := client.New(context.Background(), client.Options{PairingPath: path, Connect: func(context.Context, protocol.PairingBundle, []byte) (client.Connection, error) { return conn, nil }, SaveLanguage: func(i18n.Language) error { close(entered); <-release; return nil }})
	if err := app.ConsentAndConnect([]byte("tskey-auth-inert")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for app.Snapshot().State != "online" {
		if time.Now().After(deadline) {
			t.Fatal("inert connection not online")
		}
		time.Sleep(time.Millisecond)
	}
	m := newModel(app)
	m.Update(snapshotMsg(app.Snapshot()))
	key(m, tea.KeyMsg{Type: tea.KeyF2})
	cmd := key(m, runeKey("2"))
	finished := make(chan tea.Msg, 1)
	go func() { finished <- cmd() }()
	defer func() { app.Disconnect("local_closed"); app.Wait() }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		close(release)
		<-finished
		t.Fatal("save was not reached")
	}
	quit := key(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	select {
	case <-conn.done:
	default:
		close(release)
		<-finished
		t.Fatal("Ctrl+C did not disconnect transport before save release")
	}
	if quit == nil {
		close(release)
		<-finished
		t.Fatal("Ctrl+C did not return quit")
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		close(release)
		<-finished
		t.Fatal("Ctrl+C returned a non-quit command")
	}
	m.snapshot.Scope = []string{"visible after shutdown"}
	close(release)
	m.Update(<-finished)
	if len(m.snapshot.Scope) != 1 || m.snapshot.Scope[0] != "visible after shutdown" {
		t.Fatal("late language completion changed shutdown UI")
	}
}

func TestTUILanguageSnapshotDoesNotBlockEmergencyKey(t *testing.T) {
	conn := &blockedSnapshotConn{testConn: &testConn{done: make(chan struct{})}, entered: make(chan struct{}), release: make(chan struct{})}
	app, path := fixtureApp(t, func(context.Context, protocol.PairingBundle, []byte) (client.Connection, error) { return conn, nil })
	if err := app.LoadPairing(path); err != nil {
		t.Fatal(err)
	}
	if err := app.ConsentAndConnect([]byte("tskey-auth-inert")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for app.CancelTask("online-probe") != nil {
		if time.Now().After(deadline) {
			t.Fatal("inert connection not online")
		}
		time.Sleep(time.Millisecond)
	}
	m := newModel(app)
	m.screen = dashboardScreen
	m.snapshot.State = "online"
	key(m, tea.KeyMsg{Type: tea.KeyF2})
	cmd := key(m, runeKey("2"))
	finished := make(chan tea.Msg, 1)
	go func() { finished <- cmd() }()
	defer func() { close(conn.release); <-finished }()
	select {
	case <-conn.entered:
	case <-time.After(time.Second):
		t.Fatal("language snapshot did not start")
	}
	quit := key(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if quit == nil {
		t.Fatal("snapshot wait blocked quit")
	}
	select {
	case <-conn.done:
	default:
		t.Fatal("snapshot wait blocked transport disconnect")
	}
}

func TestPlainCanceledLanguageSaveReturnsWithoutFurtherReads(t *testing.T) {
	for _, screen := range []string{"path", "consent", "connected"} {
		t.Run(screen, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			var passwordReads, commandReads, promptReads atomic.Int32
			conn := &testConn{done: make(chan struct{})}
			_, path := fixtureApp(t, nil)
			app := client.New(ctx, client.Options{SaveLanguage: func(i18n.Language) error { once.Do(func() { close(entered) }); <-release; return nil }, Connect: func(context.Context, protocol.PairingBundle, []byte) (client.Connection, error) { return conn, nil }})
			if screen != "path" {
				if err := app.LoadPairing(path); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			result := make(chan error, 1)
			go func() {
				result <- runPlainSession(ctx, app, &out, func() (string, error) {
					if ctx.Err() != nil {
						return "", ctx.Err()
					}
					promptReads.Add(1)
					if screen == "connected" {
						return "同意", nil
					}
					return "lang en-US", nil
				}, func() ([]byte, error) { passwordReads.Add(1); return []byte("tskey-auth-inert"), nil }, func() <-chan lineResult {
					commandReads.Add(1)
					line := make(chan lineResult, 1)
					line <- lineResult{data: []byte("lang en-US")}
					return line
				})
			}()
			returned := false
			defer func() {
				close(release)
				if !returned {
					<-result
				}
				app.Disconnect("local_closed")
				app.Wait()
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("save callback was not reached")
			}
			cancel()
			select {
			case err := <-result:
				returned = true
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(100 * time.Millisecond):
				t.Fatal("canceled plain prompt remained blocked on language save")
			}
			if promptReads.Load() != 1 {
				t.Fatal("canceled locale selection read another prompt")
			}
			if screen == "connected" {
				if commandReads.Load() != 1 || passwordReads.Load() != 1 {
					t.Fatal("canceled connected selection performed additional reads")
				}
			} else if commandReads.Load() != 0 || passwordReads.Load() != 0 {
				t.Fatal("canceled preconnection locale selection reached key/command input")
			}
		})
	}
}
