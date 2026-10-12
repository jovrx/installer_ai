//go:build windows

package gui

import (
	"context"
	"errors"
	"fmt"
	"remoteops/internal/i18n"
	"remoteops/internal/worker"
	"remoteops/internal/worker/client"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

type update struct {
	generation uint64
	request    uint64
	kind       string
	view       client.Snapshot
	err        error
}
type window struct {
	app                                           *client.App
	hwnd                                          uintptr
	controls                                      map[int]uintptr
	dpi                                           int
	font, titleFont, whiteBrush                   uintptr
	view                                          client.Snapshot
	results                                       chan update
	stop                                          chan struct{}
	generation                                    uint64
	busy, polling, closing, closed, logs, history bool
	closeAt                                       time.Time
	selected                                      string
	taskIDs                                       []string
	listText                                      string
	logTask                                       string
	notice                                        string
	explicitSelection                             bool
	authorization                                 bool
	tickCount                                     int
	languagePending                               bool
	pairingLoading                                bool
	languageSave                                  sync.Mutex
	languageRequest                               atomic.Uint64
}

// Run owns all HWNDs on one OS thread. Application calls run outside the message loop.
func Run(ctx context.Context, app *client.App) error {
	if app == nil {
		return errors.New(i18n.Text(i18n.Chinese, "worker.gui.uninitialized"))
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// Per-monitor awareness is scoped to this UI thread; older systems use their default.
	dpiProc := user32.NewProc("SetThreadDpiAwarenessContext")
	var old uintptr
	if dpiProc.Find() == nil {
		old, _, _ = dpiProc.Call(^uintptr(3))
		defer dpiProc.Call(old)
	}
	w, err := newWindow(app)
	if err != nil {
		return err
	}
	defer w.dispose()
	visible(w.hwnd, true)
	setFocus.Call(w.controls[idPath])
	for !w.closed {
		if ctx.Err() != nil && !w.closing {
			w.beginClose()
		}
		var m message
		r, _, e := getMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) == -1 {
			w.beginClose()
			app.Wait()
			return fmt.Errorf("%s: %w", i18n.Text(app.Language(), "worker.gui.message_error"), e)
		}
		if r == 0 {
			w.beginClose()
			app.Wait()
			return nil
		}
		used, _, _ := isDialogMessage.Call(w.hwnd, uintptr(unsafe.Pointer(&m)))
		if used == 0 {
			translateMessage.Call(uintptr(unsafe.Pointer(&m)))
			dispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
		}
	}
	return nil
}
func newWindow(app *client.App) (*window, error) {
	if err := registerClass(app.Language()); err != nil {
		return nil, err
	}
	w := &window{app: app, dpi: 96, controls: map[int]uintptr{}, results: make(chan update, 8), stop: make(chan struct{}), view: client.Snapshot{State: "idle", Language: app.Language()}}
	h, _, err := createWindow.Call(0x10000, ptr(u16("RemoteOpsWorkerWindow")), ptr(u16(i18n.Text(w.view.Language, "worker.gui.title"))), 0xcf0000|0x2000000, 0x80000000, 0x80000000, 840, 690, 0, 0, 0, 0)
	if h == 0 {
		return nil, fmt.Errorf("%s: %w", i18n.Text(app.Language(), "worker.gui.create_error"), err)
	}
	w.hwnd = h
	windowRegistry.Store(h, w)
	if p := user32.NewProc("GetDpiForWindow"); p.Find() == nil {
		v, _, _ := p.Call(h)
		if v > 0 {
			w.dpi = int(v)
		}
	}
	user32.NewProc("SetWindowPos").Call(h, 0, 0, 0, uintptr(scale(840, w.dpi)), uintptr(scale(690, w.dpi)), 0x16)
	w.whiteBrush, _, _ = createBrush.Call(0xffffff)
	defs := []struct {
		id             int
		class, caption string
		style          uintptr
	}{
		{idPathLabel, "STATIC", i18n.Text(w.view.Language, "worker.gui.path_label"), 0},
		{idPath, "EDIT", "", wsBorder | wsTabStop | 0x80}, {idBrowse, "BUTTON", i18n.Text(w.view.Language, "worker.gui.browse"), wsTabStop}, {idLoad, "BUTTON", i18n.Text(w.view.Language, "worker.gui.load"), wsTabStop},
		{idScopeLabel, "STATIC", i18n.Text(w.view.Language, "worker.gui.scope_label"), 0},
		{idScope, "EDIT", "", wsBorder | wsVScroll | wsTabStop | 0x4 | 0x40 | 0x800},
		{idConsent, "BUTTON", i18n.Text(w.view.Language, "worker.gui.consent"), wsTabStop | 3 | 0x2000},
		{idKeyLabel, "STATIC", "Auth Key(&K)", 0},
		{idKey, "EDIT", "", wsBorder | wsTabStop | 0x20 | 0x80},
		{idConnect, "BUTTON", i18n.Text(w.view.Language, "worker.gui.connect"), wsTabStop | 1},
		{idDisconnect, "BUTTON", i18n.Text(w.view.Language, "worker.gui.disconnect"), wsTabStop},
		{idExit, "BUTTON", i18n.Text(w.view.Language, "worker.gui.exit"), wsTabStop},
		{idTasks, "LISTBOX", i18n.Text(w.view.Language, "worker.gui.tasks"), wsBorder | wsVScroll | wsTabStop | 1 | 0x100},
		{idHistory, "BUTTON", i18n.Text(w.view.Language, "worker.gui.history_open"), wsTabStop},
		{idLogsToggle, "BUTTON", i18n.Text(w.view.Language, "worker.gui.logs_open"), wsTabStop},
		{idLogs, "EDIT", "", wsBorder | wsVScroll | wsTabStop | 0x4 | 0x40 | 0x800},
		{idCancel, "BUTTON", i18n.Text(w.view.Language, "worker.gui.cancel"), wsTabStop},
		{idNewPairing, "BUTTON", i18n.Text(w.view.Language, "worker.gui.new_pairing"), wsTabStop},
		{idAuthorization, "BUTTON", i18n.Text(w.view.Language, "worker.gui.authorization"), wsTabStop},
		{idLanguageLabel, "STATIC", i18n.Text(w.view.Language, "worker.gui.language_label"), 0},
		{idLanguage, "COMBOBOX", "", wsTabStop | wsVScroll | 3},
		{idMessages, "EDIT", "", wsTabStop | wsVScroll | 0x4 | 0x40 | 0x800},
	}
	for _, d := range defs {
		child, _, e := createWindow.Call(0, ptr(u16(d.class)), ptr(u16(d.caption)), wsChild|d.style, 0, 0, 10, 10, h, uintptr(d.id), 0, 0)
		if child == 0 {
			w.dispose()
			return nil, fmt.Errorf("%s: %w", i18n.Text(app.Language(), "worker.gui.control_error"), e)
		}
		w.controls[d.id] = child
	}
	for _, caption := range []string{"简体中文", "English"} {
		sendMessage.Call(w.controls[idLanguage], 0x143, 0, ptr(u16(caption)))
	}
	sendMessage.Call(w.controls[idKey], 0xc5, 4096, 0)
	sendMessage.Call(w.controls[idPath], 0xc5, 32767, 0)
	w.setFonts()
	w.layout()
	w.refresh()
	setTimer.Call(h, 1, 100, 0)
	w.poll()
	return w, nil
}
func (w *window) dispose() {
	select {
	case <-w.stop:
	default:
		close(w.stop)
	}
	killTimer.Call(w.hwnd, 1)
	windowRegistry.Delete(w.hwnd)
	destroyWindow.Call(w.hwnd)
	for _, v := range []uintptr{w.font, w.titleFont, w.whiteBrush} {
		if v != 0 {
			deleteObject.Call(v)
		}
	}
}
func (w *window) deliver(u update) {
	select {
	case w.results <- u:
	case <-w.stop:
	}
}
func (w *window) poll() {
	if w.polling || w.closing {
		return
	}
	w.polling = true
	g := w.generation
	request := w.languageRequest.Load()
	go func() { w.deliver(update{generation: g, request: request, kind: "snapshot", view: w.app.Snapshot()}) }()
}
func (w *window) start(kind string, f func() error) {
	w.generation++
	g := w.generation
	if kind == "load" {
		w.pairingLoading = true
	}
	w.busy = true
	w.notice = ""
	w.refresh()
	go func() { err := f(); w.deliver(update{generation: g, kind: kind, err: err, view: w.app.Snapshot()}) }()
}
func (w *window) tick() {
	w.tickCount++
	for {
		select {
		case u := <-w.results:
			if u.kind == "snapshot" {
				w.polling = false
			}
			if u.kind == "closed" {
				w.closed = true
				destroyWindow.Call(w.hwnd)
				return
			}
			if u.generation != w.generation || w.closing {
				continue
			}
			if u.kind == "load" {
				// Invalidate observations captured both before and during loading.
				w.generation++
				w.busy, w.pairingLoading = false, false
				language := w.view.Language
				w.view = u.view
				w.view.Language = language
				w.view.Scope = nil
				w.languagePending = true
				if u.err != nil {
					w.notice = client.ErrorText(u.err, language)
				}
				w.refreshLanguageScope()
				w.refresh()
				continue
			}
			if (u.kind == "language" || u.kind == "snapshot") && (u.request != w.languageRequest.Load() || w.pairingLoading) {
				continue
			}
			if u.kind != "snapshot" && u.kind != "language" {
				w.busy = false
			}
			if w.view.Language != "" && u.view.Language != w.view.Language {
				if u.err != nil && u.kind != "language" {
					w.notice = client.ErrorText(u.err, w.view.Language)
				}
				w.refresh()
				continue
			}
			w.languagePending = false
			if u.kind == "language" {
				// Locale work must not restore an older lifecycle or task snapshot.
				w.view.Scope = u.view.Scope
				w.view.Warning, w.view.Error, w.view.Notice = u.view.Warning, u.view.Error, u.view.Notice
			} else {
				w.view = u.view
			}
			if u.err != nil && u.kind != "language" {
				w.notice = client.ErrorText(u.err, w.view.Language)
				setText(w.controls[idKey], "")
			}
			w.refresh()
		default:
			goto drained
		}
	}
drained:
	if w.closing && time.Since(w.closeAt) >= 8*time.Second {
		w.notice = i18n.Text(w.view.Language, "worker.gui.cleanup")
		w.refresh()
	}
	if w.tickCount%5 == 0 {
		w.poll()
		w.refresh()
	}
	invalidateRect.Call(w.hwnd, 0, 0)
}
func (w *window) beginClose() {
	if w.closing {
		return
	}
	w.closing = true
	w.generation++
	w.closeAt = time.Now()
	w.notice = i18n.Text(w.view.Language, "worker.gui.closing")
	setText(w.controls[idKey], "")
	w.refresh()
	go func() { w.app.Disconnect("local_closed"); w.app.Wait(); w.deliver(update{kind: "closed"}) }()
}
func (w *window) command(id int) {
	if id == idExit {
		w.beginClose()
		return
	}
	if w.closing {
		return
	}
	switch id {
	case idLanguage:
		index, _, _ := sendMessage.Call(w.controls[idLanguage], 0x147, 0, 0)
		language := i18n.Chinese
		if index == 1 {
			language = i18n.English
		} else if index != 0 {
			return
		}
		w.chooseLanguage(language)
	case idDisconnect:
		w.generation++
		g := w.generation
		w.busy = true
		w.notice = i18n.Text(w.view.Language, "worker.gui.disconnecting")
		setText(w.controls[idKey], "")
		w.refresh()
		summary := w.view
		summary.State = "disconnected"
		summary.Notice = i18n.Text(w.view.Language, "worker.gui.disconnected")
		go func() {
			w.app.Disconnect("local_closed")
			w.deliver(update{generation: g, kind: "disconnect", view: summary})
		}()
	case idBrowse, idNewPairing:
		if w.busy || w.view.State == "online" || w.view.State == "connecting" {
			return
		}
		path, err := choosePairing(w.hwnd, w.view.Language)
		if err != nil {
			w.notice = err.Error()
			w.refresh()
			return
		}
		if path == "" {
			return
		}
		setText(w.controls[idPath], path)
		w.load(path)
	case idLoad:
		if w.busy || w.view.State == "online" || w.view.State == "connecting" {
			return
		}
		w.load(textOf(w.controls[idPath]))
	case idConsent:
		w.refresh()
	case idConnect:
		key := []byte(textOf(w.controls[idKey]))
		setText(w.controls[idKey], "")
		if !canConnect(w.view, checked(w.controls[idConsent]), w.busy || w.languagePending, time.Now()) {
			clear(key)
			w.notice = i18n.Text(w.view.Language, "worker.gui.consent_required")
			w.refresh()
			return
		}
		w.view.State = "connecting"
		w.start("connect", func() error { return w.app.ConsentAndConnect(key) })
	case idTasks:
		i, _, _ := sendMessage.Call(w.controls[idTasks], 0x188, 0, 0)
		if int(i) >= 0 && int(i) < len(w.taskIDs) {
			w.selected = w.taskIDs[i]
			w.explicitSelection = true
		}
		w.refresh()
	case idCancel:
		v, ok := selectedTask(w.view.Tasks, w.selected)
		if !ok || w.view.State != "online" || (v.Task.State != "running" && v.Task.State != "queued") {
			return
		}
		id := v.Task.ID
		g := w.generation
		go func() {
			err := w.app.CancelTask(id)
			w.deliver(update{generation: g, kind: "cancel", err: err, view: w.app.Snapshot()})
		}()
	case idHistory:
		w.history = !w.history
		w.listText = ""
		w.refresh()
	case idAuthorization:
		w.authorization = true
		w.logs = true
		w.refresh()
	case idLogsToggle:
		w.authorization = false
		w.logs = !w.logs
		w.refresh()
	}
}
func (w *window) load(path string) {
	setText(w.controls[idKey], "")
	sendMessage.Call(w.controls[idConsent], 0xf1, 0, 0)
	w.selected = ""
	w.explicitSelection = false
	w.listText = ""
	w.start("load", func() error { return w.app.LoadPairing(path) })
}
func (w *window) connected() bool {
	return w.view.State == "online" || w.view.State == "connecting" || w.view.State == "disconnected" || len(w.view.Tasks) > 0 || w.closing
}
func (w *window) layout() {
	var r winRect
	getClientRect.Call(w.hwnd, uintptr(unsafe.Pointer(&r)))
	l := makeLayout(int(r.Right), int(r.Bottom), w.dpi, w.connected(), w.logs)
	for id, h := range w.controls {
		p, ok := l[id]
		visible(h, ok)
		if ok {
			height := p.H
			if id == idLanguage {
				height = scale(180, w.dpi)
			}
			moveWindow.Call(h, uintptr(p.X), uintptr(p.Y), uintptr(p.W), uintptr(height), 1)
		}
	}
	invalidateRect.Call(w.hwnd, 0, 0)
}
func (w *window) refresh() {
	defer w.restoreFocus()
	now := time.Now()
	w.layout()
	w.localizeControls()
	enabled(w.controls[idLanguage], !w.closing)
	active := w.view.State == "online" || w.view.State == "connecting"
	for _, id := range []int{idPath, idBrowse, idLoad, idNewPairing} {
		enabled(w.controls[id], !active && !w.busy && !w.closing)
	}
	ready := w.view.State == "ready" && now.Before(w.view.PairingExpiresAt) && !w.busy && !w.closing && !w.languagePending
	if !ready || !checked(w.controls[idConsent]) {
		setText(w.controls[idKey], "")
	}
	enabled(w.controls[idConsent], ready)
	enabled(w.controls[idKey], ready && checked(w.controls[idConsent]))
	enabled(w.controls[idConnect], canConnect(w.view, checked(w.controls[idConsent]), w.busy || w.closing || w.languagePending, now))
	enabled(w.controls[idDisconnect], active && !w.closing)
	enabled(w.controls[idExit], !w.closing)
	summary := i18n.Text(w.view.Language, "worker.gui.scope_empty")
	if w.view.Controller != "" {
		summary = i18n.Text(w.view.Language, "worker.gui.controller") + w.view.Controller + i18n.Text(w.view.Language, "worker.gui.address") + w.view.Address + i18n.Text(w.view.Language, "worker.gui.deadline") + w.view.PairingExpiresAt.Local().Format("2006-01-02 15:04:05") + i18n.Text(w.view.Language, "worker.gui.duration", int(w.view.Authorization.Minutes())) + strings.Join(w.view.Scope, "\r\n")
	}
	summary += "\r\n\r\n" + i18n.Text(w.view.Language, "worker.ui.trust") + "\r\n" + i18n.Text(w.view.Language, "worker.ui.explicit")
	setText(w.controls[idScope], summary)
	setText(w.controls[idMessages], w.messages())
	if w.history {
		setText(w.controls[idHistory], i18n.Text(w.view.Language, "worker.gui.history_close"))
	} else {
		setText(w.controls[idHistory], i18n.Text(w.view.Language, "worker.gui.history_open"))
	}
	if w.logs {
		if w.authorization {
			setText(w.controls[idLogsToggle], i18n.Text(w.view.Language, "worker.gui.authorization_close"))
		} else {
			setText(w.controls[idLogsToggle], i18n.Text(w.view.Language, "worker.gui.logs_close"))
		}
	} else {
		setText(w.controls[idLogsToggle], i18n.Text(w.view.Language, "worker.gui.logs_open"))
	}
	tasks := w.view.Tasks
	limit := 20
	if w.history {
		limit = 100
	}
	tasks = worker.RecentTaskViews(tasks, limit)
	if !w.explicitSelection {
		if v, ok := client.PrimaryTask(tasks); ok {
			w.selected = v.Task.ID
		}
	}
	lines := make([]string, 0, len(tasks))
	ids := make([]string, 0, len(tasks))
	for _, v := range tasks {
		lines = append(lines, client.TaskTitle(v, w.view.Language)+" · "+taskStatus(v, w.view.Language))
		ids = append(ids, v.Task.ID)
	}
	key := strings.Join(ids, "\n") + strings.Join(lines, "\n")
	if key != w.listText {
		w.listText = key
		w.taskIDs = ids
		sendMessage.Call(w.controls[idTasks], 0xb, 0, 0)
		sendMessage.Call(w.controls[idTasks], 0x184, 0, 0)
		for i, line := range lines {
			sendMessage.Call(w.controls[idTasks], 0x180, 0, ptr(u16(line)))
			if ids[i] == w.selected {
				sendMessage.Call(w.controls[idTasks], 0x186, uintptr(i), 0)
			}
		}
		sendMessage.Call(w.controls[idTasks], 0xb, 1, 0)
		invalidateRect.Call(w.controls[idTasks], 0, 1)
	}
	v, ok := selectedTask(w.view.Tasks, w.selected)
	enabled(w.controls[idCancel], ok && w.view.State == "online" && !w.closing && (v.Task.State == "running" || v.Task.State == "queued"))
	if w.logs {
		logText := i18n.Text(w.view.Language, "worker.gui.logs_empty")
		if ok {
			logText = client.TaskTitle(v, w.view.Language) + "\r\n" + taskStatus(v, w.view.Language) + "\r\n\r\n"
			for _, l := range v.Logs {
				logText += l.Timestamp.Local().Format("15:04:05") + "  " + client.SafeText(l.Message) + "\r\n"
			}
		}
		if w.authorization {
			logText = summary
			setText(w.controls[idLogs], logText)
			w.logTask = ""
		} else {
			setLogText(w.controls[idLogs], logText, w.logTask == w.selected)
			w.logTask = w.selected
		}
	}
}

func (w *window) localizeControls() {
	setText(w.hwnd, i18n.Text(w.view.Language, "worker.gui.title"))
	for id, key := range map[int]string{
		idPathLabel: "path_label", idBrowse: "browse", idLoad: "load", idScopeLabel: "scope_label", idConsent: "consent", idConnect: "connect", idDisconnect: "disconnect", idExit: "exit", idCancel: "cancel", idNewPairing: "new_pairing", idAuthorization: "authorization", idLanguageLabel: "language_label",
	} {
		setText(w.controls[id], i18n.Text(w.view.Language, "worker.gui."+key))
	}
	index := uintptr(0)
	if w.view.Language == i18n.English {
		index = 1
	}
	sendMessage.Call(w.controls[idLanguage], 0x14e, index, 0)
}

// Native controls and captions change on the message thread. Disk persistence
// and snapshot collection run off-thread, without invalidating task callbacks.
func (w *window) chooseLanguage(language i18n.Language) {
	if language == w.view.Language {
		return
	}
	if !w.connected() {
		setText(w.controls[idKey], "")
		sendMessage.Call(w.controls[idConsent], 0xf1, 0, 0)
	}
	w.view.Language = language
	w.languagePending = true
	w.notice = ""
	w.refresh()
	g := w.generation
	request := w.languageRequest.Add(1)
	go func() {
		w.languageSave.Lock()
		defer w.languageSave.Unlock()
		if request != w.languageRequest.Load() {
			return
		}
		err := w.app.SetLanguage(language)
		w.deliver(update{generation: g, request: request, kind: "language", view: w.app.Snapshot(), err: err})
	}()
	w.poll()
}

func (w *window) refreshLanguageScope() {
	g, request := w.generation, w.languageRequest.Load()
	go func() {
		w.languageSave.Lock()
		defer w.languageSave.Unlock()
		if request != w.languageRequest.Load() {
			return
		}
		w.deliver(update{generation: g, request: request, kind: "language", view: w.app.Snapshot()})
	}()
}

func (w *window) messages() string {
	var lines []string
	for _, message := range []string{w.view.Warning, w.view.Error, w.view.Notice, w.notice} {
		if message != "" {
			lines = append(lines, client.SafeText(message))
		}
	}
	if w.view.State == "ready" && !time.Now().Before(w.view.PairingExpiresAt) {
		lines = append(lines, i18n.Text(w.view.Language, "worker.gui.expired"))
	}
	return strings.Join(lines, "\r\n")
}

// Hiding or disabling a focused form control can leave focus on the top-level
// HWND, where IsDialogMessage cannot resolve native access keys. Repair only
// unusable focus in our active window; never steal focus from a valid edit or
// another window/application.
func (w *window) restoreFocus() {
	active, _, _ := user32.NewProc("GetActiveWindow").Call()
	if active != w.hwnd || w.closing {
		return
	}
	focus, _, _ := user32.NewProc("GetFocus").Call()
	if focus != 0 && focus != w.hwnd {
		visible, _, _ := user32.NewProc("IsWindowVisible").Call(focus)
		enabled, _, _ := user32.NewProc("IsWindowEnabled").Call(focus)
		if visible != 0 && enabled != 0 {
			return
		}
	}
	next, _, _ := user32.NewProc("GetNextDlgTabItem").Call(w.hwnd, 0, 0)
	if next != 0 {
		setFocus.Call(next)
	}
}
