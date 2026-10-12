package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"remoteops/internal/security"
	"remoteops/pkg/protocol"
	"runtime"
	"strings"
	"time"
)

func ValidatePairing(b protocol.PairingBundle) error {
	if b.AuthorizationSeconds < 60 || b.AuthorizationSeconds > 86400 {
		return errors.New("pairing must declare authorization duration between 60 and 86400 seconds")
	}
	if b.ProtocolVersion != protocol.Version || b.SessionID == "" || b.PairingToken == "" || b.ControllerName == "" || !time.Now().Before(b.ExpiresAt) || b.CatalogDigest != protocol.Digest(b.Catalog) {
		return errors.New("invalid or expired pairing bundle")
	}
	u, err := url.Parse(b.ControllerURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("invalid Controller address")
	}
	if err = protocol.ValidateCatalog(b.Catalog); err != nil {
		return err
	}
	return protocol.ValidateScope(b.Scope, b.Catalog)
}
func signedCall(ctx context.Context, c *http.Client, base, path, secret string, value any) ([]byte, int, error) {
	body, _ := json.Marshal(value)
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(base, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if err = security.SignRequest(req, body, secret, "worker-to-controller"); err != nil {
		return nil, 0, err
	}
	safe := *c
	safe.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("Controller redirect refused") }
	resp, err := safe.Do(req)
	if err != nil {
		return nil, 0, errors.New("Controller request failed")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxBodyBytes+1))
	if err != nil || len(b) > protocol.MaxBodyBytes {
		return nil, 0, errors.New("invalid Controller response size")
	}
	if err = security.VerifyResponse(resp, b, secret, "worker-to-controller", req.Header.Get(security.HeaderNonce)); err != nil {
		return nil, 0, err
	}
	return b, resp.StatusCode, nil
}
func RegisterPairing(ctx context.Context, c *http.Client, b protocol.PairingBundle) (protocol.RegisterResponse, error) {
	var reg protocol.RegisterResponse
	if err := ValidatePairing(b); err != nil {
		return reg, err
	}
	host, _ := os.Hostname()
	started := time.Now()
	body, status, err := signedCall(ctx, c, b.ControllerURL, "/api/v1/worker/register", b.PairingToken, protocol.RegisterRequest{ProtocolVersion: protocol.Version, SessionID: b.SessionID, PairingToken: b.PairingToken, Hostname: host, OS: runtime.GOOS, ClientVersion: "remoteops-mvp-v1", Port: protocol.WorkerPort})
	if err != nil {
		return reg, err
	}
	if status != 200 && status != 201 {
		return reg, errors.New("Controller rejected pairing")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&reg); err != nil || dec.Decode(new(any)) != io.EOF {
		return reg, errors.New("invalid registration response")
	}
	if reg.Session.ID != b.SessionID || reg.Device.SessionID != b.SessionID || reg.Device.ID == "" || reg.Session.DeviceID != reg.Device.ID || reg.SessionToken == "" || !reg.Session.State.Active() || !time.Now().Before(reg.Session.ExpiresAt) || reg.Session.ExpiresAt.After(time.Now().Add(24*time.Hour)) || protocol.Digest(reg.Session.Scope) != protocol.Digest(b.Scope) || protocol.Digest(reg.Catalog) != b.CatalogDigest {
		return protocol.RegisterResponse{}, errors.New("registration identity or consent mismatch")
	}
	// A small skew allowance permits clock differences without widening local consent.
	localDeadline := started.Add(time.Duration(b.AuthorizationSeconds) * time.Second)
	if reg.Session.ExpiresAt.After(localDeadline.Add(60 * time.Second)) {
		return protocol.RegisterResponse{}, errors.New("registration exceeds consented authorization duration")
	}
	if reg.Session.ExpiresAt.After(localDeadline) {
		reg.Session.ExpiresAt = localDeadline
	}
	return reg, nil
}

// Heartbeats do not extend local authorization. Loss of contact is enforced by Server.
func (s *Server) Heartbeats(ctx context.Context, c *http.Client, controllerURL string) {
	timer := time.NewTicker(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.Done():
			return
		case <-timer.C:
			requestCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			body, status, err := signedCall(requestCtx, c, controllerURL, "/api/v1/worker/heartbeat", s.opt.SessionToken, protocol.HeartbeatRequest{SessionID: s.opt.Session.ID, DeviceID: s.opt.DeviceID})
			cancel()
			if err != nil {
				continue
			}
			if status < 200 || status >= 300 {
				s.CloseWithReason("controller_closed")
				return
			}
			var session protocol.Session
			if json.Unmarshal(body, &session) != nil || session.ID != s.opt.Session.ID || session.DeviceID != s.opt.DeviceID || !session.State.Active() || !time.Now().Before(session.ExpiresAt) || protocol.Digest(session.Scope) != protocol.Digest(s.opt.Session.Scope) {
				s.CloseWithReason("controller_closed")
				return
			}
			s.TouchController()
		}
	}
}
func (s *Server) NotifyClose(ctx context.Context, c *http.Client, controllerURL string) error {
	_, _, err := signedCall(ctx, c, controllerURL, "/api/v1/worker/close", s.opt.SessionToken, protocol.HeartbeatRequest{SessionID: s.opt.Session.ID, DeviceID: s.opt.DeviceID})
	return err
}
func (s *Server) LocalTasks() []protocol.Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks := make([]protocol.Task, 0, len(s.tasks))
	for _, r := range s.tasks {
		b, _ := json.Marshal(r.task)
		var task protocol.Task
		json.Unmarshal(b, &task)
		tasks = append(tasks, task)
	}
	return tasks
}
