// Package client owns a consent-bound Worker connection shared by local interfaces.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"remoteops/internal/i18n"
	"remoteops/internal/worker"
	"remoteops/pkg/protocol"
)

type Options struct {
	// Language defaults to Chinese when omitted.
	Language i18n.Language
	// SaveLanguage persists a UI choice; it runs without any App lock.
	SaveLanguage func(i18n.Language) error
	// LanguageWarning marks recoverable startup settings failure; its payload
	// is discarded and never exposed through Snapshot.
	LanguageWarning error
	PairingPath     string
	Connect         Connector
}
type Connector func(context.Context, protocol.PairingBundle, []byte) (Connection, error)
type Connection interface {
	Snapshot() []worker.LocalTaskView
	Session() protocol.Session
	CancelTask(string) error
	Disconnect(string)
	Done() <-chan struct{}
	Wait()
	EndReason() string
}

type scopeFact struct {
	key, name, version string
	admin              bool
}

type App struct {
	language             i18n.Language
	saveLanguage         func(i18n.Language) error
	languageRevision     uint64
	languageWarning      string
	displayError         error
	scopeFacts           []scopeFact
	mu                   sync.Mutex
	ctx                  context.Context
	connect              Connector
	bundle               protocol.PairingBundle
	view                 Snapshot
	generation           uint64
	cancel               context.CancelFunc
	connection           Connection
	connectionGeneration uint64
	// The private sanitizer retains the opaque pairing value solely to redact it
	// from display data, including final results after the pairing is consumed.
	// It is never part of Snapshot or used to reconnect.
	sanitize func(string) string
	wg       sync.WaitGroup
}

func New(ctx context.Context, opts Options) *App {
	language := opts.Language
	if _, err := i18n.Parse(string(language)); err != nil {
		language = i18n.Chinese
	}
	a := &App{ctx: ctx, connect: opts.Connect, view: Snapshot{State: "idle"}, sanitize: cleanText, language: language, saveLanguage: opts.SaveLanguage}
	if opts.LanguageWarning != nil {
		a.languageWarning = "common.error.settings"
	}
	if a.connect == nil {
		a.connect = defaultConnector
	}
	if opts.PairingPath != "" {
		_ = a.LoadPairing(opts.PairingPath)
	}
	return a
}

// LoadPairing reads exactly one bounded strict JSON document. No network is used.
// An active connection must be explicitly disconnected before its consent is reset.
func (a *App) LoadPairing(path string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.view.State == "online" || a.view.State == "connecting" {
		return errActive
	}
	a.generation++
	a.bundle = protocol.PairingBundle{}
	a.connection = nil
	a.connectionGeneration = 0
	a.sanitize = cleanText
	a.view = Snapshot{State: "error"}
	a.scopeFacts = nil
	a.displayError = nil
	fail := func(err error) error { a.displayError = safeError(err); return err }
	f, err := os.Open(path)
	if err != nil {
		return fail(errPairingFile)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, protocol.MaxBodyBytes+1))
	if err != nil {
		return fail(errPairingFile)
	}
	if len(data) > protocol.MaxBodyBytes {
		return fail(errPairingInvalid)
	}
	var b protocol.PairingBundle
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&b) != nil || dec.Decode(new(any)) != io.EOF {
		return fail(errPairingInvalid)
	}
	if !time.Now().Before(b.ExpiresAt) {
		return fail(errPairingExpired)
	}
	if worker.ValidatePairing(b) != nil {
		return fail(errPairingInvalid)
	}
	a.bundle = b
	credential := b.PairingToken
	display := func(s string) string { return cleanText(strings.ReplaceAll(s, credential, "[REDACTED]")) }
	a.sanitize = display
	facts := []scopeFact{}
	for _, task := range b.Scope.AllowedTasks {
		key := "worker.scope.install"
		if task == protocol.InspectSystem {
			key = "worker.scope.inspect"
		}
		facts = append(facts, scopeFact{key: key})
	}
	for _, id := range b.Scope.AllowedPackages {
		e, _ := protocol.FindPackage(b.Catalog, id, "")
		facts = append(facts, scopeFact{name: display(e.Name), version: display(e.Version), admin: e.RequiresAdmin})
	}
	key := "worker.scope.admin_denied"
	if b.Scope.AllowAdmin {
		key = "worker.scope.admin_allowed"
	}
	a.scopeFacts = append(facts, scopeFact{key: key}, scopeFact{key: "worker.scope.finish"})
	a.view = Snapshot{State: "ready", Controller: display(b.ControllerName), Address: display(b.ControllerURL), PairingExpiresAt: b.ExpiresAt, Authorization: time.Duration(b.AuthorizationSeconds) * time.Second}
	return nil
}

func validKey(key []byte) bool {
	return len(key) > len("tskey-auth-") && len(key) <= 4096 && bytes.HasPrefix(key, []byte("tskey-auth-")) && !bytes.ContainsAny(key, " \t\r\n\x00")
}

// ConsentAndConnect is the explicit local consent boundary. It copies the key,
// clears the caller's buffer on every return path, and never retries automatically.
func (a *App) ConsentAndConnect(key []byte) error {
	defer clear(key)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.view.State != "ready" {
		return errNotReady
	}
	if !validKey(key) {
		return errKey
	}
	if err := a.ctx.Err(); err != nil {
		return errNotReady
	}
	if !time.Now().Before(a.bundle.ExpiresAt) {
		a.bundle = protocol.PairingBundle{}
		a.view.State = "error"
		a.displayError = errPairingExpired
		return errPairingExpired
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.cancel = cancel
	a.generation++
	generation := a.generation
	b := a.bundle
	// The loaded document's nested slices are not exposed; only this attempt owns it now.
	a.bundle = protocol.PairingBundle{}
	copied := append([]byte(nil), key...)
	a.view.State = "connecting"
	a.displayError = nil
	a.wg.Add(1)
	go a.run(ctx, cancel, generation, b, copied)
	return nil
}

func (a *App) run(ctx context.Context, cancel context.CancelFunc, generation uint64, b protocol.PairingBundle, key []byte) {
	defer a.wg.Done()
	defer cancel()
	defer clear(key)
	conn, err := a.connect(ctx, b, key)
	b.PairingToken = ""
	clear(key)
	// Session implementations may take time; never call them while holding the
	// lifecycle mutex, and recheck the generation before publishing their result.
	var session protocol.Session
	if conn != nil && err == nil && ctx.Err() == nil {
		session = conn.Session()
	}
	a.mu.Lock()
	current := generation == a.generation
	if !current || ctx.Err() != nil || err != nil || conn == nil {
		if current {
			if ctx.Err() != nil {
				a.view.State = "disconnected"
				a.view.EndReason = "local_closed"
			} else {
				a.view.State = "error"
				if err == nil {
					err = errNotOnline
				}
				a.displayError = safeError(err)
			}
		}
		a.mu.Unlock()
		if conn != nil {
			conn.Disconnect("local_closed")
			conn.Wait()
		}
		return
	}
	a.connection = conn
	a.connectionGeneration = generation
	a.view.State = "online"
	a.view.ExpiresAt = session.ExpiresAt
	a.mu.Unlock()
	select {
	case <-ctx.Done():
		conn.Disconnect("local_closed")
	case <-conn.Done():
	}
	reason := normalizedReason(conn.EndReason())
	a.mu.Lock()
	if generation == a.generation {
		a.view.State = "disconnected"
		a.view.EndReason = reason
	}
	a.mu.Unlock()
	conn.Wait()
}

// Snapshot returns detached display data, retaining final results after disconnect.
func (a *App) Snapshot() Snapshot {
	a.mu.Lock()
	conn := a.connection
	connectionGeneration := a.connectionGeneration
	sanitize := a.sanitize
	a.mu.Unlock()
	var tasks []worker.LocalTaskView
	if conn != nil {
		tasks = copyTasks(conn.Snapshot(), sanitize)
	}
	a.mu.Lock()
	s := a.view
	s.Language = a.language
	s.Scope = a.renderScope(s.Language)
	s.Error = ErrorText(a.displayError, s.Language)
	if a.languageWarning != "" {
		s.Warning = i18n.Text(s.Language, a.languageWarning)
	}
	// A slow observer cannot return an old pairing's tasks after a consent reset.
	if connectionGeneration == a.connectionGeneration {
		s.Tasks = tasks
	}
	a.mu.Unlock()
	if s.State == "online" {
		s.Notice = onlineNotice(s.Tasks, s.Language)
	}
	if s.State == "connecting" {
		s.Notice = i18n.Text(s.Language, "worker.notice.connecting")
	}
	if s.State == "disconnected" {
		s.Notice = endNotice(s.EndReason, s.Language)
	}
	return s
}

func copyTasks(tasks []worker.LocalTaskView, sanitize func(string) string) []worker.LocalTaskView {
	tasks = worker.RecentTaskViews(tasks, 100)
	// protocol tasks contain nested pointers/maps. JSON detaches all of them.
	b, _ := json.Marshal(tasks)
	var out []worker.LocalTaskView
	_ = json.Unmarshal(b, &out)
	for i := range out {
		v := &out[i]
		v.PackageName = sanitize(v.PackageName)
		v.Progress.Message = sanitize(v.Progress.Message)
		v.Task.ErrorCode = sanitize(v.Task.ErrorCode)
		v.Task.ErrorMessage = sanitize(v.Task.ErrorMessage)
		if v.Task.Spec.Package != nil {
			v.Task.Spec.Package.Version = sanitize(v.Task.Spec.Package.Version)
		}
		if v.Task.Result != nil {
			v.Task.Result.Version = sanitize(v.Task.Result.Version)
			sanitizeSystemInfo(v.Task.Result.Info, sanitize)
		}
		for j := range v.Logs {
			v.Logs[j].Message = sanitize(v.Logs[j].Message)
			v.Logs[j].Stream = sanitize(v.Logs[j].Stream)
		}
	}
	return out
}

// Only detached, informational result data is changed. Task/session/device/
// package identifiers remain intact for selecting and cancelling the right task.
func sanitizeSystemInfo(info *protocol.SystemInfo, sanitize func(string) string) {
	if info == nil {
		return
	}
	info.OS = sanitize(info.OS)
	info.Hostname = sanitize(info.Hostname)
	info.CPU = sanitize(info.CPU)
	for i := range info.Disks {
		info.Disks[i].Name = sanitize(info.Disks[i].Name)
	}
	for i := range info.GPUs {
		info.GPUs[i].Name = sanitize(info.GPUs[i].Name)
		info.GPUs[i].Driver = sanitize(info.GPUs[i].Driver)
	}
	if info.Software != nil {
		software := make(map[string]string, len(info.Software))
		for name, version := range info.Software {
			software[sanitize(name)] = sanitize(version)
		}
		info.Software = software
	}
}

func (a *App) CancelTask(id string) error {
	a.mu.Lock()
	if a.view.State != "online" || a.connection == nil {
		a.mu.Unlock()
		return errNotOnline
	}
	conn := a.connection
	a.mu.Unlock()
	if conn.CancelTask(id) != nil {
		return errTask
	}
	return nil
}

// Disconnect cancels an in-flight connector immediately. Its late result still
// owns cleanup, but cannot overwrite a newly loaded pairing or become online.
func (a *App) Disconnect(reason string) {
	a.mu.Lock()
	if a.view.State == "disconnected" {
		a.mu.Unlock()
		return
	}
	a.generation++
	reason = normalizedReason(reason)
	a.view.State = "disconnected"
	a.displayError = nil
	a.view.EndReason = reason
	a.bundle = protocol.PairingBundle{}
	cancel, conn := a.cancel, a.connection
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if conn != nil {
		conn.Disconnect(reason)
	}
}

// Wait joins outstanding connection attempts and their resource cleanup. Local
// interfaces call it after Disconnect or after observing a terminal session state.
func (a *App) Wait() { a.wg.Wait() }

// Language returns the current process language.
func (a *App) Language() i18n.Language { a.mu.Lock(); defer a.mu.Unlock(); return a.language }

// SetLanguage changes display state without invalidating consent or the connection.
// External persistence runs without any app lock. A callback finishing after a
// newer choice repairs its stale write before returning. Once callbacks finish
// successfully, the latest accepted choice is persisted.
func (a *App) SetLanguage(language i18n.Language) error {
	if _, err := i18n.Parse(string(language)); err != nil {
		return err
	}
	a.mu.Lock()
	a.language = language
	a.languageRevision++
	revision := a.languageRevision
	save := a.saveLanguage
	a.mu.Unlock()
	if save == nil {
		return nil
	}
	for {
		err := save(language)
		a.mu.Lock()
		if revision == a.languageRevision {
			if err != nil {
				a.languageWarning = "common.error.save"
			} else {
				a.languageWarning = ""
			}
			a.mu.Unlock()
			return err
		}
		// Even a failed callback might have partially persisted its old choice.
		// Repair using the current locale, and recheck in case that choice also
		// changes during persistence. Do not call SetLanguage here: a repair is
		// not a new choice and must not invalidate another accepted revision.
		language, revision = a.language, a.languageRevision
		a.mu.Unlock()
	}
}

func (a *App) renderScope(language i18n.Language) []string {
	scope := make([]string, 0, len(a.scopeFacts))
	for _, fact := range a.scopeFacts {
		if fact.key != "" {
			scope = append(scope, i18n.Text(language, fact.key))
			continue
		}
		label := fact.name + " " + fact.version
		if fact.admin {
			label += i18n.Text(language, "worker.scope.admin_package")
		}
		scope = append(scope, label)
	}
	return scope
}

// Keep only known safe classifications, never a transport/parser error payload.
func safeError(err error) error {
	for _, candidate := range []error{errPairingFile, errPairingInvalid, errPairingExpired, errNotReady, errActive, errKey, errNotOnline, errTask, errPlatform, errElevated} {
		if errors.Is(err, candidate) {
			return candidate
		}
	}
	return errConnection
}
