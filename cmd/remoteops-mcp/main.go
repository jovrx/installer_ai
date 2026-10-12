package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"remoteops/integrations/mcp"
	"remoteops/internal/commandlocale"
	"remoteops/internal/i18n"
	"strings"
	"time"
)

type cliOptions struct {
	settings        i18n.Settings
	base, tokenFile string
}

func parseOptions(args []string, diagnostic io.Writer, userDir string) (cliOptions, error) {
	var opts cliOptions
	settings, err := commandlocale.Resolve(args, commandlocale.Options{Program: "mcp", UserConfigDir: userDir, ValueFlags: []string{"controller", "token-file"}})
	opts.settings = settings
	if err != nil {
		return opts, err
	}
	language := settings.Language
	fs := commandlocale.NewFlagSet("remoteops-mcp", diagnostic, language, "mcp.cli.usage")
	fs.StringVar(&opts.base, "controller", "http://127.0.0.1:8080", i18n.Text(language, "mcp.cli.controller"))
	fs.StringVar(&opts.tokenFile, "token-file", "", i18n.Text(language, "mcp.cli.token-file"))
	if err := commandlocale.Parse(fs, args, language); err != nil {
		return opts, err
	}
	return opts, nil
}
func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, ""); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string, in io.Reader, out, diagnostic io.Writer, userDir string) error {
	opts, err := parseOptions(args, diagnostic, userDir)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	language := opts.settings.Language
	if opts.settings.Warning != nil {
		fmt.Fprintln(diagnostic, i18n.Text(language, "native.cli.settings_warning"))
	}
	if err := mcp.ValidateLocalURL(opts.base); err != nil {
		return errors.New(i18n.Text(language, "mcp.cli.error.url"))
	}
	token := os.Getenv("REMOTEOPS_TOKEN")
	if opts.tokenFile != "" {
		data, err := os.ReadFile(opts.tokenFile)
		if err != nil {
			return errors.New(i18n.Text(language, "mcp.cli.error.token_file"))
		}
		token = strings.TrimSpace(string(data))
	}
	if len(token) < 16 {
		return errors.New(i18n.Text(language, "mcp.cli.error.token"))
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	if err := mcp.NewWithLanguage(opts.base, token, client, language).Serve(in, out); err != nil {
		return errors.New(i18n.Text(language, "mcp.cli.error.transport"))
	}
	return nil
}
