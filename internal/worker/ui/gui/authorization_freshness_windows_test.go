//go:build windows

package gui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"remoteops/internal/i18n"
	"remoteops/internal/worker/client"
	"remoteops/pkg/protocol"
	"runtime"
	"strings"
	"testing"
	"time"
)

func freshnessPairing(t *testing.T, name string, admin bool) string {
	t.Helper()
	b := protocol.PairingBundle{ProtocolVersion: protocol.Version, ControllerName: name, ControllerURL: "http://inert.invalid", SessionID: "sess_" + name, PairingToken: "inert-token-" + name, ExpiresAt: time.Now().Add(time.Hour), AuthorizationSeconds: 3600, Scope: protocol.Scope{AllowedTasks: []protocol.TaskType{protocol.InspectSystem}, AllowAdmin: admin}}
	b.CatalogDigest = protocol.Digest(b.Catalog)
	body, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "pairing.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func freshnessWindow(t *testing.T) (*window, *client.App) {
	t.Helper()
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
	app := client.New(context.Background(), client.Options{Language: i18n.English, PairingPath: freshnessPairing(t, "Old", false)})
	w, err := newWindow(app)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.dispose)
	initial := <-w.results
	w.view = initial.view
	// Test the completion boundary deterministically; do not schedule timer polls.
	w.polling = true
	w.refresh()
	return w, app
}

func controlEnabled(w *window, id int) bool {
	v, _, _ := user32.NewProc("IsWindowEnabled").Call(w.controls[id])
	return v != 0
}

func TestNativeLateAuthorizationCannotReplaceLoadedPairing(t *testing.T) {
	for _, kind := range []string{"language", "snapshot"} {
		for _, timing := range []string{"before", "during"} {
			t.Run(kind+"/"+timing, func(t *testing.T) {
				w, app := freshnessWindow(t)
				old := app.Snapshot()
				stale := update{generation: w.generation, kind: kind, view: old}
				release := make(chan struct{})
				newPath := freshnessPairing(t, "New", true)
				w.start("load", func() error { <-release; return app.LoadPairing(newPath) })
				if timing == "during" {
					stale.generation = w.generation
				}
				w.languagePending = true
				close(release)
				loaded := <-w.results
				// Hold only the off-thread locale collector while we replay the late result.
				w.languageSave.Lock()
				locked := true
				defer func() {
					if locked {
						w.languageSave.Unlock()
					}
				}()
				w.results <- loaded
				w.tick()
				w.results <- stale
				w.tick()
				sendMessage.Call(w.controls[idConsent], 0xf1, 1, 0)
				w.refresh()
				if w.view.Controller != "New" || !w.languagePending || controlEnabled(w, idConsent) || controlEnabled(w, idConnect) {
					t.Fatalf("late %s %s load restored authorization: controller=%q pending=%v consent=%v connect=%v", kind, timing, w.view.Controller, w.languagePending, controlEnabled(w, idConsent), controlEnabled(w, idConnect))
				}
				sendMessage.Call(w.controls[idConsent], 0xf1, 0, 0)
				w.languageSave.Unlock()
				locked = false
				awaitNative(t, "new localized authorization missing", func() bool { w.tick(); return !w.languagePending })
				if w.view.Controller != "New" || !strings.Contains(strings.Join(w.view.Scope, "|"), "Administrator privileges may be requested") || !controlEnabled(w, idConsent) || controlEnabled(w, idConnect) {
					t.Fatal("current pairing/scope not required before explicit consent")
				}
				sendMessage.Call(w.controls[idConsent], 0xf1, 1, 0)
				w.refresh()
				if !controlEnabled(w, idConnect) {
					t.Fatal("current scoped consent cannot connect")
				}
			})
		}
	}
}

func TestNativeLoadCollectsSelectedLocaleBeforeConsent(t *testing.T) {
	w, app := freshnessWindow(t)
	if err := app.SetLanguage(i18n.Chinese); err != nil {
		t.Fatal(err)
	}
	path := freshnessPairing(t, "New", true)
	w.start("load", func() error { return app.LoadPairing(path) })
	loaded := <-w.results
	w.languageSave.Lock()
	w.results <- loaded
	w.tick()
	if w.view.Controller != "New" || w.view.Language != i18n.English || !w.languagePending || len(w.view.Scope) != 0 || controlEnabled(w, idConsent) {
		w.languageSave.Unlock()
		t.Fatal("load did not invalidate scope in the previous locale")
	}
	if err := app.SetLanguage(i18n.English); err != nil {
		w.languageSave.Unlock()
		t.Fatal(err)
	}
	w.languageSave.Unlock()
	awaitNative(t, "selected locale scope missing", func() bool { w.tick(); return !w.languagePending })
	if !strings.Contains(textOf(w.controls[idScope]), "Administrator privileges may be requested") || !controlEnabled(w, idConsent) {
		t.Fatal("fresh selected-locale authorization missing")
	}
}

func TestNativeOldSameLocaleRequestCannotCompleteLatestChoice(t *testing.T) {
	for _, kind := range []string{"language", "snapshot"} {
		t.Run(kind, func(t *testing.T) {
			w, app := freshnessWindow(t)
			if err := app.SetLanguage(i18n.Chinese); err != nil {
				t.Fatal(err)
			}
			w.view = app.Snapshot()
			w.chooseLanguage(i18n.English)
			stale := <-w.results
			stale.kind = kind
			w.chooseLanguage(i18n.Chinese)
			<-w.results
			w.languageSave.Lock()
			w.chooseLanguage(i18n.English)
			w.results <- stale
			w.tick()
			pending := w.languagePending
			w.languageSave.Unlock()
			if !pending || controlEnabled(w, idConsent) {
				t.Fatal("older English result completed the newest English request")
			}
			awaitNative(t, "latest locale completion missing", func() bool { w.tick(); return !w.languagePending })

		})
	}
}

func TestNativeLanguageCompletionPreservesNewerLifecycle(t *testing.T) {
	w, _ := freshnessWindow(t)
	stale := w.view
	w.view.State = "online"
	w.results <- update{generation: w.generation, kind: "language", view: stale}
	w.tick()
	if w.view.State != "online" {
		t.Fatal("locale completion replaced current lifecycle state")
	}
}
