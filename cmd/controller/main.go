package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"remoteops/internal/commandlocale"
	"remoteops/internal/controller"
	"remoteops/internal/i18n"
	"remoteops/internal/security"
	"remoteops/internal/webui"
	"remoteops/pkg/protocol"
	"strings"
	"syscall"
	"time"
)

type startup struct{ Management, Worker, TLSCert, TLSKey string }

func (c startup) validate() error {
	host, _, err := net.SplitHostPort(c.Management)
	if err != nil {
		return commandError("controller.error.management_address")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		if c.TLSCert == "" || c.TLSKey == "" {
			return commandError("controller.error.management_tls")
		}
	}
	host, _, err = net.SplitHostPort(c.Worker)
	if err != nil {
		return commandError("controller.error.worker_required")
	}
	ip, err = netip.ParseAddr(host)
	if err != nil || !(netip.MustParsePrefix("100.64.0.0/10").Contains(ip) || netip.MustParsePrefix("fd7a:115c:a1e0::/48").Contains(ip)) {
		return commandError("controller.error.worker_address")
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func agentCredential(configured, path, operator string) (string, error) {
	if configured != "" {
		if len(configured) < 32 || configured == operator {
			return "", commandError("controller.error.mcp_token")
		}
		return configured, nil
	}
	token := security.NewID("mcp")
	if path == "" {
		return "", commandError("controller.error.mcp_path")
	}
	if err := controller.SecureStateDirectory(filepath.Dir(path)); err != nil {
		return "", err
	}
	// Create a protected temporary file and atomically replace the output path.
	// Rename replaces a final symlink instead of writing the credential through it.
	f, err := os.CreateTemp(filepath.Dir(path), ".remoteops-mcp-*")
	if err != nil {
		return "", commandError("controller.error.mcp_create")
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = controller.ProtectStateFile(tmp); err != nil {
		f.Close()
		return "", err
	}
	if _, err = f.WriteString(token); err != nil {
		f.Close()
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	if err = os.Rename(tmp, path); err != nil {
		return "", commandError("controller.error.mcp_replace")
	}
	return token, nil
}

type cliOptions struct {
	settings                            i18n.Settings
	startup                             startup
	db, name, catalogPath, mcpTokenPath string
}
type commandError string

func (err commandError) Error() string { return i18n.Text(i18n.Chinese, string(err)) }
func safeDiagnostic(value string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r >= 127 && r <= 159 {
			return -1
		}
		return r
	}, security.Redact(value))
}

func parseOptions(args []string, output io.Writer, userDir string) (cliOptions, error) {
	var opts cliOptions
	settings, err := commandlocale.Resolve(args, commandlocale.Options{Program: "controller", UserConfigDir: userDir, ValueFlags: []string{"listen", "worker-listen", "tls-cert", "tls-key", "db", "name", "catalog", "mcp-token-file"}})
	opts.settings = settings
	if err != nil {
		return opts, err
	}
	language := settings.Language
	c := &opts.startup
	fs := commandlocale.NewFlagSet("remoteops-controller", output, language, "controller.cli.usage")
	fs.StringVar(&c.Management, "listen", "127.0.0.1:8080", i18n.Text(language, "controller.cli.listen"))
	fs.StringVar(&c.Worker, "worker-listen", "", i18n.Text(language, "controller.cli.worker-listen"))
	fs.StringVar(&c.TLSCert, "tls-cert", "", i18n.Text(language, "controller.cli.tls-cert"))
	fs.StringVar(&c.TLSKey, "tls-key", "", i18n.Text(language, "controller.cli.tls-key"))
	fs.StringVar(&opts.db, "db", "remoteops-state/remoteops.db", i18n.Text(language, "controller.cli.db"))
	fs.StringVar(&opts.name, "name", "RemoteOps", i18n.Text(language, "controller.cli.name"))
	fs.StringVar(&opts.catalogPath, "catalog", "", i18n.Text(language, "controller.cli.catalog"))
	fs.StringVar(&opts.mcpTokenPath, "mcp-token-file", "remoteops-state/mcp.token", i18n.Text(language, "controller.cli.mcp-token-file"))
	if err := commandlocale.Parse(fs, args, language); err != nil {
		return opts, err
	}
	if opts.db == "" {
		return opts, errors.New(i18n.Text(language, "controller.error.database_path"))
	}
	return opts, nil
}
func run() (result error) {
	opts, err := parseOptions(os.Args[1:], os.Stderr, "")
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	language := opts.settings.Language
	defer func() { result = formatStartupError(result, language) }()
	if opts.settings.Warning != nil {
		fmt.Fprintln(os.Stderr, i18n.Text(language, "native.cli.settings_warning"))
	}
	c := opts.startup
	db, name, catalogPath, mcpTokenPath := opts.db, opts.name, opts.catalogPath, opts.mcpTokenPath

	if err := c.validate(); err != nil {
		return err
	}
	token := os.Getenv("REMOTEOPS_ADMIN_TOKEN")
	if len(token) < 32 {
		return commandError("controller.error.admin_token")
	}
	catalog, err := loadCatalog(catalogPath)
	if err != nil {
		return err
	}
	agentToken, err := agentCredential(os.Getenv("REMOTEOPS_MCP_TOKEN"), mcpTokenPath, token)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var cleaner controller.NodeCleaner
	if key := os.Getenv("REMOTEOPS_TAILSCALE_API_KEY"); key != "" {
		cleaner = controller.TailscaleNodeCleaner{APIKey: key}
	}
	s, err := controller.New(controller.Options{DBPath: db, AdminToken: token, AgentToken: agentToken, ControllerName: name, ControllerURL: "http://" + c.Worker, Catalog: catalog, NodeCleaner: cleaner})
	if err != nil {
		return err
	}
	defer s.Close()
	management := &http.Server{Addr: c.Management, Handler: webui.Handler(s.Handler()), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	worker := &http.Server{Addr: c.Worker, Handler: s.WorkerHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	ml, err := net.Listen("tcp", c.Management)
	if err != nil {
		return commandError("controller.error.management_listener")
	}
	defer ml.Close()
	wl, err := net.Listen("tcp", c.Worker)
	if err != nil {
		return commandError("controller.error.worker_listener")
	}
	defer wl.Close()
	s.Start(ctx)
	errs := make(chan error, 2)
	go func() {
		if c.TLSCert != "" && c.TLSKey != "" {
			errs <- management.ServeTLS(ml, c.TLSCert, c.TLSKey)
		} else {
			errs <- management.Serve(ml)
		}
	}()
	go func() { errs <- worker.Serve(wl) }()
	fmt.Fprintln(os.Stderr, i18n.Text(language, "controller.started"))
	select {
	case <-ctx.Done():
	case err = <-errs:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = management.Shutdown(shutdown)
	_ = worker.Shutdown(shutdown)
	return err
}

// Product input validation is classified before constructor side effects. OS
// file errors retain their original diagnostic details through formatStartupError.
func validateCatalog(catalog []protocol.CatalogEntry) error {
	if err := protocol.ValidateCatalog(catalog); err != nil {
		return commandError("controller.error.catalog_invalid")
	}
	return nil
}
func loadCatalog(path string) ([]protocol.CatalogEntry, error) {
	catalog := []protocol.CatalogEntry{}
	if path == "" {
		return catalog, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, protocol.MaxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > protocol.MaxBodyBytes {
		return nil, commandError("controller.error.catalog_size")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		return nil, commandError("controller.error.catalog_format")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, commandError("controller.error.catalog_format")
	}
	if err := validateCatalog(catalog); err != nil {
		return nil, err
	}
	return catalog, nil
}
func formatStartupError(err error, language i18n.Language) error {
	if err == nil {
		return nil
	}
	var category commandError
	if errors.As(err, &category) {
		return errors.New(i18n.Text(language, string(category)))
	}
	return errors.New(i18n.Text(language, "common.error.detail", safeDiagnostic(err.Error())))
}
