package security

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRequestIdentityIntegrityAndReplay(t *testing.T) {
	body := []byte(`{"task":"install"}`)
	req, _ := http.NewRequest("POST", "http://worker/v1/tasks?limit=10", bytes.NewReader(body))
	if err := SignRequest(req, body, "key", "controller-to-worker"); err != nil {
		t.Fatal(err)
	}
	verifier := NewVerifier("key", "controller-to-worker")
	if err := verifier.Verify(req, []byte("tampered")); err == nil {
		t.Fatal("tampered body accepted")
	}
	if err := verifier.Verify(req, body); err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify(req, body); err == nil {
		t.Fatal("nonce replay accepted")
	}
	wrong := NewVerifier("other", "controller-to-worker")
	if err := wrong.Verify(req, body); err == nil {
		t.Fatal("wrong controller accepted")
	}
}

func TestResponseIntegrity(t *testing.T) {
	body := []byte(`{"state":"online"}`)
	resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}
	SignResponse(resp.Header, 200, body, "key", "controller-to-worker", "nonce")
	if err := VerifyResponse(resp, body, "key", "controller-to-worker", "nonce"); err != nil {
		t.Fatal(err)
	}
	if err := VerifyResponse(resp, []byte("other"), "key", "controller-to-worker", "nonce"); err == nil {
		t.Fatal("tampered response accepted")
	}
	if err := VerifyResponse(resp, body, "key", "controller-to-worker", "other"); err == nil {
		t.Fatal("response replay accepted")
	}
}

func TestCredentialRedaction(t *testing.T) {
	for _, s := range []string{"Authorization: Bearer secret", "password=secret", "Cookie: session=secret", "tskey-auth-secret", "token: secret", "-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----"} {
		if strings.Contains(Redact(s), "secret") {
			t.Fatalf("credential leaked: %q", s)
		}
	}
}

func TestStructuredAndStandaloneCredentialRedaction(t *testing.T) {
	for _, s := range []string{`{"token":"secret"}`, `{"password":"secret with spaces"}`, `{"api_key":"secret"}`, `{"Cookie":"session=secret"}`, "Bearer secret"} {
		if strings.Contains(Redact(s), "secret") {
			t.Fatalf("structured credential leaked: %q", s)
		}
	}
}
