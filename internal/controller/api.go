package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"remoteops/internal/security"
	"remoteops/pkg/protocol"
	"strconv"
	"strings"
	"time"
)

type actorContextKey struct{}

func actorOf(r *http.Request) string {
	actor, _ := r.Context().Value(actorContextKey{}).(string)
	if actor == "" {
		return "operator"
	}
	return actor
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, code, msg string) {
	write(w, status, protocol.ErrorEnvelope{Error: protocol.APIError{Code: code, Message: security.Redact(msg), RequestID: security.NewID("req")}})
}
func internal(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, 404, "not_found", "Resource not found")
	} else {
		fail(w, 500, "internal_error", "Database operation failed")
	}
}
func decodeBody(w http.ResponseWriter, r *http.Request, out any) bool {
	if err := security.DecodeJSON(w, r, out); err != nil {
		fail(w, 400, "invalid_request", "Invalid JSON body")
		return false
	}
	return true
}
func sameOrigin(r *http.Request, required bool) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return !required
	}
	u, err := url.Parse(origin)
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return err == nil && u.Scheme == scheme && u.Host == r.Host && u.Path == "" && u.RawQuery == "" && u.Fragment == "" && u.User == nil
}
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.manage) }
func (s *Server) manage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		fail(w, 503, "unavailable", "Controller stopped")
		return
	}
	if r.URL.Path == "/api/v1/auth/login" && r.Method == "POST" {
		if !sameOrigin(r, false) {
			fail(w, 403, "origin_rejected", "Origin rejected")
			return
		}
		var in protocol.LoginRequest
		if !decodeBody(w, r, &in) {
			return
		}
		if !equalToken(in.Token, s.adminDigest) {
			fail(w, 401, "unauthorized", "Invalid management token")
			return
		}
		now := s.opts.Now()
		for k, c := range s.cookies {
			if !now.Before(c.Expires) {
				delete(s.cookies, k)
			}
		}
		if len(s.cookies) >= 1000 {
			fail(w, 429, "session_limit", "Too many browser sessions")
			return
		}
		token, csrf := security.NewID("browser"), security.NewID("csrf")
		s.cookies[security.TokenDigest(token)] = browserSession{csrf, now.Add(time.Hour)}
		http.SetCookie(w, &http.Cookie{Name: "remoteops_session", Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 3600})
		s.auditAs("operator", "login", "", "", "", "success")
		write(w, 200, protocol.LoginResponse{CSRFToken: csrf})
		return
	}
	cookieAuth := false
	if c, err := r.Cookie("remoteops_session"); err == nil {
		if b, ok := s.cookies[security.TokenDigest(c.Value)]; ok && s.opts.Now().Before(b.Expires) {
			cookieAuth = true
			if r.Method != "GET" && r.Method != "HEAD" {
				if !sameOrigin(r, true) || r.Header.Get("X-CSRF-Token") != b.CSRF {
					fail(w, 403, "csrf_rejected", "Origin and CSRF token required")
					return
				}
			}
		}
	}
	authorization := r.Header.Get("Authorization")
	bearerToken := strings.TrimPrefix(authorization, "Bearer ")
	bearerAuth := strings.HasPrefix(authorization, "Bearer ") && (equalToken(bearerToken, s.adminDigest) || (s.agentDigest != "" && equalToken(bearerToken, s.agentDigest)))
	if !cookieAuth && !bearerAuth {
		fail(w, 401, "unauthorized", "Management authentication required")
		return
	}
	actor := "operator"
	if !cookieAuth && s.agentDigest != "" && equalToken(bearerToken, s.agentDigest) {
		actor = "agent"
	}
	r = r.WithContext(context.WithValue(r.Context(), actorContextKey{}, actor))
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/")
	parts := strings.Split(path, "/")
	if path == "network-status" && r.Method == "GET" {
		s.mu.Unlock()
		status := s.networkStatus(r.Context())
		s.mu.Lock()
		write(w, 200, status)
		return
	}
	if path == "auth/logout" && r.Method == "POST" {
		if c, err := r.Cookie("remoteops_session"); err == nil {
			delete(s.cookies, security.TokenDigest(c.Value))
		}
		http.SetCookie(w, &http.Cookie{Name: "remoteops_session", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		write(w, 200, struct{}{})
		return
	}
	if path == "config" {
		if r.Method == "GET" {
			write(w, 200, s.config)
			return
		}
		if r.Method == "PUT" {
			var c protocol.Config
			if !decodeBody(w, r, &c) {
				return
			}
			if err := protocol.ValidateConfig(c); err != nil {
				fail(w, 400, "invalid_config", err.Error())
				return
			}
			if err := s.put("settings", "config", c); err != nil {
				internal(w, err)
				return
			}
			s.config = c
			s.auditAs(actorOf(r), "config_updated", "", "", "", "success")
			write(w, 200, c)
			return
		}
	}
	if path == "catalog" && r.Method == "GET" {
		write(w, 200, s.opts.Catalog)
		return
	}
	if len(parts) == 1 && r.Method == "GET" {
		switch path {
		case "sessions":
			v, err := list[sessionRecord](s.db, "sessions")
			if err != nil {
				internal(w, err)
				return
			}
			out := []protocol.Session{}
			for _, x := range v {
				out = append(out, x.Session)
			}
			write(w, 200, out)
			return
		case "devices":
			respondList[protocol.Device](w, s, "devices")
			return
		case "tasks":
			respondList[protocol.Task](w, s, "tasks")
			return
		case "plans":
			respondList[protocol.Plan](w, s, "plans")
			return
		case "approvals":
			respondList[protocol.Approval](w, s, "approvals")
			return
		case "audit-events":
			respondList[protocol.AuditEvent](w, s, "audit_events")
			return
		}
	}
	if path == "sessions" && r.Method == "POST" {
		s.createSession(w, r)
		return
	}
	if path == "plans" && r.Method == "POST" {
		s.createPlan(w, r)
		return
	}
	if path == "approvals" && r.Method == "POST" {
		s.requestApproval(w, r)
		return
	}
	if path == "tasks" && r.Method == "POST" {
		s.createTask(w, r)
		return
	}
	if len(parts) == 2 && r.Method == "GET" {
		switch parts[0] {
		case "sessions":
			var v sessionRecord
			if err := s.get("sessions", parts[1], &v); err != nil {
				internal(w, err)
				return
			}
			write(w, 200, v.Session)
			return
		case "devices":
			respondGet[protocol.Device](w, s, "devices", parts[1])
			return
		case "tasks":
			respondGet[protocol.Task](w, s, "tasks", parts[1])
			return
		}
	}
	if len(parts) == 3 {
		id, action := parts[1], parts[2]
		if r.Method == "POST" {
			switch parts[0] {
			case "approvals":
				if action == "approve" || action == "reject" {
					if !cookieAuth {
						fail(w, 403, "operator_confirmation_required", "Approval requires a browser operator session")
						return
					}
					s.decideApproval(w, r, id, action)
					return
				}
			case "sessions":
				if action == "pairing" {
					if !cookieAuth {
						fail(w, 403, "operator_confirmation_required", "Pairing regeneration requires a browser operator session")
						return
					}
					s.regeneratePairing(w, r, id)
					return
				}
				if action == "close" || action == "retry-cleanup" {
					var empty struct{}
					if !decodeBody(w, r, &empty) {
						return
					}
					var rec sessionRecord
					if err := s.get("sessions", id, &rec); err != nil {
						internal(w, err)
						return
					}
					if action == "close" {
						if err := s.revokeLocked(&rec, protocol.Closed, actorOf(r)); err != nil {
							internal(w, err)
							return
						}
					} else if rec.Session.State != protocol.CleanupPending {
						fail(w, 409, "cleanup_not_pending", "Session has no pending cleanup")
						return
					}
					job := cleanupJob{SessionID: rec.Session.ID, Next: s.opts.Now()}
					if rec.Session.NodeID != "" {
						_ = s.put("cleanup_jobs", rec.Session.ID, job)
					}
					write(w, 200, rec.Session)
					return
				}
			case "tasks":
				if action == "cancel" {
					s.cancelTask(w, r, id)
					return
				}
			}
		}
		if parts[0] == "tasks" && action == "logs" && r.Method == "GET" {
			s.taskLogs(w, r, id)
			return
		}
	}
	fail(w, 404, "not_found", "Endpoint not found")
}
func respondList[T any](w http.ResponseWriter, s *Server, table string) {
	v, err := list[T](s.db, table)
	if err != nil {
		internal(w, err)
		return
	}
	write(w, 200, v)
}
func respondGet[T any](w http.ResponseWriter, s *Server, table, id string) {
	var v T
	if err := s.get(table, id, &v); err != nil {
		internal(w, err)
		return
	}
	write(w, 200, v)
}
func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var in protocol.CreateSessionRequest
	if !decodeBody(w, r, &in) {
		return
	}
	if err := protocol.ValidateScope(in.Scope, s.opts.Catalog); err != nil {
		fail(w, 400, "invalid_scope", err.Error())
		return
	}
	now := s.opts.Now().UTC()
	token := security.NewID("pair")
	session := protocol.Session{ID: security.NewID("session"), Operator: "operator", ControllerName: s.opts.ControllerName, Scope: in.Scope, State: protocol.PendingPair, CreatedAt: now, PairingExpiresAt: now.Add(time.Duration(s.config.PairingTTLSeconds) * time.Second)}
	rec := sessionRecord{Session: session, PairingDigest: security.TokenDigest(token), Catalog: s.opts.Catalog, AuthorizationSeconds: s.config.SessionTTLSeconds}
	if err := s.put("sessions", session.ID, rec); err != nil {
		internal(w, err)
		return
	}
	s.auditAs(actorOf(r), "session_created", session.ID, "", "", "success")
	write(w, 201, protocol.CreateSessionResponse{Session: session, Pairing: protocol.PairingBundle{ProtocolVersion: protocol.Version, ControllerName: s.opts.ControllerName, ControllerURL: s.opts.ControllerURL, SessionID: session.ID, PairingToken: token, ExpiresAt: session.PairingExpiresAt, Scope: in.Scope, Catalog: rec.Catalog, CatalogDigest: protocol.Digest(rec.Catalog), AuthorizationSeconds: rec.AuthorizationSeconds}})
}
func (s *Server) regeneratePairing(w http.ResponseWriter, r *http.Request, id string) {
	var empty struct{}
	if !decodeBody(w, r, &empty) {
		return
	}
	var rec sessionRecord
	if err := s.get("sessions", id, &rec); err != nil {
		internal(w, err)
		return
	}
	if rec.Session.State != protocol.PendingPair || !s.opts.Now().Before(rec.Session.PairingExpiresAt) {
		fail(w, 409, "pairing_unavailable", "Only unexpired sessions waiting for pairing can regenerate a pairing file")
		return
	}
	token := security.NewID("pair")
	rec.PairingDigest = security.TokenDigest(token)
	if err := s.put("sessions", id, rec); err != nil {
		internal(w, err)
		return
	}
	delete(s.verifiers, "pair:"+id)
	s.auditAs(actorOf(r), "pairing_regenerated", id, "", "", "success")
	write(w, 200, protocol.PairingBundle{ProtocolVersion: protocol.Version, ControllerName: rec.Session.ControllerName, ControllerURL: s.opts.ControllerURL, SessionID: id, PairingToken: token, ExpiresAt: rec.Session.PairingExpiresAt, Scope: rec.Session.Scope, Catalog: rec.Catalog, CatalogDigest: protocol.Digest(rec.Catalog), AuthorizationSeconds: rec.AuthorizationSeconds})
}
func (s *Server) validateSpec(spec *protocol.TaskSpec) (sessionRecord, error) {
	if spec.TimeoutSeconds == 0 {
		spec.TimeoutSeconds = s.config.TaskTimeoutSeconds
	}
	rec, err := s.active(s.db, spec.SessionID, spec.DeviceID)
	if err != nil {
		return rec, err
	}
	return rec, protocol.ValidateSpec(*spec, rec.Session.Scope, rec.Catalog)
}
func (s *Server) createPlan(w http.ResponseWriter, r *http.Request) {
	var spec protocol.TaskSpec
	if !decodeBody(w, r, &spec) {
		return
	}
	rec, err := s.validateSpec(&spec)
	if err != nil {
		fail(w, 409, "invalid_task", err.Error())
		return
	}
	now := s.opts.Now().UTC()
	p := protocol.Plan{ID: security.NewID("plan"), Spec: spec, Digest: protocol.TaskDigest(spec), CatalogDigest: protocol.Digest(rec.Catalog), Steps: []string{"validate_authorization", "inspect_system"}, CreatedAt: now, ExpiresAt: rec.Session.ExpiresAt}
	if spec.Type == protocol.InstallPackage {
		p.Steps = []string{"validate_authorization", "verify_artifact", "install_pinned_package", "verify_version"}
	}
	if err = s.put("plans", p.ID, p); err != nil {
		internal(w, err)
		return
	}
	s.auditAs(actorOf(r), "plan_created", spec.SessionID, spec.DeviceID, "", "success")
	write(w, 201, p)
}
func (s *Server) requestApproval(w http.ResponseWriter, r *http.Request) {
	var in protocol.CreateApprovalRequest
	if !decodeBody(w, r, &in) {
		return
	}
	var p protocol.Plan
	if err := s.get("plans", in.PlanID, &p); err != nil {
		internal(w, err)
		return
	}
	rec, err := s.active(s.db, p.Spec.SessionID, p.Spec.DeviceID)
	if err != nil || !s.opts.Now().Before(p.ExpiresAt) || p.CatalogDigest != protocol.Digest(rec.Catalog) {
		fail(w, 409, "plan_expired", "Plan authority expired")
		return
	}
	a := protocol.Approval{ID: security.NewID("approval"), PlanID: p.ID, SessionID: p.Spec.SessionID, DeviceID: p.Spec.DeviceID, Digest: p.Digest, Status: "pending", CreatedAt: s.opts.Now().UTC(), ExpiresAt: p.ExpiresAt}
	if err = s.put("approvals", a.ID, a); err != nil {
		internal(w, err)
		return
	}
	s.auditAs(actorOf(r), "approval_requested", a.SessionID, a.DeviceID, "", "pending")
	write(w, 201, a)
}
func (s *Server) decideApproval(w http.ResponseWriter, r *http.Request, id, action string) {
	var empty struct{}
	if !decodeBody(w, r, &empty) {
		return
	}
	var a protocol.Approval
	if err := s.get("approvals", id, &a); err != nil {
		internal(w, err)
		return
	}
	if _, err := s.active(s.db, a.SessionID, a.DeviceID); err != nil || a.Status != "pending" || !s.opts.Now().Before(a.ExpiresAt) {
		fail(w, 409, "approval_inactive", "Approval inactive")
		return
	}
	a.Status = "approved"
	if action == "reject" {
		a.Status = "rejected"
	}
	a.Operator = "web_operator"
	if err := s.put("approvals", a.ID, a); err != nil {
		internal(w, err)
		return
	}
	s.auditAs(actorOf(r), "approval_"+a.Status, a.SessionID, a.DeviceID, "", "success")
	write(w, 200, a)
}
func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var in protocol.CreateTaskRequest
	if !decodeBody(w, r, &in) {
		return
	}
	if !protocol.ValidIdempotencyKey(in.IdempotencyKey) {
		fail(w, 400, "invalid_idempotency_key", "Idempotency key must contain 8 to 128 characters")
		return
	}
	if _, err := s.validateSpec(&in.Spec); err != nil {
		fail(w, 409, "session_inactive", err.Error())
		return
	}
	digest := protocol.TaskDigest(in.Spec)
	tx, err := s.db.Begin()
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRow("SELECT data FROM tasks WHERE session_id=? AND idempotency_key=?", in.Spec.SessionID, in.IdempotencyKey).Scan(&existing)
	if err == nil {
		var t protocol.Task
		json.Unmarshal([]byte(existing), &t)
		if t.Digest != digest {
			fail(w, 409, "idempotency_conflict", "Same key has different execution parameters")
			return
		}
		write(w, 201, t)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		internal(w, err)
		return
	}
	now := s.opts.Now().UTC()
	if in.Spec.Type == protocol.InstallPackage {
		var a protocol.Approval
		if get(tx, "approvals", in.ApprovalID, &a) != nil || a.Status != "approved" || a.ConsumedAt != nil || a.SessionID != in.Spec.SessionID || a.DeviceID != in.Spec.DeviceID || a.Digest != digest || !now.Before(a.ExpiresAt) {
			fail(w, 403, "approval_required", "Valid unconsumed approval for these exact parameters required")
			return
		}
		var p protocol.Plan
		var rec sessionRecord
		if get(tx, "plans", a.PlanID, &p) != nil || get(tx, "sessions", a.SessionID, &rec) != nil || p.Digest != digest || p.CatalogDigest != protocol.Digest(rec.Catalog) {
			fail(w, 403, "approval_mismatch", "Approval catalog or parameters changed")
			return
		}
		a.ConsumedAt = &now
		a.Status = "consumed"
		if err = put(tx, "approvals", a.ID, a); err != nil {
			internal(w, err)
			return
		}
	}
	t := protocol.Task{ID: security.NewID("task"), Spec: in.Spec, State: protocol.Queued, ApprovalID: in.ApprovalID, IdempotencyKey: in.IdempotencyKey, Digest: digest, CreatedAt: now}
	if err = put(tx, "tasks", t.ID, t); err != nil {
		internal(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		internal(w, err)
		return
	}
	s.auditAs(actorOf(r), "task_created", t.Spec.SessionID, t.Spec.DeviceID, t.ID, "queued")
	write(w, 201, t)
}
func (s *Server) taskLogs(w http.ResponseWriter, r *http.Request, id string) {
	var t protocol.Task
	if err := s.get("tasks", id, &t); err != nil {
		internal(w, err)
		return
	}
	after := int64(0)
	limit := 100
	var err error
	if v := r.URL.Query().Get("after"); v != "" {
		after, err = strconv.ParseInt(v, 10, 64)
		if err != nil || after < 0 {
			fail(w, 400, "invalid_cursor", "Invalid log cursor")
			return
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, err = strconv.Atoi(v)
		if err != nil || limit < 1 || limit > 500 {
			fail(w, 400, "invalid_limit", "Log limit must be 1 through 500")
			return
		}
	}
	var page protocol.LogsPage
	if err = s.get("task_logs", id, &page); errors.Is(err, sql.ErrNoRows) {
		page.Entries = []protocol.LogEntry{}
	} else if err != nil {
		internal(w, err)
		return
	}
	out := protocol.LogsPage{Entries: []protocol.LogEntry{}, NextAfter: after, Truncated: page.Truncated}
	for _, e := range page.Entries {
		if e.Sequence > after && len(out.Entries) < limit {
			out.Entries = append(out.Entries, e)
			out.NextAfter = e.Sequence
		}
	}
	write(w, 200, out)
}
