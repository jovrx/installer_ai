package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"remoteops/internal/i18n"
	"remoteops/internal/worker"
	"remoteops/internal/worker/client"
	"remoteops/internal/worker/events"
	"remoteops/pkg/protocol"
)

func TestEnglishAuthorizationAndDashboard(t *testing.T) {
	app, path := fixtureApp(t, nil)
	if err := app.SetLanguage(i18n.English); err != nil {
		t.Fatal(err)
	}
	m := newModel(app)
	load(t, m, path)
	for _, want := range []string{"Review", "Controller:", "Authorization duration:", "Y", "agree"} {
		if !strings.Contains(m.View(), want) {
			t.Fatalf("missing %q: %s", want, m.View())
		}
	}
	m.screen = dashboardScreen
	m.snapshot = client.Snapshot{State: "online", Language: i18n.English, Warning: "settings warning", Error: "connection error", Tasks: []worker.LocalTaskView{{Task: protocol.Task{ID: "task", State: "running"}, Progress: events.Progress{Stage: "installing"}}}}
	m.ensureSelection()
	for _, want := range []string{"Connected", "Selected task:", "Installing", "Elapsed:", "settings warning", "connection error"} {
		if !strings.Contains(m.View(), want) {
			t.Fatalf("missing %q: %s", want, m.View())
		}
	}
}

func TestLanguageChooserResetsReadyConsentButIgnoresKeyScreenF2(t *testing.T) {
	var saved i18n.Language
	app := client.New(context.Background(), client.Options{SaveLanguage: func(l i18n.Language) error { saved = l; return errors.New("PRIVATE-path") }})
	m := newModel(app)
	m.snapshot = client.Snapshot{State: "ready"}
	m.screen = consentScreen
	m.consented = true
	m.input = []byte("pending-private-key")
	backing := m.input
	key(m, tea.KeyMsg{Type: tea.KeyF2})
	apply(m, key(m, runeKey("2")))
	if saved != i18n.English || app.Language() != i18n.English {
		t.Fatal("explicit choice did not apply/persist")
	}
	if m.consented || len(m.input) != 0 || strings.Trim(string(backing), "\x00") != "" {
		t.Fatal("locale switch retained pending authorization")
	}
	if strings.Contains(m.View(), "PRIVATE") || !strings.Contains(m.snapshot.Warning, "could not be saved") {
		t.Fatal("save failure must use safe localized warning")
	}
	m.screen = keyScreen
	m.consented = true
	key(m, runeKey("tskey-auth-SECRET"))
	key(m, tea.KeyMsg{Type: tea.KeyF2})
	if m.screen != keyScreen || !m.consented || string(m.input) != "tskey-auth-SECRET" || strings.Contains(m.View(), "SECRET") {
		t.Fatal("F2 changed secret screen or leaked input")
	}
}

func TestLanguageChooserPreservesOnlineSelectionAndReading(t *testing.T) {
	app, _ := fixtureApp(t, nil)
	m := newModel(app)
	m.screen = dashboardScreen
	m.snapshot = client.Snapshot{State: "online", Tasks: []worker.LocalTaskView{{Task: protocol.Task{ID: "selected", State: "running"}}}}
	m.selected = "selected"
	m.logs = true
	m.history = true
	m.scroll = 4
	key(m, tea.KeyMsg{Type: tea.KeyF2})
	apply(m, key(m, runeKey("2")))
	if m.selected != "selected" || !m.logs || !m.history || m.scroll != 4 || m.screen != dashboardScreen {
		t.Fatal("locale choice changed online task/reading state")
	}
}

func TestEnglishPlainReporterRetainsNumbersAcrossLanguageSwitch(t *testing.T) {
	p := plainReporter{}
	var out bytes.Buffer
	task := worker.LocalTaskView{Task: protocol.Task{ID: "stable", State: "running"}, Progress: events.Progress{Stage: "installing"}}
	p.print(&out, client.Snapshot{State: "online", Tasks: []worker.LocalTaskView{task}})
	out.Reset()
	p.print(&out, client.Snapshot{State: "online", Language: i18n.English, Warning: "settings warning", Error: "operation error", Tasks: []worker.LocalTaskView{task}})
	for _, want := range []string{"Connected", "Task 1", "Installing", "Elapsed", "settings warning", "operation error"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, out.String())
		}
	}
	if len(p.ids) != 1 || p.ids[0] != "stable" {
		t.Fatal("locale reset task number")
	}
}

func TestEnglishConsentIncludesCompleteWarnings(t *testing.T) {
	s := client.Snapshot{Language: i18n.English, State: "ready", Warning: "settings warning", Error: "operation error", Authorization: time.Hour}
	got := strings.Join(consentLines(s, time.Now()), "\n")
	for _, want := range []string{"settings warning", "operation error", "trusted", "will not"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q: %s", want, got)
		}
	}
}

func TestPlainEnglishRequiresExactAgreeAndTreatsPasswordAsData(t *testing.T) {
	for _, answer := range []string{"", "同意", "agree", "AGREE"} {
		t.Run(answer, func(t *testing.T) {
			app, path := fixtureApp(t, nil)
			if err := app.LoadPairing(path); err != nil {
				t.Fatal(err)
			}
			_ = app.SetLanguage(i18n.English)
			var out bytes.Buffer
			reads := 0
			languageHints := 0
			err := runPlainSession(context.Background(), app, &out, func() (string, error) { return answer, nil }, func() ([]byte, error) {
				reads++
				languageHints = strings.Count(out.String(), "lang zh-CN")
				return []byte("lang zh-CN"), nil
			}, func() <-chan lineResult { return nil })
			if answer == "AGREE" {
				if reads != 1 || err == nil {
					t.Fatal("AGREE did not reach masked input/key validation")
				}
			} else if reads != 0 || err != nil {
				t.Fatal("non-exact agreement reached masked input")
			}
			if app.Language() != i18n.English || (reads > 0 && strings.Count(out.String(), "lang zh-CN") != languageHints) {
				t.Fatal("password was parsed or echoed")
			}
		})
	}
}

func TestTerminalWarningsAndErrorsVisibleInEveryScreen(t *testing.T) {
	app, _ := fixtureApp(t, nil)
	m := newModel(app)
	m.snapshot = client.Snapshot{Language: i18n.English, Warning: "settings warning", Error: "operation error"}
	for _, screen := range []screen{pathScreen, loadingScreen, consentScreen, keyScreen, dashboardScreen} {
		m.screen = screen
		if !strings.Contains(m.View(), "settings warning") || !strings.Contains(m.View(), "operation error") {
			t.Fatalf("screen %d hides warning/error", screen)
		}
	}
}

func TestPlainLanguageCommandsReprintFullAuthorization(t *testing.T) {
	for _, atPath := range []bool{false, true} {
		t.Run(fmt.Sprint(atPath), func(t *testing.T) {
			app, path := fixtureApp(t, nil)
			if !atPath {
				if err := app.LoadPairing(path); err != nil {
					t.Fatal(err)
				}
			}
			answers := []string{"lang en-US", ""}
			if atPath {
				answers = []string{"lang en-US", path, ""}
			}
			var out bytes.Buffer
			err := runPlainSession(context.Background(), app, &out, func() (string, error) {
				if len(answers) == 0 {
					return "", io.EOF
				}
				s := answers[0]
				answers = answers[1:]
				return s, nil
			}, func() ([]byte, error) { t.Fatal("locale command/Enter granted authorization"); return nil, nil }, func() <-chan lineResult { return nil })
			if err != nil {
				t.Fatal(err)
			}
			if app.Language() != i18n.English || !strings.Contains(out.String(), "Review the authorization") || !strings.Contains(out.String(), "trusted controller") || !strings.Contains(out.String(), "AGREE") {
				t.Fatalf("full authorization not reprinted: %s", out.String())
			}
			if len(answers) != 0 {
				t.Fatal("locale command was treated as a rejection instead of re-review")
			}
		})
	}
}

func TestQueuedSnapshotCannotUndoChosenLanguage(t *testing.T) {
	app, _ := fixtureApp(t, nil)
	m := newModel(app)
	old := app.Snapshot()
	key(m, tea.KeyMsg{Type: tea.KeyF2})
	apply(m, key(m, runeKey("2")))
	m.Update(snapshotMsg(old))
	if !strings.Contains(m.View(), "Select a pairing file") {
		t.Fatal("queued snapshot reverted the chosen language")
	}
}

func TestPathLanguageChoicePreservesNonsecretPathInput(t *testing.T) {
	app, _ := fixtureApp(t, nil)
	m := newModel(app)
	key(m, runeKey(`C:\pairing.json`))
	key(m, tea.KeyMsg{Type: tea.KeyF2})
	apply(m, key(m, runeKey("2")))
	if string(m.input) != `C:\pairing.json` {
		t.Fatal("locale choice erased the nonsecret pairing path")
	}
}

func TestEnglishTerminalSizesKeepExplicitActionsAccessible(t *testing.T) {
	app, path := fixtureApp(t, nil)
	_ = app.SetLanguage(i18n.English)
	m := newModel(app)
	load(t, m, path)
	for _, size := range []tea.WindowSizeMsg{{Width: 60, Height: 18}, {Width: 100, Height: 30}} {
		m.Update(size)
		key(m, tea.KeyMsg{Type: tea.KeyEnd})
		for _, line := range strings.Split(m.View(), "\n") {
			if lipgloss.Width(line) > size.Width {
				t.Fatal("English terminal overflows")
			}
		}
		if len(strings.Split(m.View(), "\n")) > size.Height || !strings.Contains(m.View(), "Y explicitly agrees") {
			t.Fatal("English consent actions inaccessible")
		}
	}
}

func TestOnlineLanguageSwitchPreservesActualTUICancelTarget(t *testing.T) {
	c := &testConn{done: make(chan struct{}), tasks: []worker.LocalTaskView{{Task: protocol.Task{ID: "one", State: protocol.TaskRunning}}, {Task: protocol.Task{ID: "two", State: protocol.Queued}}}}
	app, path := fixtureApp(t, func(context.Context, protocol.PairingBundle, []byte) (client.Connection, error) { return c, nil })
	if err := app.LoadPairing(path); err != nil {
		t.Fatal(err)
	}
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
	m.selected = "two"
	key(m, tea.KeyMsg{Type: tea.KeyF2})
	apply(m, key(m, runeKey("2")))
	apply(m, key(m, runeKey("c")))
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != "two" {
		t.Fatalf("localized TUI canceled %s", c.cancel)
	}
}

func TestPlainConnectedLanguageCommandsKeepNumbersAndResetDisconnectConsent(t *testing.T) {
	c := &testConn{done: make(chan struct{}), tasks: []worker.LocalTaskView{{Task: protocol.Task{ID: "stable", State: protocol.TaskRunning}, Progress: events.Progress{Stage: "installing"}}}}
	app, path := fixtureApp(t, func(context.Context, protocol.PairingBundle, []byte) (client.Connection, error) { return c, nil })
	if err := app.LoadPairing(path); err != nil {
		t.Fatal(err)
	}
	commands := []string{"d", "lang en-US", "AGREE", "c 1", "d", "AGREE"}
	var out bytes.Buffer
	err := runPlainSession(context.Background(), app, &out, func() (string, error) { return "同意", nil }, func() ([]byte, error) { return []byte("tskey-auth-inert"), nil }, func() <-chan lineResult {
		deadline := time.Now().Add(time.Second)
		for app.Snapshot().State == "connecting" {
			if time.Now().After(deadline) {
				t.Fatal("inert connection not online")
			}
			time.Sleep(time.Millisecond)
		}
		if len(commands) == 0 {
			ch := make(chan lineResult, 1)
			ch <- lineResult{err: io.EOF}
			return ch
		}
		if len(commands) == 3 {
			select {
			case <-c.done:
				t.Fatal("language change reused disconnect consent")
			default:
			}
		}
		ch := make(chan lineResult, 1)
		ch <- lineResult{data: []byte(commands[0])}
		commands = commands[1:]
		return ch
	})
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != "stable" || app.Language() != i18n.English || !strings.Contains(out.String(), "Task 1") || !strings.Contains(out.String(), "Enter AGREE to disconnect") {
		t.Fatalf("plain switch lost task mapping/localized disconnect: %s", out.String())
	}
}
