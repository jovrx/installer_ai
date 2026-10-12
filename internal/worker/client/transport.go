package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"

	"remoteops/internal/worker"
	"remoteops/internal/worker/privilege"
	"remoteops/pkg/protocol"
	"tailscale.com/tsnet"
)

// networkNode isolates tsnet I/O, allowing focused tests to exercise the real
// registration and Worker lifecycle without joining any tailnet.
type networkNode interface {
	Up(context.Context) error
	ClearKey()
	HTTPClient() *http.Client
	Listen(string, string) (net.Listener, error)
	Logout(context.Context) error
	Close() error
}
type nodeFactory func(dir string, key []byte) (networkNode, error)
type tailscaleNode struct{ server *tsnet.Server }

func (n *tailscaleNode) Up(ctx context.Context) error { _, err := n.server.Up(ctx); return err }
func (n *tailscaleNode) ClearKey()                    { n.server.AuthKey = "" }
func (n *tailscaleNode) HTTPClient() *http.Client     { return n.server.HTTPClient() }
func (n *tailscaleNode) Listen(network, address string) (net.Listener, error) {
	return n.server.Listen(network, address)
}
func (n *tailscaleNode) Logout(ctx context.Context) error {
	lc, err := n.server.LocalClient()
	if err != nil {
		return err
	}
	return lc.Logout(ctx)
}
func (n *tailscaleNode) Close() error { return n.server.Close() }

func defaultConnector(ctx context.Context, b protocol.PairingBundle, key []byte) (Connection, error) {
	defer clear(key)
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		return nil, errPlatform
	}
	if privilege.Elevated() {
		return nil, errElevated
	}
	return connectTransport(ctx, b, key, func(dir string, key []byte) (networkNode, error) {
		return &tailscaleNode{server: &tsnet.Server{Dir: dir, Hostname: "remoteops-worker", Ephemeral: true, AuthKey: string(key), Logf: func(string, ...any) {}, UserLogf: func(string, ...any) {}}}, nil
	})
}

type transportConnection struct {
	node          networkNode
	dir           string
	srv           *worker.Server
	session       protocol.Session
	client        *http.Client
	controllerURL string
	httpServer    *http.Server
	cancel        context.CancelFunc
	finished      chan struct{}
	wg            sync.WaitGroup
}

func connectTransport(ctx context.Context, b protocol.PairingBundle, key []byte, newNode nodeFactory) (_ Connection, err error) {
	defer clear(key)
	if worker.ValidatePairing(b) != nil {
		return nil, errPairingInvalid
	}
	if !validKey(key) {
		return nil, errKey
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	dir, err := os.MkdirTemp("", "remoteops-tsnet-")
	if err != nil {
		return nil, err
	}
	connectionCtx, cancel := context.WithCancel(ctx)
	c := &transportConnection{dir: dir, controllerURL: b.ControllerURL, cancel: cancel, finished: make(chan struct{})}
	// On success the lifetime goroutine owns cleanup. Every earlier exit cleans up
	// synchronously, including registration success followed by listener failure.
	defer func() {
		if err != nil {
			c.cleanup()
		}
	}()
	if err = privilege.SecureDirectory(dir); err != nil {
		return nil, err
	}
	if c.node, err = newNode(dir, key); err != nil {
		return nil, err
	}
	upCtx, upCancel := context.WithTimeout(connectionCtx, 90*time.Second)
	err = c.node.Up(upCtx)
	upCancel()
	c.node.ClearKey()
	clear(key)
	if err != nil {
		return nil, errors.New("Tailscale connection failed")
	}
	if connectionCtx.Err() != nil {
		return nil, connectionCtx.Err()
	}
	// Do not mutate a caller-owned HTTP client in tests or the node's global client.
	httpClient := *c.node.HTTPClient()
	httpClient.Timeout = 10 * time.Second
	c.client = &httpClient
	regCtx, regCancel := context.WithTimeout(connectionCtx, 10*time.Second)
	reg, err := worker.RegisterPairing(regCtx, c.client, b)
	regCancel()
	b.PairingToken = ""
	if err != nil {
		return nil, err
	}
	c.session = reg.Session
	c.srv, err = worker.New(worker.Options{Session: reg.Session, DeviceID: reg.Device.ID, SessionToken: reg.SessionToken, Catalog: reg.Catalog})
	reg.SessionToken = ""
	if err != nil {
		return nil, err
	}
	listener, err := c.node.Listen("tcp", fmt.Sprintf(":%d", protocol.WorkerPort))
	if err != nil {
		return nil, err
	}
	c.httpServer = &http.Server{Handler: c.srv.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second}
	c.wg.Add(2)
	go func() {
		defer c.wg.Done()
		if serveErr := c.httpServer.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			c.srv.CloseWithReason("heartbeat_lost")
		}
	}()
	go func() { defer c.wg.Done(); c.srv.Heartbeats(connectionCtx, c.client, c.controllerURL) }()
	go func() {
		select {
		case <-connectionCtx.Done():
			c.srv.CloseWithReason("local_closed")
		case <-c.srv.Done():
		}
		c.cleanup()
	}()
	return c, nil
}

func (c *transportConnection) cleanup() {
	c.cancel()
	if c.srv != nil {
		c.srv.Close()
		c.srv.Wait()
	}
	if c.httpServer != nil {
		_ = c.httpServer.Close()
	}
	c.wg.Wait()
	if c.srv != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = c.srv.NotifyClose(closeCtx, c.client, c.controllerURL)
		cancel()
	}
	if c.node != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = c.node.Logout(closeCtx)
		cancel()
		_ = c.node.Close()
		c.node.ClearKey()
	}
	_ = os.RemoveAll(c.dir)
	close(c.finished)
}

func (c *transportConnection) Snapshot() []worker.LocalTaskView { return c.srv.LocalStatus() }
func (c *transportConnection) Session() protocol.Session {
	s := c.session
	s.Scope.AllowedTasks = append([]protocol.TaskType(nil), s.Scope.AllowedTasks...)
	s.Scope.AllowedPackages = append([]string(nil), s.Scope.AllowedPackages...)
	if s.ClosedAt != nil {
		closed := *s.ClosedAt
		s.ClosedAt = &closed
	}
	return s
}
func (c *transportConnection) CancelTask(id string) error { return c.srv.CancelLocalTask(id) }
func (c *transportConnection) Disconnect(reason string)   { c.srv.CloseWithReason(reason); c.cancel() }
func (c *transportConnection) Done() <-chan struct{}      { return c.srv.Done() }
func (c *transportConnection) Wait()                      { <-c.finished }
func (c *transportConnection) EndReason() string          { return c.srv.EndReason() }
