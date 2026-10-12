package client

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"remoteops/internal/i18n"
	"remoteops/internal/worker"
	"remoteops/internal/worker/events"
	"remoteops/pkg/protocol"
)

func TestEnglishInspectionResult(t *testing.T) {
	v := worker.LocalTaskView{Task: protocol.Task{State: protocol.Succeeded, Spec: protocol.TaskSpec{Type: protocol.InspectSystem}}}
	if got := TaskResultText(v, i18n.English); got != "System inspection completed" {
		t.Fatal(got)
	}
}

func TestEnglishPresentation(t *testing.T) {
	for _, tt := range []struct{ got, want string }{
		{StateText("ready", i18n.English), "Review and agree to the authorization"},
		{StageText("awaiting_uac", i18n.English), "Confirm in the Windows permission dialog to continue"},
		{TaskTitle(worker.LocalTaskView{Task: protocol.Task{Spec: protocol.TaskSpec{Type: protocol.InspectSystem}}}, i18n.English), "System inspection"},
		{ProgressText(events.Progress{Stage: "downloading", CompletedBytes: 1 << 20}, i18n.English), "Downloaded 1.0 MB"},
		{ErrorText(errPairingExpired, i18n.English), "The pairing file has expired. Ask the controller for a new file."},
	} {
		if tt.got != tt.want {
			t.Errorf("got %q; want %q", tt.got, tt.want)
		}
	}
	if got := ErrorText(errors.New("opaque-secret"), i18n.English); strings.Contains(got, "opaque-secret") || !strings.Contains(got, "Connection failed") {
		t.Fatal(got)
	}
}

func TestLanguageSwitchPreservesConsentConnectionAndSafeScope(t *testing.T) {
	b := pairingFixture(t)
	b.Catalog[0].Name = "Git " + b.PairingToken + "\x1b"
	b.CatalogDigest = protocol.Digest(b.Catalog)
	c := newConnection(b)
	c.tasks = []worker.LocalTaskView{{PackageName: b.Catalog[0].Name, Task: protocol.Task{ID: "stable-task", State: protocol.TaskRunning}, Progress: events.Progress{Stage: "installing"}}}
	var calls atomic.Int32
	a := New(context.Background(), Options{Connect: func(context.Context, protocol.PairingBundle, []byte) (Connection, error) { calls.Add(1); return c, nil }})
	defer func() { a.Disconnect("local_closed"); a.Wait() }()
	if err := a.LoadPairing(writePairing(t, b)); err != nil {
		t.Fatal(err)
	}
	if err := a.SetLanguage(i18n.English); err != nil {
		t.Fatal(err)
	}
	s := a.Snapshot()
	if s.State != "ready" || s.Language != i18n.English || s.Scope[0] != "Inspect the system and authorized software versions" || !strings.Contains(strings.Join(s.Scope, " "), "administrator") || calls.Load() != 0 {
		t.Fatal(s)
	}
	if err := a.ConsentAndConnect([]byte("tskey-auth-test")); err != nil {
		t.Fatal(err)
	}
	s = awaitState(t, a, "online")
	if !strings.Contains(s.Notice, "Installing") {
		t.Fatal(s)
	}
	if err := a.SetLanguage(i18n.Chinese); err != nil {
		t.Fatal(err)
	}
	if got := a.Snapshot(); got.Tasks[0].Task.ID != "stable-task" || !strings.Contains(got.Notice, "正在安装") {
		t.Fatal(got)
	}
	if err := a.SetLanguage(i18n.English); err != nil {
		t.Fatal(err)
	}
	c.Disconnect("expired")
	a.Wait()
	if err := a.SetLanguage(i18n.Chinese); err != nil {
		t.Fatal(err)
	}
	if got := a.Snapshot(); !strings.Contains(got.Notice, "授权已到期") || got.Tasks[0].Task.ID != "stable-task" {
		t.Fatal(got)
	}
	if err := a.SetLanguage(i18n.English); err != nil {
		t.Fatal(err)
	}
	s = a.Snapshot()
	if s.EndReason != "expired" || !strings.Contains(s.Notice, "Authorization has expired") || s.Scope[0] != "Inspect the system and authorized software versions" || calls.Load() != 1 {
		t.Fatal(s)
	}
	raw, _ := json.Marshal(s)
	if strings.Contains(string(raw), b.PairingToken) || strings.Contains(string(raw), "\\u001b") {
		t.Fatal("snapshot exposes credentials or controls")
	}
}

func TestLanguageSaveRunsUnlockedAndFailureRemainsRecoverable(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	a := New(context.Background(), Options{LanguageWarning: errors.New("startup-secret"), SaveLanguage: func(i18n.Language) error { close(entered); <-release; return errors.New("save-secret") }})
	if s := a.Snapshot(); s.Warning == "" || s.Error != "" {
		t.Fatal(s)
	}
	done := make(chan error, 1)
	go func() { done <- a.SetLanguage(i18n.English) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("save callback not invoked")
	}
	observed := make(chan Snapshot, 1)
	go func() { observed <- a.Snapshot() }()
	select {
	case s := <-observed:
		if s.Language != i18n.English {
			t.Fatal(s)
		}
	case <-time.After(time.Second):
		t.Fatal("save blocks snapshot")
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("save error lost")
	}
	s := a.Snapshot()
	if a.Language() != i18n.English || s.Warning != "Language settings could not be saved. Your current choice still applies." || s.Error != "" || strings.Contains(s.Warning, "secret") {
		t.Fatal(s)
	}
	if err := a.SetLanguage(i18n.Language("invalid")); err == nil || a.Language() != i18n.English {
		t.Fatal("invalid locale changes language")
	}
}

func TestLanguageSwitchReformatsClassifiedError(t *testing.T) {
	a := New(context.Background(), Options{})
	_ = a.LoadPairing(filepath.Join(t.TempDir(), "missing-pairing.json"))
	if err := a.SetLanguage(i18n.English); err != nil {
		t.Fatal(err)
	}
	if got := a.Snapshot().Error; got != "Cannot read the pairing file. Select an accessible pairing file." {
		t.Fatal(got)
	}
}

func TestLanguageSaveCallbackCanReenterLanguageSwitch(t *testing.T) {
	var a *App
	a = New(context.Background(), Options{SaveLanguage: func(language i18n.Language) error {
		if language == i18n.English {
			return a.SetLanguage(i18n.Chinese)
		}
		return nil
	}})
	done := make(chan error, 1)
	go func() { done <- a.SetLanguage(i18n.English) }()
	select {
	case err := <-done:
		if err != nil || a.Language() != i18n.Chinese {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("save callback runs under an app lock")
	}
}

func TestOverlappingLanguageSavesPersistLatestChoice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker-settings.json")
	entered, release := make(chan struct{}), make(chan struct{})
	var englishCalls atomic.Int32
	a := New(context.Background(), Options{SaveLanguage: func(language i18n.Language) error {
		if language == i18n.English && englishCalls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return i18n.Save(path, language)
	}})
	olderDone := make(chan error, 1)
	go func() { olderDone <- a.SetLanguage(i18n.English) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("older save never entered")
	}
	newerDone := make(chan error, 1)
	go func() { newerDone <- a.SetLanguage(i18n.Chinese) }()
	select {
	case err := <-newerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("older save blocked accepting and saving the newer choice")
	}
	close(release)
	select {
	case err := <-olderDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("older save did not finish")
	}
	settings, err := i18n.Resolve(i18n.SettingsOptions{Program: "worker", ConfigPath: path, ConfigSet: true})
	if err != nil || settings.Language != i18n.Chinese {
		t.Fatalf("latest Chinese choice was overwritten: persisted=%q err=%v", settings.Language, err)
	}
}
