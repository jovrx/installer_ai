// Package controller provides the scoped management API and durable task scheduler.
package controller

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	_ "modernc.org/sqlite"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"remoteops/internal/security"
	"remoteops/pkg/protocol"
	"sync"
	"time"
)

type Peer struct {
	Address string
	NodeID  string
}
type PeerResolver interface {
	Resolve(context.Context, *http.Request) (Peer, error)
}
type PeerResolverFunc func(context.Context, *http.Request) (Peer, error)

func (f PeerResolverFunc) Resolve(c context.Context, r *http.Request) (Peer, error) { return f(c, r) }

type NodeCleaner interface {
	DeleteNode(context.Context, string) error
}
type NodeCleanerFunc func(context.Context, string) error

func (f NodeCleanerFunc) DeleteNode(c context.Context, id string) error { return f(c, id) }

type Options struct {
	DBPath, AdminToken, AgentToken, ControllerName, ControllerURL string
	Catalog                                                       []protocol.CatalogEntry
	HTTPClient                                                    *http.Client
	PeerResolver                                                  PeerResolver
	NodeCleaner                                                   NodeCleaner
	// Now and TickInterval make expiry deterministic without real-time sleeps.
	Now          func() time.Time
	TickInterval time.Duration
}
type sessionRecord struct {
	Session              protocol.Session        `json:"session"`
	PairingDigest        string                  `json:"pairing_digest"`
	Token                string                  `json:"token,omitempty"`
	Catalog              []protocol.CatalogEntry `json:"catalog"`
	AuthorizationSeconds int                     `json:"authorization_seconds"`
}
type browserSession struct {
	CSRF    string
	Expires time.Time
}
type cleanupJob struct {
	SessionID string
	Attempts  int
	Next      time.Time
}
type Server struct {
	mu          sync.Mutex
	db          *sql.DB
	opts        Options
	config      protocol.Config
	adminDigest string
	agentDigest string
	cookies     map[string]browserSession
	verifiers   map[string]*security.Verifier
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	closed      bool
}

func New(o Options) (*Server, error) {
	if len(o.AdminToken) < 32 {
		return nil, errors.New("management token must contain at least 32 characters")
	}
	if o.AgentToken != "" && (len(o.AgentToken) < 32 || o.AgentToken == o.AdminToken) {
		return nil, errors.New("MCP token must be distinct from operator token and contain at least 32 characters")
	}
	if o.DBPath == "" {
		return nil, errors.New("database path required")
	}
	if err := protocol.ValidateCatalog(o.Catalog); err != nil {
		return nil, err
	}
	// Deep-copy catalog so the caller cannot alter a paired session's authority.
	b, _ := json.Marshal(o.Catalog)
	json.Unmarshal(b, &o.Catalog)
	if o.Catalog == nil {
		o.Catalog = []protocol.CatalogEntry{}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.TickInterval <= 0 {
		o.TickInterval = time.Second
	}
	if o.ControllerName == "" {
		o.ControllerName = "RemoteOps"
	}
	if o.HTTPClient == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		// Observed tailnet addresses are reached directly; environment HTTP proxies
		// must not become an alternate identity or data boundary.
		transport.Proxy = nil
		o.HTTPClient = &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	} else {
		copy := *o.HTTPClient
		copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		if copy.Timeout == 0 {
			copy.Timeout = 5 * time.Second
		}
		o.HTTPClient = &copy
	}
	if o.PeerResolver == nil {
		o.PeerResolver = LocalPeerResolver{}
	}
	if o.DBPath != ":memory:" {
		if err := rejectStateLinks(o.DBPath); err != nil {
			return nil, err
		}
		if err := SecureStateDirectory(filepath.Dir(o.DBPath)); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(o.DBPath, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		f.Close()
		if err := ProtectStateFile(o.DBPath); err != nil {
			return nil, err
		}
		// Existing SQLite sidecars may contain session keys. Protect them before
		// opening SQLite; future journals inherit the private directory DACL.
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			path := o.DBPath + suffix
			if _, err := os.Lstat(path); err == nil {
				if err = ProtectStateFile(path); err != nil {
					return nil, err
				}
			} else if !os.IsNotExist(err) {
				return nil, err
			}
		}
	}
	db, err := sql.Open("sqlite", o.DBPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Server{db: db, opts: o, config: protocol.DefaultConfig(), adminDigest: security.TokenDigest(o.AdminToken), cookies: map[string]browserSession{}, verifiers: map[string]*security.Verifier{}}
	s.opts.AdminToken = ""
	if o.AgentToken != "" {
		s.agentDigest = security.TokenDigest(o.AgentToken)
	}
	s.opts.AgentToken = ""
	if err = s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	var cfg protocol.Config
	if err = s.get("settings", "config", &cfg); err == nil {
		if err = protocol.ValidateConfig(cfg); err != nil {
			db.Close()
			return nil, err
		}
		s.config = cfg
	}
	tasks, err := list[protocol.Task](s.db, "tasks")
	if err != nil {
		db.Close()
		return nil, err
	}
	for _, t := range tasks {
		if t.State == protocol.TaskRunning {
			t.State = protocol.Unknown
			t.ErrorCode = "controller_restart"
			t.ErrorMessage = "Controller restarted; query the existing Worker task before any new execution"
			if err = s.put("tasks", t.ID, t); err != nil {
				db.Close()
				return nil, err
			}
		}
	}
	return s, nil
}
func (s *Server) migrate() error {
	if _, err := s.db.Exec("PRAGMA foreign_keys=ON; PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000"); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("CREATE TABLE IF NOT EXISTS request_nonces(id TEXT PRIMARY KEY,expires_at INTEGER NOT NULL)"); err != nil {
		return err
	}
	if _, err = tx.Exec("CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY); CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY,data TEXT NOT NULL); CREATE TABLE IF NOT EXISTS devices(id TEXT PRIMARY KEY,data TEXT NOT NULL); CREATE TABLE IF NOT EXISTS plans(id TEXT PRIMARY KEY,data TEXT NOT NULL); CREATE TABLE IF NOT EXISTS approvals(id TEXT PRIMARY KEY,data TEXT NOT NULL); CREATE TABLE IF NOT EXISTS tasks(id TEXT PRIMARY KEY,session_id TEXT NOT NULL REFERENCES sessions(id),idempotency_key TEXT NOT NULL,data TEXT NOT NULL,UNIQUE(session_id,idempotency_key)); CREATE TABLE IF NOT EXISTS task_logs(id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id),data TEXT NOT NULL); CREATE TABLE IF NOT EXISTS audit_events(id TEXT PRIMARY KEY,data TEXT NOT NULL); CREATE TABLE IF NOT EXISTS cleanup_jobs(id TEXT PRIMARY KEY,data TEXT NOT NULL); CREATE TABLE IF NOT EXISTS settings(id TEXT PRIMARY KEY,data TEXT NOT NULL); INSERT OR IGNORE INTO schema_migrations(version) VALUES(1)"); err != nil {
		return err
	}
	return tx.Commit()
}

type queryer interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}
type executor interface {
	Exec(string, ...any) (sql.Result, error)
}

func get(q queryer, table, id string, out any) error {
	var b string
	if err := q.QueryRow("SELECT data FROM "+table+" WHERE id=?", id).Scan(&b); err != nil {
		return err
	}
	return json.Unmarshal([]byte(b), out)
}
func (s *Server) get(table, id string, out any) error { return get(s.db, table, id, out) }
func put(q executor, table, id string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if table == "tasks" {
		t := v.(protocol.Task)
		_, err = q.Exec("INSERT INTO tasks(id,session_id,idempotency_key,data) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", id, t.Spec.SessionID, t.IdempotencyKey, string(b))
		return err
	}
	if table == "task_logs" {
		return errors.New("use task log writer")
	}
	_, err = q.Exec("INSERT INTO "+table+"(id,data) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", id, string(b))
	return err
}
func (s *Server) put(table, id string, v any) error { return put(s.db, table, id, v) }
func list[T any](q queryer, table string) ([]T, error) {
	rows, err := q.Query("SELECT data FROM " + table + " ORDER BY rowid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var b string
		var v T
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(b), &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Server) audit(action, session, device, task, result string) {
	s.auditAs("system", action, session, device, task, result)
}
func (s *Server) auditAs(actor, action, session, device, task, result string) {
	a := protocol.AuditEvent{ID: security.NewID("audit"), Timestamp: s.opts.Now().UTC(), Actor: actor, Action: action, SessionID: session, DeviceID: device, TaskID: task, Result: security.Redact(result)}
	_ = s.put("audit_events", a.ID, a)
}
func equalToken(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(security.TokenDigest(a)), []byte(b)) == 1
}
func tailnetIP(s string) bool {
	ip, err := netip.ParseAddr(s)
	return err == nil && (netip.MustParsePrefix("100.64.0.0/10").Contains(ip) || netip.MustParsePrefix("fd7a:115c:a1e0::/48").Contains(ip))
}
func (s *Server) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil || s.closed {
		return
	}
	c, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.opts.TickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-c.Done():
				return
			case <-ticker.C:
				s.cycle(c)
			}
		}
	}()
}
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
	return s.db.Close()
}
func (s *Server) active(q queryer, id, device string) (sessionRecord, error) {
	var r sessionRecord
	if err := get(q, "sessions", id, &r); err != nil {
		return r, err
	}
	if !r.Session.State.Active() || !s.opts.Now().Before(r.Session.ExpiresAt) || s.opts.Now().Sub(r.Session.LastHeartbeat) > time.Duration(s.config.HeartbeatTTLSeconds)*time.Second || r.Session.DeviceID != device {
		return r, fmt.Errorf("session inactive or device binding invalid")
	}
	return r, nil
}
