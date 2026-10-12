//go:build windows

package privilege

import (
	"os/exec"
	"syscall"
)

func execHidden(exe string, args ...string) *exec.Cmd {
	c := exec.Command(exe, args...)
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return c
}
