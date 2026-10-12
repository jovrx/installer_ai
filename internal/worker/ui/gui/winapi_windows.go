//go:build windows

package gui

import (
	"fmt"
	"golang.org/x/sys/windows"
	"remoteops/internal/i18n"
	"sync"
	"unicode/utf16"
	"unsafe"
)

var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	gdi32            = windows.NewLazySystemDLL("gdi32.dll")
	comdlg32         = windows.NewLazySystemDLL("comdlg32.dll")
	createWindow     = user32.NewProc("CreateWindowExW")
	defWindowProc    = user32.NewProc("DefWindowProcW")
	sendMessage      = user32.NewProc("SendMessageW")
	destroyWindow    = user32.NewProc("DestroyWindow")
	showWindow       = user32.NewProc("ShowWindow")
	enableWindow     = user32.NewProc("EnableWindow")
	moveWindow       = user32.NewProc("MoveWindow")
	getClientRect    = user32.NewProc("GetClientRect")
	invalidateRect   = user32.NewProc("InvalidateRect")
	setWindowText    = user32.NewProc("SetWindowTextW")
	getWindowText    = user32.NewProc("GetWindowTextW")
	getTextLength    = user32.NewProc("GetWindowTextLengthW")
	setTimer         = user32.NewProc("SetTimer")
	killTimer        = user32.NewProc("KillTimer")
	getMessage       = user32.NewProc("GetMessageW")
	translateMessage = user32.NewProc("TranslateMessage")
	dispatchMessage  = user32.NewProc("DispatchMessageW")
	isDialogMessage  = user32.NewProc("IsDialogMessageW")
	setFocus         = user32.NewProc("SetFocus")
	deleteObject     = gdi32.NewProc("DeleteObject")
	selectObject     = gdi32.NewProc("SelectObject")
	createBrush      = gdi32.NewProc("CreateSolidBrush")
	windowRegistry   sync.Map
	classOnce        sync.Once
	classErr         error
)

const (
	wmClose      = 0x10
	wmDestroy    = 2
	wmSize       = 5
	wmPaint      = 15
	wmCommand    = 0x111
	wmTimer      = 0x113
	wmGetMinMax  = 0x24
	wmDPIChanged = 0x2e0
	wsChild      = 0x40000000
	wsVisible    = 0x10000000
	wsTabStop    = 0x10000
	wsBorder     = 0x800000
	wsVScroll    = 0x200000
)

type point struct{ X, Y int32 }
type winRect struct{ Left, Top, Right, Bottom int32 }
type message struct {
	Hwnd           uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	Pt             point
	Private        uint32
}
type windowClass struct {
	Size, Style                        uint32
	Proc                               uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	MenuName, ClassName                *uint16
	SmallIcon                          uintptr
}
type minMaxInfo struct{ Reserved, MaxSize, MaxPosition, MinTrack, MaxTrack point }
type paintStruct struct {
	DC                 uintptr
	Erase              int32
	Paint              winRect
	Restore, IncUpdate int32
	Reserved           [32]byte
}
type openFileName struct {
	Size                         uint32
	Owner, Instance              uintptr
	Filter, CustomFilter         *uint16
	MaxCustomFilter, FilterIndex uint32
	File                         *uint16
	MaxFile                      uint32
	FileTitle                    *uint16
	MaxFileTitle                 uint32
	InitialDir, Title            *uint16
	Flags                        uint32
	FileOffset, FileExtension    uint16
	DefaultExt                   *uint16
	CustomData, Hook             uintptr
	TemplateName                 *uint16
	Reserved                     uintptr
	Reserved2, FlagsEx           uint32
}

func u16(s string) *uint16  { p, _ := windows.UTF16PtrFromString(s); return p }
func ptr(p *uint16) uintptr { return uintptr(unsafe.Pointer(p)) }
func setText(h uintptr, s string) {
	if textOf(h) != s {
		setWindowText.Call(h, ptr(u16(s)))
	}
}
func textOf(h uintptr) string {
	length, _, _ := getTextLength.Call(h)
	b := make([]uint16, int(length)+1)
	n, _, _ := getWindowText.Call(h, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
	return windows.UTF16ToString(b[:n])
}

type scrollInfo struct {
	Size, Mask    uint32
	Min, Max      int32
	Page          uint32
	Pos, TrackPos int32
}

// Same-task refresh keeps selection and the first visible line. Output follows
// the tail only when already at the bottom with no selected text. Switching
// tasks starts at the tail; a user reading older lines is never moved there.
func setLogText(h uintptr, text string, sameTask bool) {
	if textOf(h) == text {
		return
	}
	var start, end uint32
	sendMessage.Call(h, 0xB0, uintptr(unsafe.Pointer(&start)), uintptr(unsafe.Pointer(&end))) // EM_GETSEL
	first, _, _ := sendMessage.Call(h, 0xCE, 0, 0)                                            // EM_GETFIRSTVISIBLELINE
	si := scrollInfo{Mask: 7}                                                                 // SIF_RANGE | SIF_PAGE | SIF_POS
	si.Size = uint32(unsafe.Sizeof(si))
	user32.NewProc("GetScrollInfo").Call(h, 1, uintptr(unsafe.Pointer(&si)))
	follow := !sameTask || (start == end && si.Pos >= si.Max-int32(max(si.Page, 1))+1)
	setWindowText.Call(h, ptr(u16(text)))
	if follow {
		length, _, _ := getTextLength.Call(h)
		sendMessage.Call(h, 0xB1, length, length)
		sendMessage.Call(h, 0xB7, 0, 0)                // EM_SCROLLCARET
		lines, _, _ := sendMessage.Call(h, 0xBA, 0, 0) // EM_GETLINECOUNT
		sendMessage.Call(h, 0xB6, 0, lines)
	} else {
		sendMessage.Call(h, 0xB1, uintptr(start), uintptr(end))
		current, _, _ := sendMessage.Call(h, 0xCE, 0, 0)
		sendMessage.Call(h, 0xB6, 0, uintptr(int64(first)-int64(current))) // EM_LINESCROLL
	}
}
func enabled(h uintptr, b bool) {
	v := uintptr(0)
	if b {
		v = 1
	}
	enableWindow.Call(h, v)
}
func visible(h uintptr, b bool) {
	v := uintptr(0)
	if b {
		v = 5
	}
	showWindow.Call(h, v)
}
func checked(h uintptr) bool { v, _, _ := sendMessage.Call(h, 0xf0, 0, 0); return v == 1 }
func choosePairing(owner uintptr, languages ...i18n.Language) (string, error) {
	language := i18n.Chinese
	if len(languages) > 0 {
		language = languages[0]
	}
	b := make([]uint16, 32768)
	filter := utf16.Encode([]rune(i18n.Text(language, "worker.gui.dialog_filter") + "\x00*.json\x00" + i18n.Text(language, "worker.gui.dialog_all") + "\x00*.*\x00\x00"))
	o := openFileName{Owner: owner, Filter: &filter[0], File: &b[0], MaxFile: uint32(len(b)), Title: u16(i18n.Text(language, "worker.gui.dialog_title")), Flags: 0x80000 | 0x1000 | 0x800 | 0x8}
	o.Size = uint32(unsafe.Sizeof(o))
	r, _, _ := comdlg32.NewProc("GetOpenFileNameW").Call(uintptr(unsafe.Pointer(&o)))
	if r == 0 {
		code, _, _ := comdlg32.NewProc("CommDlgExtendedError").Call()
		if code != 0 {
			return "", fmt.Errorf("%s", i18n.Text(language, "worker.gui.dialog_error"))
		}
		return "", nil
	}
	return windows.UTF16ToString(b), nil
}
func registerClass(language i18n.Language) error {
	classOnce.Do(func() {
		cursor, _, _ := user32.NewProc("LoadCursorW").Call(0, 32512)
		c := windowClass{Proc: windows.NewCallback(windowProc), Cursor: cursor, ClassName: u16("RemoteOpsWorkerWindow")}
		c.Size = uint32(unsafe.Sizeof(c))
		result, _, err := user32.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&c)))
		if result == 0 {
			classErr = fmt.Errorf("%s: %w", i18n.Text(language, "worker.gui.register_error"), err)
		}
	})
	return classErr
}
func windowProc(hwnd uintptr, msg uint32, wp uintptr, lp unsafe.Pointer) uintptr {
	if value, ok := windowRegistry.Load(hwnd); ok {
		w := value.(*window)
		switch msg {
		case wmClose:
			w.beginClose()
			return 0
		case wmDestroy:
			w.closed = true
			return 0
		case wmCommand:
			w.command(int(wp & 0xffff))
			return 0
		case wmTimer:
			w.tick()
			return 0
		case wmSize:
			w.layout()
			return 0
		case 0x318:
			w.paintDC(wp)
			return 0
		case wmPaint:
			w.paint()
			return 0
		case wmGetMinMax:
			m := (*minMaxInfo)(lp)
			m.MinTrack = point{int32(scale(720, w.dpi)), int32(scale(560, w.dpi))}
			return 0
		case wmDPIChanged:
			w.dpi = int(wp & 0xffff)
			w.setFonts()
			r := (*winRect)(lp)
			user32.NewProc("SetWindowPos").Call(hwnd, 0, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), 0x14)
			w.layout()
			return 0
		case 0x138, 0x133: // static/read-only and editable controls
			gdi32.NewProc("SetTextColor").Call(wp, 0x352619)
			gdi32.NewProc("SetBkColor").Call(wp, 0xffffff)
			return w.whiteBrush
		}
	}
	r, _, _ := defWindowProc.Call(hwnd, uintptr(msg), wp, uintptr(lp))
	return r
}
