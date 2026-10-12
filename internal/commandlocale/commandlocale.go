// Package commandlocale owns shared native-command locale parsing and help.
// The language settings API stays in i18n; this package only adapts CLI inputs.
package commandlocale

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"remoteops/internal/i18n"
	"strings"
)

type Options struct {
	Program       string
	UserConfigDir string
	ValueFlags    []string
}

// Resolve observes locale flags after help, but stops at -- or a positional.
// Known value-taking flags consume their next argument opaquely even when that
// value resembles --lang or --config. No preference is saved.
func Resolve(args []string, options Options) (i18n.Settings, error) {
	inputs := i18n.SettingsOptions{Program: options.Program, UserConfigDir: options.UserConfigDir}
	inputs.Environment, inputs.EnvironmentSet = os.LookupEnv("REMOTEOPS_LANG")
	valueFlags := map[string]bool{"lang": true, "config": true}
	for _, name := range options.ValueFlags {
		valueFlags[name] = true
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" || !strings.HasPrefix(arg, "-") {
			break
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-"), "=")
		if !valueFlags[name] {
			continue
		}
		if !hasValue && index+1 < len(args) {
			index++
			value = args[index]
		}
		if name == "lang" {
			inputs.Language, inputs.LanguageSet = value, true
		}
		if name == "config" {
			inputs.ConfigPath, inputs.ConfigSet = value, true
		}
	}
	language := i18n.Chinese
	if value, err := i18n.Parse(inputs.Environment); inputs.EnvironmentSet && err == nil {
		language = value
	}
	if value, err := i18n.Parse(inputs.Language); inputs.LanguageSet && err == nil {
		language = value
	}
	settings, err := i18n.Resolve(inputs)
	if err != nil {
		key := "native.cli.settings_invalid"
		if _, err := i18n.Parse(inputs.Language); inputs.LanguageSet && err != nil {
			key = "common.error.language"
		}
		if _, err := i18n.Parse(inputs.Environment); inputs.EnvironmentSet && err != nil {
			key = "common.error.language"
		}
		return i18n.Settings{Language: language}, errors.New(i18n.Text(language, key))
	}
	return settings, nil
}

// NewFlagSet registers shared flags and keeps all help/diagnostics on the
// supplied local writer. Command-specific flags are registered by the caller.
func NewFlagSet(name string, output io.Writer, language i18n.Language, usageKey string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.String("lang", "", i18n.Text(language, "native.cli.lang"))
	fs.String("config", "", i18n.Text(language, "native.cli.config"))
	fs.Usage = func() {
		fmt.Fprintln(output, i18n.Text(language, usageKey))
		fs.VisitAll(func(f *flag.Flag) {
			fmt.Fprintf(output, "  --%s %s\n      %s", f.Name, i18n.Text(language, "native.cli.value"), f.Usage)
			if f.DefValue != "" {
				fmt.Fprintf(output, " (%s)", i18n.Text(language, "native.cli.default", f.DefValue))
			}
			fmt.Fprintln(output)
		})
	}
	return fs
}

// Parse preserves flag.ErrHelp and localizes categories without echoing fixed
// Go flag diagnostics or untrusted argument values.
func Parse(fs *flag.FlagSet, args []string, language i18n.Language) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return errors.New(i18n.Text(language, "native.cli.arguments"))
	}
	if fs.NArg() != 0 {
		return errors.New(i18n.Text(language, "native.cli.positional"))
	}
	return nil
}
