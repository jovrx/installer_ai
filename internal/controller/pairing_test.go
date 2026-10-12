package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"remoteops/internal/security"
	"remoteops/pkg/protocol"
	"strings"
	"testing"
	"time"
)

func TestRegeneratePairing(t *testing.T) {
	s := newTestServer(t, ":memory:")
	login := call(s.Handler(), "POST", "/api/v1/auth/login", protocol.LoginRequest{Token: admin}, nil, "")
	csrf := decode[protocol.LoginResponse](t, login, 200).CSRFToken
	cookie := login.Result().Cookies()[0]
	created := decode[protocol.CreateSessionResponse](t, call(s.Handler(), "POST", "/api/v1/sessions", protocol.CreateSessionRequest{Scope: protocol.Scope{AllowedTasks: []protocol.TaskType{protocol.InspectSystem}, AllowedPackages: []string{}}}, cookie, csrf), 201)
	path := "/api/v1/sessions/" + created.Session.ID + "/pairing"
	register := func(token, version string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(protocol.RegisterRequest{ProtocolVersion: version, SessionID: created.Session.ID, PairingToken: token, Hostname: "win", OS: "windows", Port: protocol.WorkerPort})
		r := httptest.NewRequest("POST", "http://localhost/api/v1/worker/register", bytes.NewReader(b))
		security.SignRequest(r, b, token, "worker-to-controller")
		w := httptest.NewRecorder()
		s.WorkerHandler().ServeHTTP(w, r)
		return w
	}
	// Cache the old signing verifier while leaving the session pending.
	if w := register(created.Pairing.PairingToken, "invalid"); w.Code != 400 {
		t.Fatalf("registration: %d", w.Code)
	}
	s.mu.Lock()
	s.config.SessionTTLSeconds = 7200
	s.opts.Catalog = nil
	s.mu.Unlock()
	bundle := decode[protocol.PairingBundle](t, call(s.Handler(), "POST", path, struct{}{}, cookie, csrf), 200)
	expected := created.Pairing
	expected.PairingToken = bundle.PairingToken
	if bundle.PairingToken == created.Pairing.PairingToken || !reflect.DeepEqual(expected, bundle) {
		t.Fatal("rotation changed frozen authorization or reused token")
	}
	s.mu.Lock()
	var rec sessionRecord
	err := s.get("sessions", created.Session.ID, &rec)
	s.mu.Unlock()
	if err != nil || rec.PairingDigest != security.TokenDigest(bundle.PairingToken) {
		t.Fatal("new digest was not stored")
	}
	raw, _ := json.Marshal(rec)
	if strings.Contains(string(raw), bundle.PairingToken) || strings.Contains(string(raw), created.Pairing.PairingToken) {
		t.Fatal("plaintext pairing persisted")
	}
	if w := register(created.Pairing.PairingToken, protocol.Version); w.Code != 401 {
		t.Fatalf("old token accepted: %d", w.Code)
	}
	decode[protocol.RegisterResponse](t, register(bundle.PairingToken, protocol.Version), 200)
	if w := call(s.Handler(), "POST", path, struct{}{}, cookie, csrf); w.Code != 409 {
		t.Fatalf("paired session rotated: %d", w.Code)
	}
}

func TestRegeneratePairingGuards(t *testing.T) {
	s := newTestServer(t, ":memory:")
	login := call(s.Handler(), "POST", "/api/v1/auth/login", protocol.LoginRequest{Token: admin}, nil, "")
	csrf := decode[protocol.LoginResponse](t, login, 200).CSRFToken
	cookie := login.Result().Cookies()[0]
	c := decode[protocol.CreateSessionResponse](t, call(s.Handler(), "POST", "/api/v1/sessions", protocol.CreateSessionRequest{Scope: protocol.Scope{AllowedTasks: []protocol.TaskType{protocol.InspectSystem}}}, cookie, csrf), 201)
	path := "/api/v1/sessions/" + c.Session.ID + "/pairing"
	for _, tc := range []struct {
		name   string
		cookie *http.Cookie
		csrf   string
		body   any
		status int
	}{
		{"bearer", nil, "", struct{}{}, 403},
		{"csrf", cookie, "", struct{}{}, 403},
		{"unknown field", cookie, csrf, map[string]string{"token": "override"}, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := call(s.Handler(), "POST", path, tc.body, tc.cookie, tc.csrf); w.Code != tc.status {
				t.Fatalf("want %d got %d", tc.status, w.Code)
			}
		})
	}
	for _, state := range []protocol.SessionState{protocol.Connecting, protocol.Online, protocol.Closed, protocol.Expired} {
		s.mu.Lock()
		var rec sessionRecord
		s.get("sessions", c.Session.ID, &rec)
		rec.Session.State = state
		s.put("sessions", c.Session.ID, rec)
		s.mu.Unlock()
		if w := call(s.Handler(), "POST", path, struct{}{}, cookie, csrf); w.Code != 409 {
			t.Fatalf("state %s accepted: %d", state, w.Code)
		}
	}
	s.mu.Lock()
	var rec sessionRecord
	s.get("sessions", c.Session.ID, &rec)
	rec.Session.State = protocol.PendingPair
	rec.Session.PairingExpiresAt = time.Now().Add(-time.Second)
	s.put("sessions", c.Session.ID, rec)
	s.mu.Unlock()
	if w := call(s.Handler(), "POST", path, struct{}{}, cookie, csrf); w.Code != 409 {
		t.Fatalf("expired deadline accepted: %d", w.Code)
	}
}
