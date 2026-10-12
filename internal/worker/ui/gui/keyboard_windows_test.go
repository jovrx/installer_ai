//go:build windows

package gui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"remoteops/internal/worker/client"
	"remoteops/pkg/protocol"
	"testing"
	"time"
	"unsafe"
)

type guiThreadInfo struct {
	Size, Flags                                        uint32
	Active, Focus, Capture, MenuOwner, MoveSize, Caret uintptr
	CaretRect                                          winRect
}

func focusedControl(hwnd uintptr) uintptr {
	thread, _, _ := user32.NewProc("GetWindowThreadProcessId").Call(hwnd, 0)
	info := guiThreadInfo{}
	info.Size = uint32(unsafe.Sizeof(info))
	user32.NewProc("GetGUIThreadInfo").Call(thread, uintptr(unsafe.Pointer(&info)))
	return info.Focus
}
func awaitNative(t *testing.T, description string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(description)
}
func TestRunFocusSurvivesConnectionViewTransition(t *testing.T) {
	b := protocol.PairingBundle{ProtocolVersion: protocol.Version, ControllerName: "键盘回归测试", ControllerURL: "http://controller:9080", SessionID: "sess_keyboard", PairingToken: "test-only-keyboard-token", ExpiresAt: time.Now().Add(time.Hour), AuthorizationSeconds: 3600, Scope: protocol.Scope{AllowedTasks: []protocol.TaskType{protocol.InspectSystem}}}
	b.CatalogDigest = protocol.Digest(b.Catalog)
	data, _ := json.Marshal(b)
	path := filepath.Join(t.TempDir(), "pairing.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	conn := &slowConnection{done: make(chan struct{}), release: make(chan struct{})}
	close(conn.release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app := client.New(ctx, client.Options{PairingPath: path, Connect: func(context.Context, protocol.PairingBundle, []byte) (client.Connection, error) { return conn, nil }})
	done := make(chan error, 1)
	go func() { done <- Run(ctx, app) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Run did not clean up")
		}
	}()
	var hwnd uintptr
	awaitNative(t, "Run window missing", func() bool {
		hwnd, _, _ = user32.NewProc("FindWindowW").Call(ptr(u16("RemoteOpsWorkerWindow")), 0)
		return hwnd != 0
	})
	child := func(id int) uintptr { h, _, _ := user32.NewProc("GetDlgItem").Call(hwnd, uintptr(id)); return h }
	enabledControl := func(id int) bool { v, _, _ := user32.NewProc("IsWindowEnabled").Call(child(id)); return v != 0 }
	visibleControl := func(id int) bool { v, _, _ := user32.NewProc("IsWindowVisible").Call(child(id)); return v != 0 }

	awaitNative(t, "consent never enabled", func() bool { return enabledControl(idConsent) })
	awaitNative(t, "initial pairing focus missing", func() bool { return focusedControl(hwnd) == child(idPath) })
	user32.NewProc("SetForegroundWindow").Call(hwnd)
	if err := app.ConsentAndConnect([]byte("tskey-auth-keyboard")); err != nil {
		t.Fatal(err)
	}
	awaitNative(t, "connection did not start", func() bool { return visibleControl(idTasks) && app.Snapshot().State == "online" })
	awaitNative(t, "view transition left focus on top-level/hidden/disabled window", func() bool {
		h := focusedControl(hwnd)
		if h == 0 || h == hwnd {
			return false
		}
		v, _, _ := user32.NewProc("IsWindowVisible").Call(h)
		e, _, _ := user32.NewProc("IsWindowEnabled").Call(h)
		return v != 0 && e != 0
	})
}
