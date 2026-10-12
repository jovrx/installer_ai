package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"remoteops/internal/i18n"
	"remoteops/pkg/protocol"
	"strings"
	"testing"
	"time"
)

func TestCLIOptions(t *testing.T) {
	isolateWorkerSettings(t)
	for _, tt := range []struct {
		name                string
		args                []string
		ui, pairing, helper string
		bad                 bool
	}{
		{"default tui", nil, "tui", "", "", false},
		{"legacy pairing", []string{"--pairing", "trusted bundle.json"}, "tui", "trusted bundle.json", "", false},
		{"gui", []string{"--ui=gui"}, "gui", "", "", false},
		{"tui", []string{"--ui=tui"}, "tui", "", "", false},
		{"plain", []string{"--ui=plain", "--pairing=p.json"}, "plain", "p.json", "", false},
		{"helper", []string{"--elevated-helper=req.json"}, "tui", "", "req.json", false},
		{"empty helper", []string{"--elevated-helper="}, "tui", "", "", true},
		{"helper positional", []string{"--elevated-helper=req.json", "extra"}, "tui", "", "", true},
		{"missing pairing value", []string{"--pairing"}, "tui", "", "", true},
		{"empty UI", []string{"--ui="}, "", "", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := parseOptions(tt.args, io.Discard)
			if (err != nil) != tt.bad {
				t.Fatalf("parse error %v; want error=%t", err, tt.bad)
			}
			if !tt.bad && (opts.ui != tt.ui || opts.pairingPath != tt.pairing || opts.helperPath != tt.helper) {
				t.Fatalf("options=%+v", opts)
			}
		})
	}
	old := defaultUI
	defaultUI = "gui"
	defer func() { defaultUI = old }()
	opts, err := parseOptions(nil, io.Discard)
	if err != nil || opts.ui != "gui" {
		t.Fatalf("GUI build default: %+v, %v", opts, err)
	}
	opts, err = parseOptions([]string{"--elevated-helper=req.json"}, io.Discard)
	if err != nil || !opts.helper {
		t.Fatalf("GUI helper dispatch: %+v, %v", opts, err)
	}
	opts, err = parseOptions([]string{"--elevated-helper=req.json", "--unknown"}, io.Discard)
	if err == nil || !opts.helper {
		t.Fatalf("malformed helper must retain non-UI error routing: %+v, %v", opts, err)
	}
}

// Exercise the real flag/entrypoint boundary without opening a UI or network.
func TestCLIParameterRegression(t *testing.T) {
	if os.Getenv("REMOTEOPS_TEST_ENTRYPOINT") == "1" {
		for i, arg := range os.Args {
			if arg == "--" {
				os.Args = append([]string{os.Args[0]}, os.Args[i+1:]...)
				main()
				os.Exit(0)
			}
		}
	}
	isolateWorkerSettings(t)
	for _, tt := range []struct {
		name string
		args []string
		want string
		ok   bool
	}{
		{"help lists UI", []string{"--help"}, "gui|tui|plain", true},
		{"unsupported UI", []string{"--ui=browser"}, "不支持的界面", false},
		{"helper UI conflict", []string{"--elevated-helper=req.json", "--ui=tui"}, "辅助进程不能", false},
		{"helper pairing conflict", []string{"--elevated-helper=req.json", "--pairing=p.json"}, "辅助进程不能", false},
		{"helper explicit empty pairing conflict", []string{"--elevated-helper=req.json", "--pairing="}, "辅助进程不能", false},
		{"reject positional", []string{"p.json"}, "不接受位置参数", false},
		{"reject key flag", []string{"--auth-key=tskey-auth-fake"}, "flag provided but not defined", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			args := append([]string{"-test.run=^TestCLIParameterRegression$", "--"}, tt.args...)
			cmd := exec.CommandContext(ctx, os.Args[0], args...)
			cmd.Env = append(os.Environ(), "REMOTEOPS_TEST_ENTRYPOINT=1")
			out, err := cmd.CombinedOutput()
			if (err == nil) != tt.ok || !strings.Contains(string(out), tt.want) {
				t.Fatalf("exit=%v output=%s; want success=%t and %q", err, out, tt.ok, tt.want)
			}
			if strings.Contains(string(out), "tskey-auth-fake") {
				t.Fatal("argument secret was echoed")
			}
		})
	}
}

func isolateWorkerSettings(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	old, exists := os.LookupEnv("REMOTEOPS_LANG")
	_ = os.Unsetenv("REMOTEOPS_LANG")
	t.Cleanup(func() {
		if exists {
			_ = os.Setenv("REMOTEOPS_LANG", old)
		} else {
			_ = os.Unsetenv("REMOTEOPS_LANG")
		}
	})
	return dir
}

func TestWorkerStartupLanguageAndLocalizedHelp(t *testing.T) {
	dir := isolateWorkerSettings(t)
	config := filepath.Join(dir, "explicit.json")
	if err := os.WriteFile(config, []byte(`{"language":"en-US"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--help", "--lang=en-US"}, {"--lang=en-US", "--help"}, {"--help", "--config", config}} {
		var out bytes.Buffer
		opts, err := parseOptions(args, &out)
		if !errors.Is(err, flag.ErrHelp) || opts.settings.Language != i18n.English || !strings.Contains(out.String(), "Interface: gui|tui|plain") || !strings.Contains(out.String(), "--lang") {
			t.Fatalf("%v: %+v %v %s", args, opts, err, out.String())
		}
	}
	opts, err := parseOptions([]string{"--config", config, "--lang=zh-CN"}, io.Discard)
	if err != nil || opts.settings.Language != i18n.Chinese || opts.settings.Path != config {
		t.Fatalf("%+v %v", opts, err)
	}
	data, _ := os.ReadFile(config)
	if string(data) != `{"language":"en-US"}` {
		t.Fatal("startup silently saved override")
	}
	_ = os.WriteFile(config, []byte(`{"language":"invalid"}`), 0600)
	if _, err := parseOptions([]string{"--lang=en-US", "--config", config}, io.Discard); err == nil || !strings.Contains(err.Error(), "settings") {
		t.Fatal(err)
	}
	if _, err := parseOptions([]string{"--lang="}, io.Discard); err == nil {
		t.Fatal("empty explicit language accepted")
	}
	if _, err := parseOptions([]string{"--config="}, io.Discard); err == nil {
		t.Fatal("empty explicit config accepted")
	}
	t.Setenv("REMOTEOPS_LANG", "")
	if _, err := parseOptions([]string{"--lang=en-US"}, io.Discard); err == nil {
		t.Fatal("invalid overridden environment accepted")
	}
}

func TestWorkerHelperBypassesInvalidLocaleAndSettings(t *testing.T) {
	isolateWorkerSettings(t)
	t.Setenv("REMOTEOPS_LANG", "invalid")
	opts, err := parseOptions([]string{"--elevated-helper=req.json"}, io.Discard)
	if err != nil || !opts.helper || opts.settings.Path != "" || opts.settings.Language != "" {
		t.Fatalf("%+v %v", opts, err)
	}
	for _, extra := range []string{"--lang=", "--config=", "--ui=", "--pairing=", "--help", "--unknown", ""} {
		for _, args := range [][]string{{"--elevated-helper=req.json", extra}, {extra, "--elevated-helper=req.json"}} {
			opts, err := parseOptions(args, io.Discard)
			if err == nil || !opts.helper || opts.settings.Path != "" {
				t.Fatalf("%v: %+v %v", args, opts, err)
			}
		}
	}
}

func TestWorkerImplicitCorruptionWarnsWithoutFailingStartup(t *testing.T) {
	dir := isolateWorkerSettings(t)
	path := filepath.Join(dir, "RemoteOps", "worker-settings.json")
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	_ = os.WriteFile(path, []byte(`{"language":"invalid"}`), 0600)
	opts, err := parseOptions(nil, io.Discard)
	if err != nil || opts.settings.Warning == nil || opts.settings.Language != i18n.Chinese || opts.settings.Path != path {
		t.Fatalf("%+v %v", opts, err)
	}
}

func TestWorkerPositionalTextCannotDispatchHelperOrSelectLanguage(t *testing.T) {
	isolateWorkerSettings(t)
	for _, arg := range []string{"elevated-helper=req.json", "lang=en-US", "config=missing.json"} {
		opts, err := parseOptions([]string{arg}, io.Discard)
		if err == nil || opts.helper || opts.settings.Language != i18n.Chinese || !strings.Contains(err.Error(), "不接受位置参数") {
			t.Fatalf("%q: %+v %v", arg, opts, err)
		}
	}
}

func TestLocalDisconnectRequiresTokenAndOrigin(t *testing.T) {
	calls := 0
	c := localControl{origin: "http://127.0.0.1:8765", token: "random-local-proof", pairing: protocol.PairingBundle{ControllerName: "Operator"}, stop: func() { calls++ }}
	for _, tt := range []struct {
		token, origin string
		status        int
	}{{"", "http://127.0.0.1:8765", 403}, {"random-local-proof", "http://evil.invalid", 403}, {"random-local-proof", "http://127.0.0.1:8765", 200}} {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8765/api/disconnect", strings.NewReader("{}"))
		r.Header.Set("Authorization", "Bearer "+tt.token)
		r.Header.Set("Origin", tt.origin)
		w := httptest.NewRecorder()
		c.handler().ServeHTTP(w, r)
		if w.Code != tt.status {
			t.Fatalf("status %d want %d", w.Code, tt.status)
		}
	}
	if calls != 1 {
		t.Fatal("unauthorized stop or valid disconnect not applied")
	}
}
