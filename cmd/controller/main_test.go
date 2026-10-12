package main

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"remoteops/internal/i18n"
	"remoteops/pkg/protocol"
	"strings"
	"testing"
)

func TestLocalizedOptionsBeforeStartup(t *testing.T) {
	t.Setenv("REMOTEOPS_LANG", "en-US")
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"language":"zh-CN"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	opts, err := parseOptions([]string{"--help", "--config", path, "--lang=zh-CN"}, &output, dir)
	if !errors.Is(err, flag.ErrHelp) || opts.settings.Language != i18n.Chinese || !strings.Contains(output.String(), "管理监听地址") {
		t.Fatalf("help: %s, %v", output.String(), err)
	}
	output.Reset()
	opts, err = parseOptions([]string{"--config", path, "--help"}, &output, dir)
	if !errors.Is(err, flag.ErrHelp) || opts.settings.Language != i18n.English || !strings.Contains(output.String(), "management listen address") {
		t.Fatalf("help: %s, %v", output.String(), err)
	}
	if err := os.WriteFile(path, []byte(`{"language":"bad"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = parseOptions([]string{"--lang=en-US", "--config", path}, &output, dir); err == nil {
		t.Fatal("invalid overridden config accepted")
	}
	output.Reset()
	if _, err = parseOptions([]string{"--unknown=private-secret"}, &output, dir); err == nil || strings.Contains(err.Error(), "private-secret") || strings.Contains(err.Error(), "flag provided") {
		t.Fatalf("unsafe flag error: %v", err)
	}
	if _, err = os.Stat(filepath.Join(dir, "RemoteOps", "controller-settings.json")); !os.IsNotExist(err) {
		t.Fatalf("parse saved preferences: %v", err)
	}
}

func TestControllerSavedLanguageAndRecoverableImplicitSettings(t *testing.T) {
	t.Setenv("REMOTEOPS_LANG", "en-US")
	if err := os.Unsetenv("REMOTEOPS_LANG"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "RemoteOps", "controller-settings.json")
	if err := i18n.Save(path, i18n.English); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	opts, err := parseOptions(nil, &output, dir)
	if err != nil || opts.settings.Language != i18n.English || opts.settings.Path != path {
		t.Fatalf("saved preference: %+v %v", opts.settings, err)
	}
	explicit := filepath.Join(dir, "explicit.json")
	if err := i18n.Save(explicit, i18n.Chinese); err != nil {
		t.Fatal(err)
	}
	opts, err = parseOptions([]string{"--config", explicit}, &output, dir)
	if err != nil || opts.settings.Language != i18n.Chinese || opts.settings.Path != explicit {
		t.Fatalf("explicit preference: %+v %v", opts.settings, err)
	}
	if err := os.WriteFile(path, []byte("private-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	opts, err = parseOptions(nil, &output, dir)
	if err != nil || opts.settings.Language != i18n.Chinese || opts.settings.Warning == nil {
		t.Fatalf("recoverable preference: %+v %v", opts.settings, err)
	}
	for _, args := range [][]string{{"--lang="}, {"--lang=fr-FR"}, {"--config="}, {"--listen"}, {"positional-secret"}} {
		if _, err := parseOptions(args, &output, dir); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("invalid inputs accepted/leaked: %v %v", args, err)
		}
	}
	t.Setenv("REMOTEOPS_LANG", "")
	if _, err := parseOptions([]string{"--lang=en-US"}, &output, dir); err == nil {
		t.Fatal("overridden empty environment accepted")
	}
}

func TestListenerSafety(t *testing.T) {
	for _, c := range []startup{{Management: "0.0.0.0:8080", Worker: "100.64.0.1:9080"}, {Management: "127.0.0.1:8080"}, {Management: "127.0.0.1:8080", Worker: "0.0.0.0:9080"}, {Management: "127.0.0.1:8080", Worker: "127.0.0.1:9080"}} {
		if err := c.validate(); err == nil {
			t.Fatalf("unsafe listener accepted: %+v", c)
		}
	}
	if err := (startup{Management: "127.0.0.1:8080", Worker: "100.64.0.1:9080"}).validate(); err != nil {
		t.Fatal(err)
	}
	if err := (startup{Management: "0.0.0.0:8080", Worker: "100.64.0.1:9080", TLSCert: "cert", TLSKey: "key"}).validate(); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyDatabaseInputIsLocalizedBeforeStartup(t *testing.T) {
	for _, language := range []i18n.Language{i18n.Chinese, i18n.English} {
		t.Setenv("REMOTEOPS_LANG", string(language))
		var output bytes.Buffer
		_, err := parseOptions([]string{"--db="}, &output, t.TempDir())
		if err == nil || err.Error() != i18n.Text(language, "controller.error.database_path") {
			t.Fatalf("unlocalized or accepted empty db: %v", err)
		}
	}
}

func TestControllerOpaqueOperandsAndEndOfOptions(t *testing.T) {
	t.Setenv("REMOTEOPS_LANG", "en-US")
	var output bytes.Buffer
	dir := t.TempDir()
	opts, err := parseOptions([]string{"--name", "--config=private-secret", "--lang=zh-CN"}, &output, dir)
	if err != nil || opts.name != "--config=private-secret" || opts.settings.Language != i18n.Chinese {
		t.Fatalf("operand/locale: %+v %v", opts, err)
	}
	opts, err = parseOptions([]string{"--", "--lang=bad"}, &output, dir)
	if err == nil || opts.settings.Language != i18n.English || err.Error() != i18n.Text(i18n.English, "native.cli.positional") {
		t.Fatalf("end of options: %+v %v", opts, err)
	}
}

func TestCatalogInputValidationIsLocalizedAndExternalErrorsStayRedacted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(path, []byte(`[{"id":"private-secret","version":"latest","installer":"winget"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := loadCatalog(path)
	for _, language := range []i18n.Language{i18n.Chinese, i18n.English} {
		got := formatStartupError(err, language)
		if got == nil || got.Error() != i18n.Text(language, "controller.error.catalog_invalid") || strings.Contains(got.Error(), "private-secret") {
			t.Fatalf("catalog error: %v", got)
		}
	}
	valid := []protocol.CatalogEntry{{ID: "fixture", Version: "1.0.0", Installer: "winget", WingetID: "Vendor.Fixture", Source: "winget"}}
	if err := validateCatalog(valid); err != nil {
		t.Fatal(err)
	}
	external := formatStartupError(errors.New("OS details token=private-secret\x1b"), i18n.Chinese)
	if !strings.Contains(external.Error(), "OS details") || strings.Contains(external.Error(), "private-secret") || strings.Contains(external.Error(), "\x1b") {
		t.Fatalf("external details: %v", external)
	}
}

func TestGeneratedMCPTokenIsProtectedAndDistinct(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "mcp.token")
	token, err := agentCredential("", path, "operator_token_0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != token || len(token) < 32 {
		t.Fatal("token file did not contain generated credential")
	}
	if _, err = agentCredential("operator_token_0123456789abcdef0123456789abcdef", path, "operator_token_0123456789abcdef0123456789abcdef"); err == nil {
		t.Fatal("same operator and MCP credential accepted")
	}
}
