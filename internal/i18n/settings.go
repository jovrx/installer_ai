package i18n

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const maxSettingsBytes = 4096

// SettingsOptions contains explicit inputs from the calling entry point.
// Environment and EnvironmentSet normally come from os.LookupEnv("REMOTEOPS_LANG").
// A Set field distinguishes an absent input from a present but invalid empty value.
type SettingsOptions struct {
	Language       string
	LanguageSet    bool
	Environment    string
	EnvironmentSet bool
	ConfigPath     string
	ConfigSet      bool
	UserConfigDir  string
	Program        string
}

// Settings is the resolved process locale and the file to use for future saves.
// Warning indicates unreadable or invalid implicit settings; it is recoverable.
type Settings struct {
	Language Language
	Path     string
	Warning  error
}

// Resolve validates every explicit input, including overridden ones, and applies
// flag > environment > explicit file > saved file > Chinese priority. It never saves.
func Resolve(options SettingsOptions) (Settings, error) {
	switch options.Program {
	case "worker", "controller", "mcp":
	default:
		return Settings{}, errors.New("settings program must be worker, controller, or mcp")
	}
	var flag, environment Language
	var err error
	if options.LanguageSet {
		flag, err = Parse(options.Language)
		if err != nil {
			return Settings{}, fmt.Errorf("--lang: %w", err)
		}
	}
	if options.EnvironmentSet {
		environment, err = Parse(options.Environment)
		if err != nil {
			return Settings{}, fmt.Errorf("REMOTEOPS_LANG: %w", err)
		}
	}
	settings := Settings{Language: Chinese}
	if options.ConfigSet {
		if options.ConfigPath == "" {
			return Settings{}, errors.New("--config requires a settings file path")
		}
		settings.Path = options.ConfigPath
		settings.Language, err = readSettings(settings.Path)
		if err != nil {
			return Settings{}, fmt.Errorf("--config: %w", err)
		}
	} else {
		dir := options.UserConfigDir
		if dir == "" {
			dir, err = os.UserConfigDir()
			if err != nil {
				settings.Warning = fmt.Errorf("locate user settings: %w", err)
			}
		}
		if dir != "" {
			settings.Path = filepath.Join(dir, "RemoteOps", options.Program+"-settings.json")
			language, readErr := readSettings(settings.Path)
			switch {
			case readErr == nil:
				settings.Language = language
			case errors.Is(readErr, os.ErrNotExist):
				// A first run has no preference yet.
			default:
				settings.Warning = readErr
			}
		}
	}
	if options.EnvironmentSet {
		settings.Language = environment
	}
	if options.LanguageSet {
		settings.Language = flag
	}
	return settings, nil
}

func readSettings(path string) (Language, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open settings file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect settings file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("settings must be a regular file")
	}
	body, err := io.ReadAll(io.LimitReader(file, maxSettingsBytes+1))
	if err != nil {
		return "", fmt.Errorf("read settings file: %w", err)
	}
	if len(body) > maxSettingsBytes {
		return "", errors.New("settings file exceeds 4096 bytes")
	}
	return parseSettings(body)
}

// Token-level decoding rejects duplicate and case-insensitive field names as
// well as unknown fields. Decoder errors are deliberately kept body-free.
func parseSettings(body []byte) (Language, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return "", errors.New("settings must be a JSON object with one language field")
	}
	seen := false
	var language string
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil || key != "language" || seen {
			return "", errors.New("settings must contain only one language field")
		}
		seen = true
		if err := decoder.Decode(&language); err != nil {
			return "", errors.New("settings language must be a string")
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') || !seen {
		return "", errors.New("settings must contain one language field")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", errors.New("settings contain trailing JSON or invalid data")
	}
	return Parse(language)
}

// Save persists only the language. It writes and syncs a same-directory temporary
// file, then replaces the destination atomically without removing the old file first.
func Save(path string, language Language) error {
	if _, err := Parse(string(language)); err != nil {
		return err
	}
	if path == "" {
		return errors.New("settings save requires a file path")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}
	file, err := os.CreateTemp(dir, ".remoteops-language-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary settings: %w", err)
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	defer file.Close()
	body, err := json.MarshalIndent(struct {
		Language Language `json:"language"`
	}{language}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	if _, err := file.Write(append(body, '\n')); err != nil {
		return fmt.Errorf("write temporary settings: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync temporary settings: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary settings: %w", err)
	}
	if err := replaceSettings(temporary, path); err != nil {
		return fmt.Errorf("replace settings file: %w", err)
	}
	return nil
}
