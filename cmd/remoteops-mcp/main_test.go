package main

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

func TestMCPHelpAndCredentialRefusalKeepStdoutProtocolOnly(t *testing.T) {
	t.Setenv("REMOTEOPS_LANG", "en-US")
	t.Setenv("REMOTEOPS_TOKEN", "")
	dir := t.TempDir()
	var output, diagnostic bytes.Buffer
	err := run([]string{"--help"}, strings.NewReader(""), &output, &diagnostic, dir)
	if err != nil || output.Len() != 0 || !strings.Contains(diagnostic.String(), "local Controller URL") {
		t.Fatalf("help stdout=%q stderr=%q err=%v", output.String(), diagnostic.String(), err)
	}
	diagnostic.Reset()
	err = run(nil, strings.NewReader(""), &output, &diagnostic, dir)
	if err == nil || !strings.Contains(err.Error(), "REMOTEOPS_TOKEN") || output.Len() != 0 {
		t.Fatalf("credentials stdout=%q err=%v", output.String(), err)
	}
	t.Setenv("REMOTEOPS_TOKEN", "test-token-only-for-inert-input")
	input := "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\"}\n"
	if err = run(nil, strings.NewReader(input), &output, &diagnostic, dir); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "List currently authorized devices") || len(strings.Split(strings.TrimSpace(output.String()), "\n")) != 2 {
		t.Fatalf("protocol output: %s", output.String())
	}
}

func TestMCPExplicitSettingsAreValidatedEvenWhenOverridden(t *testing.T) {
	t.Setenv("REMOTEOPS_LANG", "en-US")
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"language":"zh-CN"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	opts, err := parseOptions([]string{"--help", "--lang=zh-CN", "--config", path}, &output, dir)
	if !errors.Is(err, flag.ErrHelp) || opts.settings.Language != i18n.Chinese || !strings.Contains(output.String(), "本机 Controller 地址") {
		t.Fatalf("help: %s %v", output.String(), err)
	}
	if err := os.WriteFile(path, []byte(`{"language":"bad"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = parseOptions([]string{"--lang=en-US", "--config", path}, &output, dir); err == nil {
		t.Fatal("invalid overridden config accepted")
	}
	t.Setenv("REMOTEOPS_LANG", "")
	if _, err = parseOptions([]string{"--lang=en-US"}, &output, dir); err == nil {
		t.Fatal("present empty environment accepted")
	}
}

func TestMCPSavedPreferenceAndCorruptSettingsWarningStayLocal(t *testing.T) {
	t.Setenv("REMOTEOPS_LANG", "en-US")
	if err := os.Unsetenv("REMOTEOPS_LANG"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REMOTEOPS_TOKEN", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "RemoteOps", "mcp-settings.json")
	if err := i18n.Save(path, i18n.English); err != nil {
		t.Fatal(err)
	}
	var output, diagnostic bytes.Buffer
	opts, err := parseOptions(nil, &diagnostic, dir)
	if err != nil || opts.settings.Language != i18n.English || opts.settings.Path != path {
		t.Fatalf("saved preference: %+v %v", opts.settings, err)
	}
	explicit := filepath.Join(dir, "explicit.json")
	if err := i18n.Save(explicit, i18n.Chinese); err != nil {
		t.Fatal(err)
	}
	opts, err = parseOptions([]string{"--config", explicit}, &diagnostic, dir)
	if err != nil || opts.settings.Language != i18n.Chinese || opts.settings.Path != explicit {
		t.Fatalf("explicit preference: %+v %v", opts.settings, err)
	}
	if err := os.WriteFile(path, []byte("private-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	err = run(nil, strings.NewReader(""), &output, &diagnostic, dir)
	if err == nil || !strings.Contains(err.Error(), "REMOTEOPS_TOKEN") || output.Len() != 0 || !strings.Contains(diagnostic.String(), "语言设置") || strings.Contains(diagnostic.String(), "private-secret") {
		t.Fatalf("warning stdout=%q stderr=%q err=%v", output.String(), diagnostic.String(), err)
	}
	for _, args := range [][]string{{"--lang="}, {"--config="}, {"--token-file"}, {"--unknown=private-secret"}, {"positional-secret"}} {
		if _, err := parseOptions(args, &diagnostic, dir); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("invalid inputs accepted/leaked: %v %v", args, err)
		}
	}
	if _, err = os.Stat(filepath.Join(dir, "RemoteOps", "controller-settings.json")); !os.IsNotExist(err) {
		t.Fatalf("MCP touched Controller settings: %v", err)
	}
}

func TestMCPOpaqueOperandsAndEndOfOptions(t *testing.T) {
	t.Setenv("REMOTEOPS_LANG", "en-US")
	var diagnostic bytes.Buffer
	dir := t.TempDir()
	opts, err := parseOptions([]string{"--token-file", "--config=private-secret", "--controller", "--lang=bad"}, &diagnostic, dir)
	if err != nil || opts.tokenFile != "--config=private-secret" || opts.base != "--lang=bad" || opts.settings.Language != i18n.English {
		t.Fatalf("opaque operands: %+v %v", opts, err)
	}
	opts, err = parseOptions([]string{"--", "--config=private-secret"}, &diagnostic, dir)
	if err == nil || opts.settings.Language != i18n.English || err.Error() != i18n.Text(i18n.English, "native.cli.positional") {
		t.Fatalf("end of options: %+v %v", opts, err)
	}
}
