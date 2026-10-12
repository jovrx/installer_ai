package i18n

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeSettings(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSavedLanguageReload(t *testing.T) {
	dir := t.TempDir()
	initial, err := Resolve(SettingsOptions{Program: "worker", UserConfigDir: dir})
	if err != nil || initial.Language != Chinese || initial.Warning != nil {
		t.Fatalf("initial: %+v %v", initial, err)
	}
	if err := Save(initial.Path, English); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(SettingsOptions{Program: "worker", UserConfigDir: dir})
	if err != nil || got.Language != English || got.Warning != nil {
		t.Fatalf("saved: %+v %v", got, err)
	}
}

func TestDefaultSettingsProgramPaths(t *testing.T) {
	dir := t.TempDir()
	for _, program := range []string{"worker", "controller", "mcp"} {
		got, err := Resolve(SettingsOptions{Program: program, UserConfigDir: dir})
		if err != nil || got.Path != filepath.Join(dir, "RemoteOps", program+"-settings.json") {
			t.Fatalf("%s: %+v %v", program, got, err)
		}
	}
	for _, program := range []string{"", "other", "../worker"} {
		if _, err := Resolve(SettingsOptions{Program: program, UserConfigDir: dir}); err == nil {
			t.Errorf("accepted program %q", program)
		}
	}
}

func TestResolvePriorityAndExplicitSavePath(t *testing.T) {
	for _, tc := range []struct {
		name, flag, env, config, saved string
		want                           Language
	}{
		{"default", "", "", "", "", Chinese},
		{"saved", "", "", "", "en-US", English},
		{"config", "", "", "zh-CN", "en-US", Chinese},
		{"environment", "", "en-US", "zh-CN", "zh-CN", English},
		{"flag", "zh-CN", "en-US", "en-US", "en-US", Chinese},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			opts := SettingsOptions{Program: "worker", UserConfigDir: dir, Language: tc.flag, LanguageSet: tc.flag != "", Environment: tc.env, EnvironmentSet: tc.env != ""}
			wantPath := filepath.Join(dir, "RemoteOps", "worker-settings.json")
			if tc.saved != "" {
				writeSettings(t, wantPath, `{"language":"`+tc.saved+`"}`)
			}
			if tc.config != "" {
				opts.ConfigSet = true
				opts.ConfigPath = filepath.Join(dir, "explicit.json")
				writeSettings(t, opts.ConfigPath, `{"language":"`+tc.config+`"}`)
				wantPath = opts.ConfigPath
			}
			got, err := Resolve(opts)
			if err != nil || got.Language != tc.want || got.Path != wantPath || got.Warning != nil {
				t.Fatalf("got %+v %v; want %s %s", got, err, tc.want, wantPath)
			}
			if opts.ConfigSet {
				if err := Save(got.Path, English); err != nil {
					t.Fatal(err)
				}
				got, err = Resolve(SettingsOptions{Program: "worker", UserConfigDir: dir, ConfigSet: true, ConfigPath: opts.ConfigPath})
				if err != nil || got.Language != English {
					t.Fatalf("explicit saved reload: %+v %v", got, err)
				}
			}
		})
	}
}

func TestResolveRejectsAllInvalidExplicitInputsEvenOverridden(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options SettingsOptions
	}{
		{"empty flag", SettingsOptions{LanguageSet: true}},
		{"bad flag", SettingsOptions{LanguageSet: true, Language: "fr-FR"}},
		{"empty env overridden", SettingsOptions{LanguageSet: true, Language: "en-US", EnvironmentSet: true}},
		{"bad env overridden", SettingsOptions{LanguageSet: true, Language: "en-US", EnvironmentSet: true, Environment: "invalid"}},
		{"empty config overridden", SettingsOptions{LanguageSet: true, Language: "en-US", ConfigSet: true}},
		{"missing config overridden", SettingsOptions{LanguageSet: true, Language: "en-US", ConfigSet: true, ConfigPath: "missing.json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.options
			opts.Program = "worker"
			opts.UserConfigDir = t.TempDir()
			if opts.ConfigPath != "" {
				opts.ConfigPath = filepath.Join(opts.UserConfigDir, opts.ConfigPath)
			}
			if _, err := Resolve(opts); err == nil {
				t.Fatal("invalid explicit input accepted")
			}
		})
	}
}

func TestResolveRejectsStrictInvalidExplicitJSONEvenOverridden(t *testing.T) {
	for _, body := range []string{
		``, `{`, `null`, `[]`, `{}`, `{"language":null}`, `{"language":23}`, `{"language":""}`, `{"language":"fr-FR"}`,
		`{"Language":"en-US"}`, `{"language":"en-US","auth_key":"secret"}`,
		`{"language":"zh-CN","language":"en-US"}`, `{"language":"en-US"} {}`, `{"language":"en-US"} garbage`,
		`{"language":"en-US"}` + strings.Repeat(" ", 4096),
	} {
		t.Run(body[:min(len(body), 60)], func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "explicit.json")
			writeSettings(t, path, body)
			_, err := Resolve(SettingsOptions{Program: "worker", UserConfigDir: dir, LanguageSet: true, Language: "en-US", ConfigSet: true, ConfigPath: path})
			if err == nil {
				t.Fatal("invalid JSON accepted")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("error exposed configuration body")
			}
		})
	}
}

func TestImplicitInvalidSettingsWarnAndRecover(t *testing.T) {
	for _, fixture := range []string{"corrupt", "unsupported", "directory"} {
		t.Run(fixture, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "RemoteOps", "worker-settings.json")
			if fixture == "directory" {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				body := "bad JSON"
				if fixture == "unsupported" {
					body = `{"language":"fr-FR"}`
				}
				writeSettings(t, path, body)
			}
			got, err := Resolve(SettingsOptions{Program: "worker", UserConfigDir: dir})
			if err != nil || got.Language != Chinese || got.Warning == nil || got.Path != path {
				t.Fatalf("recover: %+v %v", got, err)
			}
			got, err = Resolve(SettingsOptions{Program: "worker", UserConfigDir: dir, LanguageSet: true, Language: "en-US"})
			if err != nil || got.Language != English || got.Warning == nil {
				t.Fatalf("override: %+v %v", got, err)
			}
		})
	}
}

func TestUnsetInputsIgnoredAndExplicitConfigDoesNotReadSavedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "explicit.json")
	writeSettings(t, path, `{"language":"en-US"}`)
	writeSettings(t, filepath.Join(dir, "RemoteOps", "worker-settings.json"), "broken")
	got, err := Resolve(SettingsOptions{Program: "worker", UserConfigDir: dir, Language: "bad", Environment: "bad", ConfigPath: path, ConfigSet: true})
	if err != nil || got.Language != English || got.Warning != nil {
		t.Fatalf("explicit file: %+v %v", got, err)
	}
}

func TestResolveDoesNotPersistProcessOverrides(t *testing.T) {
	for _, source := range []string{"flag", "environment", "explicit file"} {
		t.Run(source, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "RemoteOps", "worker-settings.json")
			if source == "explicit file" {
				path = filepath.Join(dir, "explicit.json")
			}
			original := `{"language":"zh-CN"}`
			writeSettings(t, path, original)
			opts := SettingsOptions{Program: "worker", UserConfigDir: dir}
			if source == "environment" {
				opts.EnvironmentSet, opts.Environment = true, "en-US"
			} else {
				opts.LanguageSet, opts.Language = true, "en-US"
			}
			if source == "explicit file" {
				opts.ConfigSet, opts.ConfigPath = true, path
			}
			got, err := Resolve(opts)
			if err != nil || got.Language != English {
				t.Fatalf("override: %+v %v", got, err)
			}
			body, err := os.ReadFile(path)
			if err != nil || string(body) != original {
				t.Fatalf("startup changed saved preference: %q %v", body, err)
			}
		})
	}
	// Resolving the first run is read-only, too.
	dir := t.TempDir()
	if _, err := Resolve(SettingsOptions{Program: "worker", UserConfigDir: dir}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("first-run resolution wrote files: %v %v", entries, err)
	}
}

func TestSettingsAllowsWhitespaceAtSizeLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	compact := `{"language":"en-US"}`
	body := compact + strings.Repeat(" ", 4096-len(compact))
	writeSettings(t, path, body)
	got, err := Resolve(SettingsOptions{Program: "worker", UserConfigDir: dir, ConfigSet: true, ConfigPath: path})
	if err != nil || got.Language != English {
		t.Fatalf("valid settings at limit: %+v %v", got, err)
	}
}

func TestExplicitDirectorySettingsFailEvenWhenOverridden(t *testing.T) {
	dir := t.TempDir()
	_, err := Resolve(SettingsOptions{Program: "worker", UserConfigDir: dir, ConfigSet: true, ConfigPath: dir, LanguageSet: true, Language: "en-US"})
	if err == nil {
		t.Fatal("directory accepted as explicit settings")
	}
}

func TestSaveReplacesExistingWithLanguageOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "settings.json")
	if err := Save(path, Chinese); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, English); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["language"] != "en-US" {
		t.Fatalf("saved body: %s", body)
	}
	assertNoTemporaryFiles(t, filepath.Dir(path), "settings.json")
}

func TestSaveRejectsInvalidLanguageWithoutChangingExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := `{"language":"zh-CN"}`
	writeSettings(t, path, original)
	if err := Save(path, Language("fr-FR")); err == nil {
		t.Fatal("invalid language saved")
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != original {
		t.Fatalf("changed original: %q %v", body, err)
	}
	if err := Save("", English); err == nil {
		t.Fatal("empty save path accepted")
	}
	assertNoTemporaryFiles(t, dir, "settings.json")
}

func TestSaveReplacementFailureKeepsDestinationAndCleansTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "settings.json")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(destination, "keep")
	writeSettings(t, sentinel, "original")
	if err := Save(destination, English); err == nil {
		t.Fatal("replaced a directory")
	}
	body, err := os.ReadFile(sentinel)
	if err != nil || string(body) != "original" {
		t.Fatalf("destination changed: %q %v", body, err)
	}
	assertNoTemporaryFiles(t, dir, "settings.json")
}

func TestWindowsLockedReplacementKeepsPreviousFile(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows sharing violation")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := []byte(`{"language":"zh-CN"}`)
	writeSettings(t, path, string(original))
	locked, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	if err := Save(path, English); err == nil {
		t.Fatal("replaced locked settings file")
	}
	body, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(body, original) {
		t.Fatalf("previous file lost: %q %v", body, err)
	}
	assertNoTemporaryFiles(t, dir, "settings.json")
}

func assertNoTemporaryFiles(t *testing.T, dir, destination string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != destination {
		t.Fatalf("unexpected files after save: %v", entries)
	}
}
