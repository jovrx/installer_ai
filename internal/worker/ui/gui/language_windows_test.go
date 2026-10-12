//go:build windows

package gui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"remoteops/internal/i18n"
	"remoteops/internal/worker"
	"remoteops/internal/worker/client"
	"remoteops/internal/worker/events"
	"remoteops/pkg/protocol"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"
)

func TestNativeLanguageChoiceUpdatesCaptionsAndResetsReadyInput(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var saved i18n.Language
	app := client.New(context.Background(), client.Options{Language: i18n.English, SaveLanguage: func(l i18n.Language) error { saved = l; return errors.New("SECRET-path") }})
	w, err := newWindow(app)
	if err != nil {
		t.Fatal(err)
	}
	defer w.dispose()
	if !strings.Contains(textOf(w.controls[idConsent]), "agree") || !strings.Contains(textOf(w.controls[idScope]), "authorization") {
		t.Fatal("English native authorization missing")
	}
	// Existing IDs end at 119; 120 is the native language selector.
	if w.controls[120] == 0 {
		t.Fatal("native language selector missing")
	}
	w.view.State = "ready"
	w.view.PairingExpiresAt = time.Now().Add(time.Hour)
	sendMessage.Call(w.controls[idConsent], 0xf1, 1, 0)
	setText(w.controls[idKey], "tskey-auth-PRIVATE")
	sendMessage.Call(w.controls[120], 0x14e, 0, 0)
	w.command(120)
	deadline := time.Now().Add(time.Second)
	for w.view.Warning == "" && time.Now().Before(deadline) {
		w.tick()
		time.Sleep(time.Millisecond)
	}
	if saved != i18n.Chinese || app.Language() != i18n.Chinese {
		t.Fatal("native explicit choice not persisted")
	}
	if checked(w.controls[idConsent]) || textOf(w.controls[idKey]) != "" {
		t.Fatal("language change retained ready authorization input")
	}
	if strings.Contains(w.notice, "SECRET") || w.view.Warning == "" {
		t.Fatal("unsafe/hidden save warning")
	}
	if !strings.Contains(textOf(w.controls[idConsent]), "同意") {
		t.Fatal("native captions stayed English")
	}
}

func TestNativeQueuedActionCannotUndoChosenLanguage(t *testing.T) {
	w := reviewWindow(t)
	w.view.Language = i18n.English
	w.results <- update{kind: "cancel", view: client.Snapshot{Language: i18n.Chinese, State: "online"}}
	w.tick()
	if !strings.Contains(textOf(w.controls[idConsent]), "agree") {
		t.Fatal("queued action reverted chosen language")
	}
}

type localeNativeConnection struct {
	*slowConnection
	tasks []worker.LocalTaskView
}

func (c *localeNativeConnection) Snapshot() []worker.LocalTaskView { return c.tasks }

func TestNativeOnlineLanguageKeepsCancelTargetAndLongLogReading(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	c := &localeNativeConnection{slowConnection: &slowConnection{done: make(chan struct{}), release: make(chan struct{}), cancelStarted: make(chan string, 1)}, tasks: []worker.LocalTaskView{{Task: protocol.Task{ID: "one", State: protocol.TaskRunning}, Progress: events.Progress{Stage: "installing"}}, {Task: protocol.Task{ID: "two", State: protocol.Queued}, Progress: events.Progress{Stage: "queued"}}}}
	close(c.release)
	for i := 0; i < 200; i++ {
		c.tasks[1].Logs = append(c.tasks[1].Logs, protocol.LogEntry{Message: fmt.Sprintf("raw log %03d %s", i, strings.Repeat("x", 80))})
	}
	b := protocol.PairingBundle{ProtocolVersion: protocol.Version, ControllerName: "Inert controller", ControllerURL: "http://test.invalid", SessionID: "sess_locale", PairingToken: "inert-pairing-token", ExpiresAt: time.Now().Add(time.Hour), AuthorizationSeconds: 3600, Scope: protocol.Scope{AllowedTasks: []protocol.TaskType{protocol.InspectSystem}}}
	b.CatalogDigest = protocol.Digest(b.Catalog)
	data, _ := json.Marshal(b)
	path := filepath.Join(t.TempDir(), "pairing.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	app := client.New(context.Background(), client.Options{PairingPath: path, Connect: func(context.Context, protocol.PairingBundle, []byte) (client.Connection, error) { return c, nil }})
	defer func() { app.Disconnect("local_closed"); app.Wait() }()
	if err := app.ConsentAndConnect([]byte("tskey-auth-inert")); err != nil {
		t.Fatal(err)
	}
	awaitNative(t, "inert connection not online", func() bool { return app.Snapshot().State == "online" })
	w, err := newWindow(app)
	if err != nil {
		t.Fatal(err)
	}
	defer w.dispose()
	w.view = app.Snapshot()
	w.selected = "two"
	w.explicitSelection = true
	w.logs = true
	w.history = true
	w.refresh()
	h := w.controls[idLogs]
	sendMessage.Call(h, 0xB1, 100, 120)
	up := int32(-10000)
	sendMessage.Call(h, 0xB6, 0, uintptr(up))
	sendMessage.Call(h, 0xB6, 0, 40)
	before, _, _ := sendMessage.Call(h, 0xCE, 0, 0)
	selection, _, _ := sendMessage.Call(h, 0xB0, 0, 0)
	sendMessage.Call(w.controls[idLanguage], 0x14e, 1, 0)
	w.command(idLanguage)
	awaitNative(t, "English native snapshot missing", func() bool { w.tick(); return !w.languagePending && w.view.Language == i18n.English })
	after, _, _ := sendMessage.Call(h, 0xCE, 0, 0)
	selAfter, _, _ := sendMessage.Call(h, 0xB0, 0, 0)
	if before == 0 || after != before || selection != selAfter || w.selected != "two" || !w.logs || !w.history {
		t.Fatal("locale change moved selected task/log reading state")
	}
	if !strings.Contains(textOf(h), "Waiting to run") || !strings.Contains(textOf(h), "raw log 199") {
		t.Fatal("English log wrapper or original log missing")
	}
	w.command(idCancel)
	select {
	case id := <-c.cancelStarted:
		if id != "two" {
			t.Fatalf("cancel targeted %s", id)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel was not dispatched")
	}
}

func TestNativeEnglishConsentAndActionsFitMinimumDPI(t *testing.T) {
	w := reviewWindow(t)
	w.view.Language = i18n.English
	for _, dpi := range []int{96, 144, 192} {
		w.dpi = dpi
		w.setFonts()
		w.refresh()
		layout := makeLayout(scale(704, dpi), scale(521, dpi), dpi, false, false)
		dc, _, _ := user32.NewProc("GetDC").Call(w.hwnd)
		selectObject.Call(dc, w.font)
		for _, id := range []int{idConsent, idBrowse, idLoad, idConnect, idExit} {
			bounds := layout[id]
			r := winRect{Right: int32(bounds.W - scale(24, dpi))}
			flags := uintptr(0x400 | 0x20)
			if id == idConsent {
				flags = 0x400 | 0x10
			}
			user32.NewProc("DrawTextW").Call(dc, ptr(u16(textOf(w.controls[id]))), ^uintptr(0), uintptr(unsafe.Pointer(&r)), flags)
			if int(r.Bottom) > bounds.H || int(r.Right) > bounds.W-scale(12, dpi) {
				t.Fatalf("DPI %d caption %d cannot fit: %+v in %+v", dpi, id, r, bounds)
			}
		}
		user32.NewProc("ReleaseDC").Call(w.hwnd, dc)
	}
}

func TestNativeWarningsAndErrorsRemainAccessibleInEveryState(t *testing.T) {
	w := reviewWindow(t)
	for _, state := range []string{"idle", "ready", "connecting", "online", "disconnected", "error"} {
		w.view = client.Snapshot{Language: i18n.English, State: state, Warning: "settings warning", Error: "operation error"}
		w.refresh()
		text := textOf(w.controls[idMessages])
		if !strings.Contains(text, "settings warning") || !strings.Contains(text, "operation error") {
			t.Fatalf("state %s hides warning/error", state)
		}
	}
}
