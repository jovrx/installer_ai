//go:build windows

package installers

import (
	"context"
	"golang.org/x/sys/windows"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCancellationKillsWindowsProcessTree(t *testing.T) {
	exe, err := SystemExecutable("powershell.exe")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := make(chan uint32, 1)
	done := make(chan error, 1)
	// This fixed local test creates a harmless sleeping child to prove tree termination.
	const script = `$p=Start-Process -FilePath "$PSHOME\powershell.exe" -ArgumentList '-NoProfile -NonInteractive -Command Start-Sleep 120' -WindowStyle Hidden -PassThru;[Console]::WriteLine($p.Id);Start-Sleep 120`
	go func() {
		_, err := Run(ctx, exe, []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script}, t.TempDir(), func(stream, msg string) {
			if stream == "stdout" {
				id, e := strconv.ParseUint(strings.TrimSpace(msg), 10, 32)
				if e == nil {
					select {
					case child <- uint32(id):
					default:
					}
				}
			}
		})
		done <- err
	}()
	var id uint32
	select {
	case id = <-child:
	case <-ctx.Done():
		t.Fatal("child never started")
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, id)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancel returned success")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("process tree cancellation hung")
	}
	var code uint32
	if err = windows.GetExitCodeProcess(h, &code); err != nil {
		t.Fatal(err)
	}
	if code == 259 {
		t.Fatal("child process survived cancellation")
	}
}
