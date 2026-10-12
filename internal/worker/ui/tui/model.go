// Package tui provides the local bilingual consent and task interface.
package tui

import (
	"context"
	"os"
	"os/signal"
	"remoteops/internal/i18n"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
	"remoteops/internal/worker/client"
)

type screen int

const (
	pathScreen screen = iota
	loadingScreen
	consentScreen
	keyScreen
	dashboardScreen
)

type snapshotMsg client.Snapshot
type refreshedMsg struct {
	pairingRevision uint64
	request         uint64
	snapshot        client.Snapshot
}
type loadedMsg struct {
	pairingRevision uint64
	snapshot        client.Snapshot
	err             error
}
type actionMsg struct{ err error }
type languageMsg struct {
	pairingRevision uint64
	request         uint64
	snapshot        client.Snapshot
}
type tickMsg time.Time
type activityMsg time.Time

type model struct {
	app                                               *client.App
	screen                                            screen
	snapshot                                          client.Snapshot
	input                                             []byte
	width, height, scroll                             int
	selected                                          string
	logs, history, help, confirmDisconnect, consented bool
	languageMenu                                      bool
	languagePending                                   bool
	languageRequest                                   atomic.Uint64
	languageSave                                      sync.Mutex
	pairingRevision                                   uint64
	pairingLoading                                    bool
	err                                               string
	now                                               time.Time
	initialized                                       bool
	renderer                                          *lipgloss.Renderer
	frame                                             int
	connectingAt                                      time.Time
}

func newModel(a *client.App) *model {
	return &model{app: a, snapshot: client.Snapshot{Language: a.Language()}, width: 100, height: 30, now: time.Now(), renderer: lipgloss.NewRenderer(os.Stdout)}
}

func (m *model) Init() tea.Cmd { return tea.Batch(m.refresh(), nextActivity()) }
func (m *model) refresh() tea.Cmd {
	revision, request := m.pairingRevision, m.languageRequest.Load()
	return func() tea.Msg {
		return refreshedMsg{pairingRevision: revision, request: request, snapshot: m.app.Snapshot()}
	}
}
func nextTick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}
func nextActivity() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(t time.Time) tea.Msg { return activityMsg(t) })
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(msg.Width, 1)
		m.height = max(msg.Height, 1)
		m.scroll = min(m.scroll, m.maxScroll())
	case tickMsg:
		m.now = time.Time(msg)
		m.frame++
		return m, m.refresh()
	case activityMsg:
		m.now = time.Time(msg)
		m.frame++
		return m, nextActivity()
	case refreshedMsg:
		if msg.pairingRevision != m.pairingRevision || msg.request != m.languageRequest.Load() || m.pairingLoading {
			return m, nextTick()
		}
		return m.Update(snapshotMsg(msg.snapshot))
	case snapshotMsg:
		if msg.Language != "" && ((m.languageRequest.Load() > 0 && msg.Language != m.snapshot.Language) || msg.Language != m.app.Language()) {
			return m, nextTick()
		}
		m.snapshot = client.Snapshot(msg)
		if m.snapshot.State == "connecting" && m.connectingAt.IsZero() {
			m.connectingAt = m.now
		}
		// Only the initial snapshot selects a screen automatically. Subsequent
		// refreshes preserve local choices to reject/reload a pairing file.
		if !m.initialized {
			m.initialized = true
			switch m.snapshot.State {
			case "ready":
				m.screen = consentScreen
				m.scroll = 0
			case "online", "connecting", "disconnected":
				m.screen = dashboardScreen
			}
		}
		if m.screen == dashboardScreen {
			m.ensureSelection()
		}
		return m, nextTick()
	case loadedMsg:
		if msg.pairingRevision != m.pairingRevision {
			return m, nil
		}
		// A command issued during loading can still have captured the old App
		// scope. Invalidate those results as well as results from before loading.
		m.pairingRevision++
		m.pairingLoading = false
		m.initialized = true
		m.err = client.ErrorText(msg.err, m.snapshot.Language)
		m.scroll = 0
		if msg.err != nil {
			m.screen = pathScreen
		} else {
			m.screen = consentScreen
		}
		if msg.snapshot.Language != "" && msg.snapshot.Language != m.snapshot.Language {
			language := m.snapshot.Language
			m.snapshot = msg.snapshot
			m.snapshot.Language = language
			m.snapshot.Scope = nil
			m.languagePending = true
			return m, m.refreshLanguageScope()
		}
		m.snapshot = msg.snapshot
		if m.languagePending {
			return m, m.refreshLanguageScope()
		}
	case languageMsg:
		if msg.request != m.languageRequest.Load() || msg.snapshot.Language != m.snapshot.Language {
			return m, nil
		}
		if m.pairingLoading {
			return m, nil
		}
		if msg.pairingRevision != m.pairingRevision {
			if m.languagePending {
				return m, m.refreshLanguageScope()
			}
			return m, nil
		}
		if m.languagePending && m.screen == consentScreen {
			m.scroll = 0
		}
		m.languagePending = false
		m.snapshot.Scope = msg.snapshot.Scope
		m.snapshot.Warning, m.snapshot.Error, m.snapshot.Notice = msg.snapshot.Warning, msg.snapshot.Error, msg.snapshot.Notice
	case actionMsg:
		if msg.err != nil {
			m.err = client.ErrorText(msg.err, m.snapshot.Language)
		}
	case tea.KeyMsg:
		// Emergency shutdown takes precedence over prompts, paste and all screens.
		if msg.Type == tea.KeyCtrlC {
			m.languageRequest.Add(1)
			m.pairingRevision++
			m.eraseInput()
			m.app.Disconnect("local_closed")
			return m, tea.Quit
		}
		// Function keys never act on masked key input; paste remains data only.
		if m.screen != keyScreen && !msg.Paste {
			if msg.Type == tea.KeyF2 {
				m.languageMenu = !m.languageMenu
				return m, nil
			}
			if m.languageMenu {
				switch msg.String() {
				case "1":
					return m, m.chooseLanguage(i18n.Chinese)
				case "2":
					return m, m.chooseLanguage(i18n.English)
				case "esc":
					m.languageMenu = false
				}
				return m, nil
			}
		}
		if m.screen == pathScreen || m.screen == keyScreen {
			return m, m.edit(msg)
		}
		// Bracketed paste is data only. It never acts as consent or a dashboard key.
		if msg.Paste {
			return m, nil
		}
		if m.screen == loadingScreen {
			return m, nil
		}
		if m.screen == consentScreen {
			switch msg.String() {
			case "y", "Y":
				if !m.languagePending && m.scroll >= m.maxScroll() {
					m.consented = true
					m.screen = keyScreen
					m.scroll = 0
					m.err = ""
				}
			case "n", "N", "esc":
				m.consented = false
				m.screen = pathScreen
				m.scroll = 0
				m.err = ""
			default:
				m.scrollKey(msg)
			}
			return m, nil
		}
		if m.confirmDisconnect {
			switch msg.String() {
			case "y", "Y":
				m.app.Disconnect("local_closed")
				m.confirmDisconnect = false
				return m, m.refresh()
			case "n", "N", "esc":
				m.confirmDisconnect = false
			}
			return m, nil
		}
		switch msg.String() {
		case "l", "L":
			m.logs = !m.logs
			m.scroll = 0
		case "h", "H":
			m.history = !m.history
			m.ensureSelection()
			m.scroll = 0
		case "?":
			m.help = !m.help
			m.scroll = 0
		case "esc":
			m.help = false
			m.logs = false
			m.scroll = 0
		case "d", "D":
			m.confirmDisconnect = true
		case "c", "C":
			if v, ok := m.selectedTask(); ok && !v.Task.State.Terminal() {
				id := v.Task.ID
				return m, func() tea.Msg { return actionMsg{m.app.CancelTask(id)} }
			}
		case "up", "k":
			m.navigate(-1)
		case "down", "j":
			m.navigate(1)
		case "r", "R":
			if m.snapshot.State == "disconnected" || m.snapshot.State == "error" {
				m.screen = pathScreen
				m.consented = false
				m.scroll = 0
				m.err = ""
			}
		case "q", "Q":
			if m.snapshot.State == "disconnected" || m.snapshot.State == "error" {
				m.app.Disconnect("local_closed")
				return m, tea.Quit
			}
		default:
			m.scrollKey(msg)
		}
	}
	return m, nil
}

func (m *model) eraseInput() { clear(m.input); m.input = nil }

func (m *model) chooseLanguage(language i18n.Language) tea.Cmd {
	request := m.languageRequest.Add(1)
	revision := m.pairingRevision
	m.snapshot.Language = language
	m.languagePending = true
	m.languageMenu = false
	m.err = ""
	if m.screen != dashboardScreen {
		if m.screen != pathScreen {
			m.eraseInput()
		}
		m.consented = false
		m.scroll = 0
		if !m.pairingLoading && (m.screen == consentScreen || m.snapshot.State == "ready") {
			m.screen = consentScreen
		}
	}
	// Commands can run in any order. Serialize persistence off the event loop
	// and skip obsolete choices before they can change the shared language.
	return func() tea.Msg {
		m.languageSave.Lock()
		defer m.languageSave.Unlock()
		if request != m.languageRequest.Load() {
			return nil
		}
		_ = m.app.SetLanguage(language)
		return languageMsg{pairingRevision: revision, request: request, snapshot: m.app.Snapshot()}
	}
}

// Recollect authorization after a load without blocking Update or repeating a
// preference write. Serialize with language saving so scope uses the latest
// completed choice. Revision/request tags are captured only on the UI thread.
func (m *model) refreshLanguageScope() tea.Cmd {
	revision, request := m.pairingRevision, m.languageRequest.Load()
	return func() tea.Msg {
		m.languageSave.Lock()
		defer m.languageSave.Unlock()
		if request != m.languageRequest.Load() {
			return nil
		}
		return languageMsg{pairingRevision: revision, request: request, snapshot: m.app.Snapshot()}
	}
}

func (m *model) edit(k tea.KeyMsg) tea.Cmd {
	switch k.Type {
	case tea.KeyEnter:
		if m.screen == pathScreen {
			path := strings.Trim(strings.TrimSpace(string(m.input)), "\"")
			if path == "" {
				m.err = i18n.Text(m.snapshot.Language, "worker.tui.path_required")
				return nil
			}
			m.eraseInput()
			m.err = ""
			m.screen = loadingScreen
			m.consented = false
			m.pairingRevision++
			m.pairingLoading = true
			revision := m.pairingRevision
			return func() tea.Msg {
				err := m.app.LoadPairing(path)
				return loadedMsg{pairingRevision: revision, snapshot: m.app.Snapshot(), err: err}
			}
		}
		if !m.consented {
			m.eraseInput()
			m.screen = consentScreen
			return nil
		}
		// ConsentAndConnect only validates/copies under a short lock, and starts the
		// connector in App's goroutine. Keeping this boundary synchronous also avoids
		// a queued command retaining a credential after Ctrl+C.
		err := m.app.ConsentAndConnect(m.input)
		m.eraseInput()
		m.err = client.ErrorText(err, m.snapshot.Language)
		if err == nil {
			m.screen = dashboardScreen
			m.snapshot.State = "connecting"
			m.connectingAt = m.now
			m.scroll = 0
		}
	case tea.KeyEsc:
		m.eraseInput()
		m.consented = false
		m.screen = consentScreen
		m.err = ""
	case tea.KeyBackspace, tea.KeyDelete:
		if len(m.input) > 0 {
			_, n := utf8.DecodeLastRune(m.input)
			clear(m.input[len(m.input)-n:])
			m.input = m.input[:len(m.input)-n]
		}
	case tea.KeyCtrlU:
		m.eraseInput()
	case tea.KeyRunes, tea.KeySpace:
		// Reserve the bounded input once so appending a key does not leave old
		// secret-bearing backing arrays behind as the buffer grows.
		if m.input == nil {
			m.input = make([]byte, 0, 4096)
		}
		runes := k.Runes
		if k.Type == tea.KeySpace {
			runes = []rune{' '}
		}
		for _, r := range runes {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				continue
			}
			if len(m.input)+utf8.RuneLen(r) > 4096 {
				break
			}
			m.input = utf8.AppendRune(m.input, r)
		}
	}
	return nil
}

func (m *model) scrollKey(k tea.KeyMsg) {
	switch k.Type {
	case tea.KeyDown:
		m.scroll++
	case tea.KeyUp:
		m.scroll--
	case tea.KeyPgDown:
		m.scroll += m.bodyHeight()
	case tea.KeyPgUp:
		m.scroll -= m.bodyHeight()
	case tea.KeyHome:
		m.scroll = 0
	case tea.KeyEnd:
		m.scroll = m.maxScroll()
	}
	m.scroll = max(0, min(m.scroll, m.maxScroll()))
}

func plainTerminal(input, output, requested bool) bool {
	return requested || !input || !output || strings.EqualFold(os.Getenv("TERM"), "dumb")
}

// Run uses a full-screen terminal when available, or localized plain prompts.
// Neither mode accepts noninteractive authorization. App owns all networking.
func Run(ctx context.Context, app *client.App, plain bool) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			app.Disconnect("local_closed")
		case <-done:
		}
	}()
	defer close(done)
	defer func() { app.Disconnect("local_closed"); app.Wait() }()
	if plainTerminal(term.IsTerminal(int(os.Stdin.Fd())), term.IsTerminal(int(os.Stdout.Fd())), plain) {
		return runPlain(ctx, app, os.Stdin, os.Stdout)
	}
	m := newModel(app)
	defer m.eraseInput()
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithAltScreen(), tea.WithInput(os.Stdin), tea.WithOutput(os.Stdout))
	_, err := p.Run()
	if ctx.Err() != nil {
		return nil
	}
	return err
}
