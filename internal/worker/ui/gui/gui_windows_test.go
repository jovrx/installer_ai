//go:build windows

package gui

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"remoteops/internal/worker"
	"remoteops/internal/worker/client"
	"remoteops/internal/worker/events"
	"remoteops/pkg/protocol"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

func TestNativeWindowRejectsConsentAndInvalidFile(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	app := client.New(context.Background(), client.Options{})
	w, err := newWindow(app)
	if err != nil {
		t.Fatal(err)
	}
	defer w.dispose()
	w.view = client.Snapshot{State: "ready", PairingExpiresAt: time.Now().Add(time.Minute)}
	setText(w.controls[idKey], "tskey-auth-do-not-submit")
	w.command(idConnect)
	if textOf(w.controls[idKey]) != "" {
		t.Fatal("rejected connect retained password")
	}
	if app.Snapshot().State != "idle" {
		t.Fatal("unchecked consent reached application")
	}
	setText(w.controls[idPath], `Z:\missing-pairing-test.json`)
	w.command(idLoad)
	deadline := time.Now().Add(2 * time.Second)
	for w.busy && time.Now().Before(deadline) {
		w.tick()
		time.Sleep(10 * time.Millisecond)
	}
	if w.busy || !strings.Contains(w.view.Error, "无法读取") {
		t.Fatalf("invalid file state: %+v", w.view)
	}
	w.beginClose()
	deadline = time.Now().Add(time.Second)
	for !w.closed && time.Now().Before(deadline) {
		w.tick()
		time.Sleep(time.Millisecond)
	}
	if !w.closed || app.Snapshot().State != "disconnected" {
		t.Fatal("close did not disconnect and join")
	}
}
func TestRunContextCancellationClosesWindow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	app := client.New(ctx, client.Options{})
	done := make(chan error, 1)
	go func() { done <- Run(ctx, app) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("window ignored context cancellation")
	}
}

// These integration fixtures replace only the external transport. The App and native
// controls still enforce loading, consent, generation changes and lifecycle cleanup.
type slowConnection struct {
	done            chan struct{}
	release         chan struct{}
	snapshotRelease chan struct{}
	snapshotStarted chan struct{}
	cancelStarted   chan string
	once            sync.Once
	snapshotOnce    sync.Once
	block           atomic.Bool
}

func (c *slowConnection) Snapshot() []worker.LocalTaskView {
	if c.block.Load() {
		c.snapshotOnce.Do(func() { close(c.snapshotStarted) })
		<-c.snapshotRelease
	}
	return []worker.LocalTaskView{{Task: protocol.Task{ID: "one", State: "running"}}, {Task: protocol.Task{ID: "two", State: "queued"}}}
}
func (c *slowConnection) Session() protocol.Session {
	return protocol.Session{ExpiresAt: time.Now().Add(time.Hour)}
}
func (c *slowConnection) CancelTask(id string) error { c.cancelStarted <- id; <-c.release; return nil }
func (c *slowConnection) Disconnect(string)          { c.once.Do(func() { close(c.done) }) }
func (c *slowConnection) Done() <-chan struct{}      { return c.done }
func (c *slowConnection) Wait()                      { <-c.release }
func (c *slowConnection) EndReason() string          { return "local_closed" }
func TestSlowCallbacksDoNotBlockDisconnectOrCloseAndIgnoreStaleResult(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	c := &slowConnection{done: make(chan struct{}), release: make(chan struct{}), snapshotRelease: make(chan struct{}), snapshotStarted: make(chan struct{}), cancelStarted: make(chan string, 1)}
	app := client.New(context.Background(), client.Options{Connect: func(context.Context, protocol.PairingBundle, []byte) (client.Connection, error) { return c, nil }})
	b := protocol.PairingBundle{ProtocolVersion: protocol.Version, ControllerName: "测试控制端", ControllerURL: "http://controller:9080", SessionID: "sess_gui_test", PairingToken: "gui-test-opaque-token", ExpiresAt: time.Now().Add(time.Hour), AuthorizationSeconds: 3600, Scope: protocol.Scope{AllowedTasks: []protocol.TaskType{protocol.InspectSystem}}}
	b.CatalogDigest = protocol.Digest(b.Catalog)
	data, _ := json.Marshal(b)
	path := filepath.Join(t.TempDir(), "pairing.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := app.LoadPairing(path); err != nil {
		t.Fatal(err)
	}
	if err := app.ConsentAndConnect([]byte("tskey-auth-inert-test")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	var snapshot client.Snapshot
	for time.Now().Before(deadline) {
		snapshot = app.Snapshot()
		if snapshot.State == "online" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if snapshot.State != "online" {
		t.Fatal("test connection did not become online")
	}
	c.block.Store(true)
	w, err := newWindow(app)
	if err != nil {
		t.Fatal(err)
	}
	defer w.dispose()
	visible(w.hwnd, true)
	w.view = snapshot
	w.selected = "two"
	w.explicitSelection = true
	w.refresh()
	select {
	case <-c.snapshotStarted:
	case <-time.After(time.Second):
		t.Fatal("snapshot was not requested")
	}
	start := time.Now()
	w.command(idCancel)
	select {
	case id := <-c.cancelStarted:
		if id != "two" {
			t.Fatalf("cancel targeted %q", id)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel not dispatched")
	}
	w.command(idDisconnect)
	select {
	case <-c.done:
	case <-time.After(time.Second):
		t.Fatal("blocked observer/cancel delayed disconnect")
	}
	if time.Since(start) > time.Second {
		t.Fatal("UI dispatch blocked")
	}
	deadline = time.Now().Add(time.Second)
	for w.busy && time.Now().Before(deadline) {
		w.tick()
		time.Sleep(time.Millisecond)
	}
	if w.view.State != "disconnected" {
		t.Fatalf("disconnect view %s", w.view.State)
	}
	w.beginClose()
	w.closeAt = time.Now().Add(-9 * time.Second)
	w.tick()
	isVisible, _, _ := user32.NewProc("IsWindowVisible").Call(w.hwnd)
	if w.closed || isVisible == 0 || !strings.Contains(w.notice, "正在清理") {
		t.Fatal("blocked cleanup silently hid window")
	}
	close(c.snapshotRelease)
	close(c.release)
	deadline = time.Now().Add(time.Second)
	for !w.closed && time.Now().Before(deadline) {
		w.tick()
		time.Sleep(time.Millisecond)
	}
	if !w.closed {
		t.Fatal("safe cleanup did not close window")
	}
	if w.view.State != "disconnected" {
		t.Fatal("stale callback revived disconnected view")
	}
}
func TestNativeDPIChangesAndScreenshots(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	app := client.New(context.Background(), client.Options{})
	w, err := newWindow(app)
	if err != nil {
		t.Fatal(err)
	}
	defer w.dispose()
	started := time.Now().Add(-24 * time.Second)
	w.view = client.Snapshot{State: "online", Controller: "模拟场景（无真实连接）", Address: "http://test.invalid", ExpiresAt: time.Now().Add(42 * time.Minute), Scope: []string{"检查系统", "Git 2.51.0", "允许请求管理员权限；每次需要 Windows 确认"}, Tasks: []worker.LocalTaskView{{PackageName: "Git", Task: protocol.Task{ID: "test-task", State: "running", StartedAt: &started, Spec: protocol.TaskSpec{Type: protocol.InstallPackage, Package: &protocol.PackageRef{ID: "git", Version: "2.51.0"}}}, Progress: events.Progress{Stage: "downloading", CompletedBytes: 38 << 20, TotalBytes: 62 << 20}}}}
	w.refresh()
	visible(w.hwnd, true)
	for _, dpi := range []int{96, 144, 192} {
		r := winRect{20, 20, int32(20 + scale(720, dpi)), int32(20 + scale(560, dpi))}
		sendMessage.Call(w.hwnd, wmDPIChanged, uintptr(dpi|(dpi<<16)), uintptr(unsafe.Pointer(&r)))
		if w.dpi != dpi {
			t.Fatalf("WM_DPICHANGED ignored %d", dpi)
		}
		w.refresh()
		var clientBounds winRect
		getClientRect.Call(w.hwnd, uintptr(unsafe.Pointer(&clientBounds)))
		expected := makeLayout(int(clientBounds.Right), int(clientBounds.Bottom), dpi, true, false)
		for id, want := range expected {
			var actual winRect
			user32.NewProc("GetWindowRect").Call(w.controls[id], uintptr(unsafe.Pointer(&actual)))
			user32.NewProc("MapWindowPoints").Call(0, w.hwnd, uintptr(unsafe.Pointer(&actual)), 2)
			if int(actual.Left) != want.X || int(actual.Top) != want.Y || actual.Right > clientBounds.Right || actual.Bottom > clientBounds.Bottom {
				t.Fatalf("dpi %d control %d out of native bounds: %+v", dpi, id, actual)
			}
		}
		if dir := os.Getenv("GUI_SCREENSHOT_DIR"); dir != "" {
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			saveWindowPNG(t, w, filepath.Join(dir, fmt.Sprintf("gui-native-%d.png", dpi)))
		}
	}
}

type bitmapInfo struct {
	Size                         uint32
	Width, Height                int32
	Planes, BitCount             uint16
	Compression, SizeImage       uint32
	XPelsPerMeter, YPelsPerMeter int32
	ClrUsed, ClrImportant        uint32
}

func saveWindowPNG(t *testing.T, w *window, path string) {
	t.Helper()
	var bounds winRect
	getClientRect.Call(w.hwnd, uintptr(unsafe.Pointer(&bounds)))
	width, height := int(bounds.Right), int(bounds.Bottom)
	dc, _, _ := user32.NewProc("GetDC").Call(w.hwnd)
	defer user32.NewProc("ReleaseDC").Call(w.hwnd, dc)
	mem, _, _ := gdi32.NewProc("CreateCompatibleDC").Call(dc)
	defer gdi32.NewProc("DeleteDC").Call(mem)
	info := bitmapInfo{Size: 40, Width: int32(width), Height: -int32(height), Planes: 1, BitCount: 32}
	var bits unsafe.Pointer
	bitmap, _, _ := gdi32.NewProc("CreateDIBSection").Call(dc, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bitmap == 0 || bits == nil {
		t.Fatal("CreateDIBSection failed")
	}
	defer deleteObject.Call(bitmap)
	old, _, _ := selectObject.Call(mem, bitmap)
	defer selectObject.Call(mem, old)
	w.paintDC(mem)
	for id, r := range makeLayout(width, height, w.dpi, w.connected(), w.logs) {
		save, _, _ := gdi32.NewProc("SaveDC").Call(mem)
		gdi32.NewProc("SetViewportOrgEx").Call(mem, uintptr(r.X), uintptr(r.Y), 0)
		sendMessage.Call(w.controls[id], 0x317, mem, 0x1c)
		gdi32.NewProc("RestoreDC").Call(mem, save)
	}
	gdi32.NewProc("GdiFlush").Call()
	raw := unsafe.Slice((*byte)(bits), width*height*4)
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for i := 0; i < len(raw); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = raw[i+2], raw[i+1], raw[i], 255
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}
func TestNativeExpiryClearsPasswordAndDisablesConnect(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	w, err := newWindow(client.New(context.Background(), client.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	defer w.dispose()
	w.view = client.Snapshot{State: "ready", PairingExpiresAt: time.Now().Add(-time.Second)}
	sendMessage.Call(w.controls[idConsent], 0xf1, 1, 0)
	setText(w.controls[idKey], "tskey-auth-expired-input")
	w.refresh()
	if textOf(w.controls[idKey]) != "" {
		t.Fatal("expired pairing retained password")
	}
	canPress, _, _ := user32.NewProc("IsWindowEnabled").Call(w.controls[idConnect])
	if canPress != 0 {
		t.Fatal("expired connect button enabled")
	}
}
