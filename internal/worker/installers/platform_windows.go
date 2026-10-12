//go:build windows

package installers

import (
	"context"
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func FreeSpace(path string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free, total, available uint64
	err = windows.GetDiskFreeSpaceEx(p, &free, &total, &available)
	return free, err
}
func SystemExecutable(name string) (string, error) {
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		return "", err
	}
	switch name {
	case "msiexec.exe", "taskkill.exe", "icacls.exe":
		return filepath.Join(dir, name), nil
	case "powershell.exe":
		return filepath.Join(dir, "WindowsPowerShell", "v1.0", name), nil
	}
	return "", errors.New("unsupported system executable")
}

// Run terminates the full installer process tree on cancellation, then reaps the parent.
func Run(ctx context.Context, path string, args []string, dir string, emit func(string, string)) (int, error) {
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Env = []string{"SystemRoot=" + os.Getenv("SystemRoot"), "WINDIR=" + os.Getenv("SystemRoot"), "TEMP=" + dir, "TMP=" + dir, "ProgramFiles=" + os.Getenv("ProgramFiles"), "ProgramData=" + os.Getenv("ProgramData"), "LOCALAPPDATA=" + os.Getenv("LOCALAPPDATA")}
	cmd.Stdout = &boundedWriter{emit: emit, stream: "stdout"}
	cmd.Stderr = &boundedWriter{emit: emit, stream: "stderr"}
	cmd.WaitDelay = 5 * time.Second
	cmd.Cancel = func() error {
		kill, err := SystemExecutable("taskkill.exe")
		if err == nil {
			kc, kcancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer kcancel()
			k := exec.CommandContext(kc, kill, "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
			k.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			err = k.Run()
			if err == nil {
				return nil
			}
		}
		return cmd.Process.Kill()
	}
	err := cmd.Run()
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	if ctx.Err() != nil {
		return code, ctx.Err()
	}
	if code == 0 || code == 3010 {
		return code, nil
	}
	return code, err
}

type boundedWriter struct {
	mu      sync.Mutex
	emit    func(string, string)
	stream  string
	written int
}

func (w *boundedWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(b)
	remaining := (1 << 20) - w.written
	if remaining > 0 {
		if len(b) > remaining {
			b = b[:remaining]
		}
		w.written += len(b)
		w.emit(w.stream, strings.ToValidUTF8(string(b), "?"))
	}
	return n, nil
}
