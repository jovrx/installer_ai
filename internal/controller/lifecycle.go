package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"remoteops/internal/security"
	"remoteops/pkg/protocol"
	"strconv"
	"strings"
	"time"
)

func strict(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return errors.New("exactly one object required")
	}
	return nil
}
func (s *Server) rpc(ctx context.Context, rec sessionRecord, method, path string, in, out any) error {
	var d protocol.Device
	if err := s.get("devices", rec.Session.DeviceID, &d); err != nil {
		return err
	}
	if !tailnetIP(d.Address) {
		return errors.New("invalid stored peer address")
	}
	target := "http://" + net.JoinHostPort(d.Address, strconv.Itoa(protocol.WorkerPort)) + path
	return security.SignedJSON(ctx, s.opts.HTTPClient, method, target, rec.Token, "controller-to-worker", in, out)
}

// revokeLocked commits loss of authority before any best-effort network cleanup.
func (s *Server) revokeLocked(rec *sessionRecord, state protocol.SessionState, actor string) error {
	if rec.Session.ClosedAt != nil {
		return nil
	}
	now := s.opts.Now().UTC()
	rec.Session.State = state
	if rec.Session.NodeID != "" {
		rec.Session.State = protocol.CleanupPending
		rec.Session.CleanupError = "Node cleanup queued"
	}
	rec.Session.ClosedAt = &now
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = put(tx, "sessions", rec.Session.ID, *rec); err != nil {
		return err
	}
	tasks, err := list[protocol.Task](tx, "tasks")
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.Spec.SessionID == rec.Session.ID && !t.State.Terminal() {
			if t.State == protocol.Queued {
				t.State = protocol.Cancelled
				t.FinishedAt = &now
			} else {
				t.State = protocol.Unknown
				t.ErrorCode = "session_revoked"
				t.ErrorMessage = "Authority revoked; installation outcome may require local verification"
			}
			if err = put(tx, "tasks", t.ID, t); err != nil {
				return err
			}
		}
	}
	if rec.Session.DeviceID != "" {
		var d protocol.Device
		if get(tx, "devices", rec.Session.DeviceID, &d) == nil {
			d.Online = false
			if err = put(tx, "devices", d.ID, d); err != nil {
				return err
			}
		}
	}
	job := cleanupJob{SessionID: rec.Session.ID, Next: now}
	if rec.Session.NodeID != "" {
		if err = put(tx, "cleanup_jobs", rec.Session.ID, job); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	delete(s.verifiers, rec.Session.ID)
	delete(s.verifiers, "pair:"+rec.Session.ID)
	s.auditAs(actor, "session_revoked", rec.Session.ID, rec.Session.DeviceID, "", string(state))
	return nil
}

// cleanup performs slow network work without holding the management lock.
func (s *Server) cleanup(ctx context.Context, id string) {
	s.mu.Lock()
	var rec sessionRecord
	if s.closed || s.get("sessions", id, &rec) != nil || rec.Session.ClosedAt == nil {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	if rec.Token != "" && rec.Session.DeviceID != "" {
		_ = s.rpc(ctx, rec, "POST", "/v1/session/close", struct{}{}, nil)
	}
	if rec.Session.NodeID == "" {
		return
	}
	var err error
	if s.opts.NodeCleaner == nil {
		err = errors.New("Tailscale node cleanup is not configured")
	} else {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = s.opts.NodeCleaner.DeleteNode(c, rec.Session.NodeID)
		cancel()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.get("sessions", id, &rec) != nil {
		return
	}
	if err != nil {
		rec.Session.State = protocol.CleanupPending
		rec.Session.CleanupError = security.Redact(err.Error())
		var job cleanupJob
		_ = s.get("cleanup_jobs", id, &job)
		job.SessionID = id
		job.Attempts++
		job.Next = s.opts.Now().Add(time.Duration(1<<min(job.Attempts, 10)) * time.Second)
		_ = s.put("cleanup_jobs", id, job)
		s.audit("cleanup_failed", id, rec.Session.DeviceID, "", rec.Session.CleanupError)
	} else {
		rec.Session.State = protocol.Closed
		rec.Session.CleanupError = ""
		rec.Token = ""
		_, _ = s.db.Exec("DELETE FROM cleanup_jobs WHERE id=?", id)
		s.audit("cleanup_completed", id, rec.Session.DeviceID, "", "success")
	}
	_ = s.put("sessions", id, rec)
}
func (s *Server) cancelTask(w http.ResponseWriter, r *http.Request, id string) {
	var empty struct{}
	if !decodeBody(w, r, &empty) {
		return
	}
	var t protocol.Task
	if err := s.get("tasks", id, &t); err != nil {
		internal(w, err)
		return
	}
	if t.State.Terminal() {
		write(w, 200, t)
		return
	}
	if t.State == protocol.Queued {
		now := s.opts.Now().UTC()
		t.State = protocol.Cancelled
		t.FinishedAt = &now
	} else {
		var rec sessionRecord
		if s.get("sessions", t.Spec.SessionID, &rec) != nil {
			fail(w, 409, "session_inactive", "Session unavailable")
			return
		}
		// Polling cannot overwrite a concurrent cancellation request.
		t.State = protocol.Unknown
		t.ErrorCode = "cancel_requested"
		t.ErrorMessage = "Cancellation requested; awaiting Worker result"
		if err := s.put("tasks", t.ID, t); err != nil {
			internal(w, err)
			return
		}
		s.mu.Unlock()
		var result protocol.Task
		err := s.rpc(r.Context(), rec, "POST", "/v1/tasks/"+url.PathEscape(id)+"/cancel", struct{}{}, &result)
		s.mu.Lock()
		if s.get("tasks", id, &t) != nil {
			fail(w, 500, "internal_error", "Task disappeared")
			return
		}
		if t.ErrorCode == "cancel_requested" {
			if err == nil && validateResult(t, result) == nil {
				applyResult(&t, result)
			} else {
				t.State = protocol.Unknown
				t.ErrorCode = "cancel_unconfirmed"
				t.ErrorMessage = "Cancellation could not be confirmed"
			}
		}
	}
	if err := s.put("tasks", t.ID, t); err != nil {
		internal(w, err)
		return
	}
	s.auditAs(actorOf(r), "task_cancel", t.Spec.SessionID, t.Spec.DeviceID, t.ID, string(t.State))
	write(w, 200, t)
}
func validateResult(original, result protocol.Task) error {
	if result.ID != original.ID || result.Digest != original.Digest || protocol.TaskDigest(result.Spec) != original.Digest {
		return errors.New("Worker task identity or digest mismatch")
	}
	switch result.State {
	case protocol.Queued, protocol.TaskRunning, protocol.Succeeded, protocol.TaskFailed, protocol.Cancelled, protocol.TimedOut, protocol.Unknown:
		return nil
	}
	return fmt.Errorf("invalid Worker task state")
}
func applyResult(t *protocol.Task, r protocol.Task) {
	t.State = r.State
	// A Worker queue acknowledgement refers to the already dispatched task.
	// Preserve that fact so recovery never sends a second execution request.
	if t.State == protocol.Queued {
		t.State = protocol.TaskRunning
	}
	if r.StartedAt != nil {
		t.StartedAt = r.StartedAt
	}
	t.FinishedAt = r.FinishedAt
	t.Result = r.Result
	t.ErrorCode = security.Redact(r.ErrorCode)
	t.ErrorMessage = security.Redact(r.ErrorMessage)
}
func (s *Server) cycle(ctx context.Context) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	now := s.opts.Now().UTC()
	sessions, err := list[sessionRecord](s.db, "sessions")
	if err != nil {
		s.mu.Unlock()
		return
	}
	for _, rec := range sessions {
		if rec.Session.State == protocol.PendingPair && !now.Before(rec.Session.PairingExpiresAt) {
			rec.Session.State = protocol.Expired
			_ = s.put("sessions", rec.Session.ID, rec)
			delete(s.verifiers, "pair:"+rec.Session.ID)
			s.audit("pairing_expired", rec.Session.ID, "", "", "expired")
		}
		if rec.Session.State.Active() && (!now.Before(rec.Session.ExpiresAt) || now.Sub(rec.Session.LastHeartbeat) > time.Duration(s.config.HeartbeatTTLSeconds)*time.Second) {
			_ = s.revokeLocked(&rec, protocol.Expired, "system")
		}
	}
	jobs, _ := list[cleanupJob](s.db, "cleanup_jobs")
	tasks, _ := list[protocol.Task](s.db, "tasks")
	s.retention(now)
	s.mu.Unlock()
	for _, t := range tasks {
		if ctx.Err() != nil {
			return
		}
		s.processTask(ctx, t.ID)
	}
	for _, job := range jobs {
		if ctx.Err() != nil {
			return
		}
		if !now.Before(job.Next) {
			s.cleanup(ctx, job.SessionID)
		}
	}
}
func (s *Server) processTask(ctx context.Context, id string) {
	s.mu.Lock()
	var t protocol.Task
	if s.closed || s.get("tasks", id, &t) != nil {
		s.mu.Unlock()
		return
	}
	rec, err := s.active(s.db, t.Spec.SessionID, t.Spec.DeviceID)
	if err != nil {
		s.mu.Unlock()
		return
	}
	if t.State.Terminal() {
		s.mu.Unlock()
		s.syncLogs(ctx, t, rec)
		return
	}
	if t.State != protocol.Queued && t.State != protocol.TaskRunning && t.State != protocol.Unknown {
		s.mu.Unlock()
		return
	}
	dispatching := t.State == protocol.Queued
	method, path := "GET", "/v1/tasks/"+url.PathEscape(id)
	var in any
	if dispatching {
		others, _ := list[protocol.Task](s.db, "tasks")
		count := 0
		for _, other := range others {
			if other.State == protocol.TaskRunning || other.State == protocol.Unknown {
				var owner sessionRecord
				if s.get("sessions", other.Spec.SessionID, &owner) == nil && owner.Session.State.Active() {
					count++
					if other.Spec.DeviceID == t.Spec.DeviceID {
						s.mu.Unlock()
						return
					}
				}
			}
		}
		if count >= s.config.MaxConcurrency {
			s.mu.Unlock()
			return
		}
		now := s.opts.Now().UTC()
		t.State = protocol.TaskRunning
		t.StartedAt = &now
		if s.put("tasks", id, t) != nil {
			s.mu.Unlock()
			return
		}
		rec.Session.State = protocol.Running
		_ = s.put("sessions", rec.Session.ID, rec)
		d := protocol.TaskDispatch{Task: t}
		if t.Spec.Type == protocol.InstallPackage {
			d.ApprovalDigest = t.Digest
		}
		method, path, in = "POST", "/v1/tasks", d
	} else if t.State == protocol.TaskRunning && t.StartedAt != nil && s.opts.Now().Sub(*t.StartedAt) > time.Duration(t.Spec.TimeoutSeconds)*time.Second {
		method, path, in = "POST", path+"/cancel", struct{}{}
	}
	original := t
	s.mu.Unlock()
	var result protocol.Task
	err = s.rpc(ctx, rec, method, path, in, &result)
	if err == nil {
		err = validateResult(original, result)
	}
	s.mu.Lock()
	if s.closed || s.get("tasks", id, &t) != nil {
		s.mu.Unlock()
		return
	}
	// A completed task, revocation, or cancellation wins over an in-flight stale poll.
	currentSession, activeErr := s.active(s.db, t.Spec.SessionID, t.Spec.DeviceID)
	if activeErr != nil || t.State.Terminal() || t.ErrorCode == "cancel_requested" {
		s.mu.Unlock()
		return
	}
	rec = currentSession
	if err != nil {
		t.State = protocol.Unknown
		t.ErrorCode = "poll_unconfirmed"
		if dispatching {
			t.ErrorCode = "dispatch_unconfirmed"
		}
		t.ErrorMessage = "Worker outcome unavailable; only polling this task ID is allowed"
	} else {
		applyResult(&t, result)
	}
	if t.State.Terminal() {
		now := s.opts.Now().UTC()
		if t.FinishedAt == nil {
			t.FinishedAt = &now
		}
		rec.Session.State = protocol.Online
		_ = s.put("sessions", rec.Session.ID, rec)
		s.audit("task_finished", t.Spec.SessionID, t.Spec.DeviceID, t.ID, string(t.State))
	}
	_ = s.put("tasks", id, t)
	if t.Result != nil && t.Result.Info != nil {
		var d protocol.Device
		if s.get("devices", t.Spec.DeviceID, &d) == nil {
			d.Info = t.Result.Info
			_ = s.put("devices", d.ID, d)
		}
	}
	s.mu.Unlock()
	s.syncLogs(ctx, t, rec)
}
func (s *Server) syncLogs(ctx context.Context, t protocol.Task, rec sessionRecord) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	var done bool
	_ = s.get("settings", "logs_done:"+t.ID, &done)
	if done {
		s.mu.Unlock()
		return
	}
	var stored protocol.LogsPage
	_ = s.get("task_logs", t.ID, &stored)
	s.mu.Unlock()
	var page protocol.LogsPage
	if s.rpc(ctx, rec, "GET", "/v1/tasks/"+url.PathEscape(t.ID)+"/logs?after="+strconv.FormatInt(stored.NextAfter, 10)+"&limit=500", nil, &page) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	_ = s.get("task_logs", t.ID, &stored)
	s.appendLogs(t.ID, rec.Token, &stored, page)
	if t.State.Terminal() && len(page.Entries) < 500 {
		_ = s.put("settings", "logs_done:"+t.ID, true)
	}
}
func (s *Server) appendLogs(id, token string, stored *protocol.LogsPage, page protocol.LogsPage) {
	if stored.Entries == nil {
		stored.Entries = []protocol.LogEntry{}
	}
	stored.Truncated = stored.Truncated || page.Truncated
	last := stored.NextAfter
	for _, e := range page.Entries {
		if e.Sequence <= last {
			continue
		}
		last = e.Sequence
		e.Message = security.Redact(strings.ReplaceAll(e.Message, token, "[REDACTED]"))
		e.Stream = security.Redact(e.Stream)
		candidate := append(append([]protocol.LogEntry{}, stored.Entries...), e)
		b, _ := json.Marshal(candidate)
		if len(b) > s.config.LogMaxBytes {
			stored.Truncated = true
			continue
		}
		stored.Entries = candidate
	}
	stored.NextAfter = max(last, page.NextAfter)
	b, _ := json.Marshal(stored)
	_, _ = s.db.Exec("INSERT INTO task_logs(id,task_id,data) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", id, id, string(b))
}
func (s *Server) retention(now time.Time) {
	_, _ = s.db.Exec("DELETE FROM request_nonces WHERE expires_at < ?", time.Now().Unix())
	cutoff := now.AddDate(0, 0, -s.config.RetentionDays)
	events, _ := list[protocol.AuditEvent](s.db, "audit_events")
	for _, e := range events {
		if e.Timestamp.Before(cutoff) {
			_, _ = s.db.Exec("DELETE FROM audit_events WHERE id=?", e.ID)
		}
	}
	tasks, _ := list[protocol.Task](s.db, "tasks")
	for _, t := range tasks {
		if t.FinishedAt != nil && t.FinishedAt.Before(cutoff) {
			_, _ = s.db.Exec("DELETE FROM task_logs WHERE task_id=?", t.ID)
		}
	}
	for k, c := range s.cookies {
		if !now.Before(c.Expires) {
			delete(s.cookies, k)
		}
	}
}

// TailscaleNodeCleaner deletes only identities bound by the trusted peer resolver.
type TailscaleNodeCleaner struct {
	APIKey     string
	HTTPClient *http.Client
}

func (c TailscaleNodeCleaner) DeleteNode(ctx context.Context, id string) error {
	if c.APIKey == "" || id == "" {
		return errors.New("node cleanup credentials missing")
	}
	req, err := http.NewRequestWithContext(ctx, "DELETE", "https://api.tailscale.com/api/v2/device/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.APIKey, "")
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("Tailscale node deletion transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 || resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("Tailscale node deletion returned HTTP %d", resp.StatusCode)
}
