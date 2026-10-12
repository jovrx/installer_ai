package worker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"remoteops/internal/security"
	"remoteops/pkg/protocol"
	"testing"
	"time"
)

func TestRegistrationAuthenticatesAndBindsConsent(t *testing.T) {
	pairing := protocol.PairingBundle{ProtocolVersion: protocol.Version, ControllerName: "Operator", SessionID: "sess_test", PairingToken: "pair-secret", ExpiresAt: time.Now().Add(time.Minute), Scope: protocol.Scope{AllowedTasks: []protocol.TaskType{protocol.InspectSystem}}, Catalog: []protocol.CatalogEntry{}}
	pairing.CatalogDigest = protocol.Digest(pairing.Catalog)
	pairing.AuthorizationSeconds = 3600
	mode := "valid"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := security.NewVerifier(pairing.PairingToken, "worker-to-controller").Verify(r, body); err != nil {
			t.Error(err)
		}
		response := protocol.RegisterResponse{Session: protocol.Session{ID: pairing.SessionID, DeviceID: "dev_test", Scope: pairing.Scope, State: protocol.Online, ExpiresAt: time.Now().Add(time.Hour)}, Device: protocol.Device{ID: "dev_test", SessionID: pairing.SessionID}, SessionToken: "session-secret", Catalog: pairing.Catalog}
		if mode == "scope" {
			response.Session.Scope.AllowAdmin = true
		}
		if mode == "duration" {
			response.Session.ExpiresAt = time.Now().Add(2 * time.Hour)
		}
		b, _ := json.Marshal(response)
		if mode != "unsigned" {
			security.SignResponse(w.Header(), 200, b, pairing.PairingToken, "worker-to-controller", r.Header.Get(security.HeaderNonce))
		}
		w.Write(b)
	}))
	defer srv.Close()
	pairing.ControllerURL = srv.URL
	for _, m := range []string{"valid", "unsigned", "scope", "duration"} {
		mode = m
		_, err := RegisterPairing(context.Background(), srv.Client(), pairing)
		if (err == nil) != (mode == "valid") {
			t.Fatalf("registration mode %s err=%v", mode, err)
		}
	}
}
func TestExpiredSessionAndLostController(t *testing.T) {
	s := testServer(t, executorFunc(func(context.Context, protocol.TaskSpec, protocol.CatalogEntry, func(string, string)) (protocol.TaskResult, error) {
		return protocol.TaskResult{}, nil
	}))
	s.mu.Lock()
	s.opt.Session.ExpiresAt = time.Now().Add(-time.Second)
	s.mu.Unlock()
	if w := rpc(t, s, "GET", "/health", nil, true); w.Code != 409 {
		t.Fatal("expired session accepted")
	}
	s = testServer(t, executorFunc(func(context.Context, protocol.TaskSpec, protocol.CatalogEntry, func(string, string)) (protocol.TaskResult, error) {
		return protocol.TaskResult{}, nil
	}))
	s.mu.Lock()
	s.lastController = time.Now().Add(-46 * time.Second)
	s.mu.Unlock()
	if w := rpc(t, s, "GET", "/health", nil, true); w.Code != 409 {
		t.Fatal("lost controller still authorized")
	}
}
