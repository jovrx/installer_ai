// Worker is a portable, per-session executable. Its elevated helper never joins tsnet.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"

	"remoteops/internal/i18n"
	"remoteops/internal/worker"
	"remoteops/internal/worker/client"
	"remoteops/internal/worker/privilege"
	"remoteops/internal/worker/ui/gui"
	"remoteops/internal/worker/ui/tui"
)

// Both artifacts share this entrypoint; the GUI build overrides this with -X.
var defaultUI = "tui"

type cliOptions struct {
	settings    i18n.Settings
	pairingPath string
	helperPath  string
	ui          string
	helper      bool
}

// preScanSettings observes language/config flags even after --help. It respects
// value-taking flags and -- so pairing paths and positional text stay opaque.
func preScanSettings(args []string) i18n.SettingsOptions {
	opts := i18n.SettingsOptions{Program: "worker"}
	opts.Environment, opts.EnvironmentSet = os.LookupEnv("REMOTEOPS_LANG")
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		if !strings.HasPrefix(args[i], "-") {
			break
		}
		arg := strings.TrimPrefix(args[i], "-")
		arg = strings.TrimPrefix(arg, "-")
		name, value, hasValue := strings.Cut(arg, "=")
		switch name {
		case "lang", "config", "pairing", "ui":
			if !hasValue && i+1 < len(args) {
				i++
				value = args[i]
			}
			if name == "lang" {
				opts.Language, opts.LanguageSet = value, true
			}
			if name == "config" {
				opts.ConfigPath, opts.ConfigSet = value, true
			}
		}
	}
	return opts
}

func parseOptions(args []string, output io.Writer) (cliOptions, error) {
	opts := cliOptions{ui: defaultUI}
	// Detect helper anywhere in flag arguments before reading any settings. A
	// malformed mixed invocation must still route errors away from the GUI.
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		name := strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-")
		if name == "elevated-helper" || strings.HasPrefix(name, "elevated-helper=") {
			opts.helper = true
			break
		}
	}
	if opts.helper {
		if len(args) == 1 {
			name, value, ok := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(args[0], "-"), "-"), "=")
			if ok && name == "elevated-helper" {
				opts.helperPath = value
			}
		} else if len(args) == 2 && (args[0] == "--elevated-helper" || args[0] == "-elevated-helper") && !strings.HasPrefix(args[1], "-") {
			opts.helperPath = args[1]
		}
		if opts.helperPath == "" {
			return opts, errors.New("辅助进程不能与界面、配对、语言、设置或位置参数同时使用，且请求路径不能为空")
		}
		return opts, nil
	}
	inputs := preScanSettings(args)
	language := i18n.Chinese
	if parsed, err := i18n.Parse(inputs.Environment); inputs.EnvironmentSet && err == nil {
		language = parsed
	}
	if parsed, err := i18n.Parse(inputs.Language); inputs.LanguageSet && err == nil {
		language = parsed
	}
	opts.settings.Language = language
	settings, err := i18n.Resolve(inputs)
	if err != nil {
		key := "worker.cli.settings_invalid"
		if _, err := i18n.Parse(inputs.Language); inputs.LanguageSet && err != nil {
			key = "common.error.language"
		}
		if _, err := i18n.Parse(inputs.Environment); inputs.EnvironmentSet && err != nil {
			key = "common.error.language"
		}
		return opts, errors.New(i18n.Text(language, key))
	}
	opts.settings = settings
	language = settings.Language
	fs := flag.NewFlagSet("remoteops-worker", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {
		fmt.Fprintln(output, i18n.Text(language, "worker.cli.usage"))
		fs.SetOutput(output)
		fs.PrintDefaults()
		fs.SetOutput(io.Discard)
	}
	fs.StringVar(&opts.pairingPath, "pairing", "", i18n.Text(language, "worker.cli.pairing"))
	fs.StringVar(&opts.ui, "ui", defaultUI, i18n.Text(language, "worker.cli.ui"))
	fs.String("lang", "", i18n.Text(language, "worker.cli.lang"))
	fs.String("config", "", i18n.Text(language, "worker.cli.config"))
	fs.String("elevated-helper", "", i18n.Text(language, "worker.cli.helper"))
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return opts, err
		}
		return opts, errors.New(i18n.Text(language, "worker.cli.arguments", client.SafeText(err.Error())))
	}
	if fs.NArg() != 0 {
		return opts, errors.New(i18n.Text(language, "worker.cli.positional"))
	}
	switch opts.ui {
	case "gui", "tui", "plain":
	default:
		return opts, errors.New(i18n.Text(language, "worker.cli.ui_invalid"))
	}
	return opts, nil
}

func main() {
	opts, err := parseOptions(os.Args[1:], os.Stdout)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err == nil {
		err = runOptions(opts)
	}
	if err != nil {
		message := client.SafeText(err.Error())
		if !opts.helper && (defaultUI == "gui" || opts.ui == "gui") {
			showNativeError(message, opts.settings.Language)
		} else {
			fmt.Fprintln(os.Stderr, message)
		}
		os.Exit(1)
	}
}

func runOptions(opts cliOptions) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	// Helper dispatch precedes all application, terminal and window setup.
	if opts.helper {
		return worker.RunElevatedHelper(ctx, opts.helperPath)
	}
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		return errors.New(i18n.Text(opts.settings.Language, "worker.startup.platform"))
	}
	if privilege.Elevated() {
		return errors.New(i18n.Text(opts.settings.Language, "worker.startup.elevated"))
	}
	app := client.New(ctx, client.Options{PairingPath: opts.pairingPath, Language: opts.settings.Language, LanguageWarning: opts.settings.Warning, SaveLanguage: func(language i18n.Language) error { return i18n.Save(opts.settings.Path, language) }})
	defer func() { app.Disconnect("local_closed"); app.Wait() }()
	if opts.ui == "gui" {
		return gui.Run(ctx, app)
	}
	return tui.Run(ctx, app, opts.ui == "plain")
}
