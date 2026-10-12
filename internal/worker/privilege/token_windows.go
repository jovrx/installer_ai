//go:build windows

package privilege

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

func Elevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

type shellExecuteInfo struct {
	Size                              uint32
	Mask                              uint32
	Window                            windows.Handle
	Verb, File, Parameters, Directory *uint16
	Show                              int32
	Instance                          windows.Handle
	IDList                            unsafe.Pointer
	Class                             *uint16
	ClassKey                          windows.Handle
	HotKey                            uint32
	Icon                              windows.Handle
	Process                           windows.Handle
}

func launchHelper(path string) (windows.Handle, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	args, _ := windows.UTF16PtrFromString("--elevated-helper " + syscall.EscapeArg(path))
	dir, _ := windows.UTF16PtrFromString(filepath.Dir(exe))
	info := shellExecuteInfo{Mask: 0x40 | 0x100, Verb: verb, File: file, Parameters: args, Directory: dir, Show: 0}
	info.Size = uint32(unsafe.Sizeof(info))
	r, _, err := windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW").Call(uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		if errors.Is(err, windows.ERROR_CANCELLED) {
			return 0, errors.New("permission_denied: UAC was declined")
		}
		return 0, err
	}
	return info.Process, nil
}
func closeHelper(handle windows.Handle) { windows.CloseHandle(handle) }
func secureDirectory(path string) error {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	sid := user.User.Sid.String()
	sys, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	// Remove inherited access; only this user, local Administrators and SYSTEM may access IPC files.
	p := filepath.Join(sys, "icacls.exe")
	cmd := execHidden(p, path, "/inheritance:r", "/grant:r", "*"+sid+":(OI)(CI)F", "*S-1-5-18:(OI)(CI)F", "*S-1-5-32-544:(OI)(CI)F")
	return cmd.Run()
}
