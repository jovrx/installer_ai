package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"remoteops/internal/security"
	"remoteops/pkg/protocol"
)

// Replace only tsnet's network boundary; registration, response authentication,
// Worker lifetime, cancellation, close notification and directory ownership run real code.
type testNode struct {
	client     *http.Client
	upErr      error
	listenErr  error
	keyCleared atomic.Bool
	loggedOut  atomic.Bool
	closed     atomic.Bool
}

func (n *testNode) Up(context.Context) error { return n.upErr }
func (n *testNode) ClearKey()                { n.keyCleared.Store(true) }
func (n *testNode) HTTPClient() *http.Client { return n.client }
func (n *testNode) Listen(string, string) (net.Listener, error) {
	if n.listenErr != nil {
		return nil, n.listenErr
	}
	return net.Listen("tcp", "127.0.0.1:0")
}
func (n *testNode) Logout(ctx context.Context) error {
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("missing logout deadline")
	}
	n.loggedOut.Store(true)
	return nil
}
func (n *testNode) Close() error { n.closed.Store(true); return nil }

func TestTransportFailureRemovesOwnedDirectoryAndLogsOut(t *testing.T) {
	n := &testNode{upErr: errors.New("private tskey-auth-input")}
	var dir string
	_, err := connectTransport(context.Background(), pairingFixture(t), []byte("tskey-auth-input"), func(d string, _ []byte) (networkNode, error) { dir = d; return n, nil })
	if err == nil || dir == "" || !n.closed.Load() || !n.loggedOut.Load() || !n.keyCleared.Load() {
		t.Fatal("failed transport not cleaned")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("temporary credentials directory survives failure")
	}
	if got := ErrorText(err); got == "" {
		t.Fatal("failure not actionable")
	}
}

func TestTransportUsesSignedFrozenRegistrationAndCleanup(t *testing.T) {
	for _, mode := range []string{"success", "listen_failure", "scope_mismatch", "unsigned"} {
		t.Run(mode, func(t *testing.T) {
			b := pairingFixture(t)
			var closed atomic.Bool
			controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				secret := b.PairingToken
				if r.URL.Path == "/api/v1/worker/close" {
					secret = "session-secret"
					closed.Store(true)
					if _, ok := r.Context().Deadline(); ok {
						t.Error("server cannot observe client context deadline")
					}
				}
				if err := security.NewVerifier(secret, "worker-to-controller").Verify(r, body); err != nil {
					t.Error(err)
				}
				var value any = struct{}{}
				if r.URL.Path == "/api/v1/worker/register" {
					reg := protocol.RegisterResponse{Session: protocol.Session{ID: b.SessionID, DeviceID: "dev_test", Scope: b.Scope, State: protocol.Online, ExpiresAt: time.Now().Add(30 * time.Minute)}, Device: protocol.Device{ID: "dev_test", SessionID: b.SessionID}, SessionToken: "session-secret", Catalog: b.Catalog}
					if mode == "scope_mismatch" {
						reg.Session.Scope.AllowAdmin = false
					}
					value = reg
				}
				data, _ := json.Marshal(value)
				if mode != "unsigned" {
					security.SignResponse(w.Header(), 200, data, secret, "worker-to-controller", r.Header.Get(security.HeaderNonce))
				}
				w.Write(data)
			}))
			defer controller.Close()
			b.ControllerURL = controller.URL
			n := &testNode{client: controller.Client()}
			if mode == "listen_failure" {
				n.listenErr = errors.New("cannot listen")
			}
			var dir string
			conn, err := connectTransport(context.Background(), b, []byte("tskey-auth-input"), func(d string, _ []byte) (networkNode, error) { dir = d; return n, nil })
			if mode == "success" {
				if err != nil {
					t.Fatal(err)
				}
				if conn.Session().Scope.AllowAdmin != true {
					t.Fatal("scope changed")
				}
				session := conn.Session()
				session.Scope.AllowedPackages[0] = "mutated"
				if conn.Session().Scope.AllowedPackages[0] != "git" {
					t.Fatal("session aliases caller")
				}
				conn.Disconnect("local_closed")
				conn.Wait()
				if conn.EndReason() != "local_closed" {
					t.Fatal(conn.EndReason())
				}
			} else if err == nil {
				conn.Disconnect("local_closed")
				conn.Wait()
				t.Fatal("invalid transport accepted")
			}
			if closed.Load() != (mode == "success" || mode == "listen_failure") {
				t.Fatal("close notification missing or sent for untrusted registration")
			}
			if !n.closed.Load() || !n.loggedOut.Load() || !n.keyCleared.Load() {
				t.Fatal("node cleanup incomplete")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatal("directory leaked")
			}
		})
	}
}
