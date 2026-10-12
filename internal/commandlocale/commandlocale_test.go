package commandlocale

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"remoteops/internal/i18n"
	"strings"
	"testing"
)

func TestResolutionPreservesOpaqueOperandsAndStopsAtEndOfOptions(t *testing.T) {
	t.Setenv("REMOTEOPS_LANG", "en-US")
	opts := Options{Program: "mcp", UserConfigDir: t.TempDir(), ValueFlags: []string{"controller", "token-file"}}
	for _, args := range [][]string{{"--token-file", "--lang=bad"}, {"--controller", "--config=private-secret"}, {"--", "--lang=bad"}, {"positional", "--lang=bad"}} {
		settings, err := Resolve(args, opts)
		if err != nil || settings.Language != i18n.English || settings.Path != filepath.Join(opts.UserConfigDir, "RemoteOps", "mcp-settings.json") {
			t.Fatalf("opaque/end flags %v => %+v %v", args, settings, err)
		}
	}
	settings, err := Resolve([]string{"--help", "--lang=zh-CN"}, opts)
	if err != nil || settings.Language != i18n.Chinese {
		t.Fatalf("language after help: %+v %v", settings, err)
	}
	t.Setenv("REMOTEOPS_LANG", "")
	if _, err := Resolve([]string{"--lang=en-US"}, opts); err == nil {
		t.Fatal("present empty environment was not validated")
	}
}

func TestExplicitFileValidationAndRecoverableSavedPreference(t *testing.T) {
	t.Setenv("REMOTEOPS_LANG", "en-US")
	dir := t.TempDir()
	opts := Options{Program: "controller", UserConfigDir: dir}
	path := filepath.Join(dir, "invalid.json")
	if err := os.WriteFile(path, []byte(`{"language":"private-secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve([]string{"--lang=en-US", "--config", path}, opts)
	if err == nil || strings.Contains(err.Error(), "private-secret") || !strings.Contains(err.Error(), "Invalid language settings") {
		t.Fatalf("overridden explicit config: %v", err)
	}
	saved := filepath.Join(dir, "RemoteOps", "controller-settings.json")
	if err := i18n.Save(saved, i18n.English); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(saved, []byte("private-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	settings, err := Resolve(nil, opts)
	if err != nil || settings.Language != i18n.English || settings.Warning == nil {
		t.Fatalf("saved warning/override: %+v %v", settings, err)
	}
}

func TestLocalizedHelpAndSafeArgumentCategories(t *testing.T) {
	for _, language := range []i18n.Language{i18n.Chinese, i18n.English} {
		var output bytes.Buffer
		fs := NewFlagSet("fixture", &output, language, "mcp.cli.usage")
		fs.String("controller", "http://127.0.0.1:8080", i18n.Text(language, "mcp.cli.controller"))
		if err := Parse(fs, []string{"--help"}, language); !errors.Is(err, flag.ErrHelp) {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "--lang") || !strings.Contains(output.String(), i18n.Text(language, "mcp.cli.controller")) || !strings.Contains(output.String(), i18n.Text(language, "native.cli.default", "http://127.0.0.1:8080")) {
			t.Fatalf("help: %s", output.String())
		}
		for _, args := range [][]string{{"--unknown=private-secret"}, {"--controller"}, {"private-secret"}} {
			err := Parse(fs, args, language)
			if err == nil || strings.Contains(err.Error(), "private-secret") || strings.Contains(err.Error(), "flag provided") {
				t.Fatalf("unsafe parser error: %v", err)
			}
		}
	}
}
