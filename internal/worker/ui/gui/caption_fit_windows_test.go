//go:build windows

package gui

import (
	"remoteops/internal/i18n"
	"remoteops/internal/worker/client"
	"strings"
	"testing"
	"unsafe"
)

// Narrow controls must retain the complete caption, mnemonic and readable
// button padding with the actual native font, including the host's 125% DPI.
func TestNativeMinimumLanguageAndBrowseCaptionsFit(t *testing.T) {
	w := reviewWindow(t)
	dc, _, _ := user32.NewProc("GetDC").Call(w.hwnd)
	defer user32.NewProc("ReleaseDC").Call(w.hwnd, dc)
	for _, locale := range []i18n.Language{i18n.Chinese, i18n.English} {
		w.view.Language = locale
		for _, dpi := range []int{96, 120, 144, 192} {
			w.dpi = dpi
			w.setFonts()
			w.refresh()
			selectObject.Call(dc, w.font)
			layout := makeLayout(scale(704, dpi), scale(521, dpi), dpi, false, false)
			for _, id := range []int{idLanguageLabel, idBrowse, idLoad} {
				bounds := layout[id]
				padding := 0
				if id != idLanguageLabel {
					padding = scale(24, dpi)
				}
				r := winRect{}
				caption := strings.ReplaceAll(textOf(w.controls[id]), "&", "")
				user32.NewProc("DrawTextW").Call(dc, ptr(u16(caption)), ^uintptr(0), uintptr(unsafe.Pointer(&r)), 0x400|0x20|0x800)
				if int(r.Right)+padding > bounds.W || int(r.Bottom) > bounds.H {
					t.Errorf("locale %s DPI %d caption %d clipped: native text %dx%d + padding %d in %dx%d", locale, dpi, id, r.Right, r.Bottom, padding, bounds.W, bounds.H)
				}
			}
		}
	}
}

type captionBitmapInfo struct {
	Size                         uint32
	Width, Height                int32
	Planes, BitCount             uint16
	Compression, SizeImage       uint32
	XPelsPerMeter, YPelsPerMeter int32
	ClrUsed, ClrImportant        uint32
}

// This compares actual paint output with an unclipped native rendering of the
// same localized status. A wrapped line cut by the header loses glyph pixels.
func TestNativeMinimumHeaderPaintRetainsCompleteStatus(t *testing.T) {
	w := reviewWindow(t)
	dc, _, _ := user32.NewProc("GetDC").Call(w.hwnd)
	defer user32.NewProc("ReleaseDC").Call(w.hwnd, dc)
	const statusColor = uintptr(0xe4eeb1)
	const backgroundColor = uintptr(0x352319)
	dibColor := uint32((statusColor&255)<<16 | (statusColor & 0xff00) | (statusColor>>16)&255)
	for _, locale := range []i18n.Language{i18n.Chinese, i18n.English} {
		w.view.Language, w.view.State = locale, "ready"
		for _, dpi := range []int{96, 120, 144, 192} {
			w.dpi = dpi
			w.setFonts()
			moveWindow.Call(w.hwnd, 0, 0, uintptr(scale(720, dpi)), uintptr(scale(560, dpi)), 0)
			var bounds winRect
			getClientRect.Call(w.hwnd, uintptr(unsafe.Pointer(&bounds)))
			width, height := int(bounds.Right), scale(90, dpi)
			render := func(actual bool) int {
				mem, _, _ := gdi32.NewProc("CreateCompatibleDC").Call(dc)
				defer gdi32.NewProc("DeleteDC").Call(mem)
				info := captionBitmapInfo{Size: 40, Width: int32(width), Height: -int32(height), Planes: 1, BitCount: 32}
				var bits unsafe.Pointer
				bitmap, _, _ := gdi32.NewProc("CreateDIBSection").Call(mem, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
				if bitmap == 0 || bits == nil {
					t.Fatal("native DIB creation failed")
				}
				old, _, _ := selectObject.Call(mem, bitmap)
				defer func() { selectObject.Call(mem, old); deleteObject.Call(bitmap) }()
				if actual {
					w.paintDC(mem)
				} else {
					r := winRect{Right: int32(width), Bottom: int32(height)}
					brush, _, _ := createBrush.Call(backgroundColor)
					user32.NewProc("FillRect").Call(mem, uintptr(unsafe.Pointer(&r)), brush)
					deleteObject.Call(brush)
					selectObject.Call(mem, w.font)
					gdi32.NewProc("SetTextColor").Call(mem, statusColor)
					gdi32.NewProc("SetBkMode").Call(mem, 1)
					user32.NewProc("DrawTextW").Call(mem, ptr(u16(client.StateText("ready", locale))), ^uintptr(0), uintptr(unsafe.Pointer(&r)), 0x20|0x800)
				}
				gdi32.NewProc("GdiFlush").Call()
				count := 0
				for _, pixel := range unsafe.Slice((*uint32)(bits), width*height) {
					if pixel&0xffffff == dibColor {
						count++
					}
				}
				return count
			}
			actual, complete := render(true), render(false)
			if complete == 0 || actual != complete {
				t.Errorf("locale %s DPI %d status clipped in actual header paint: %d glyph pixels, complete native rendering %d", locale, dpi, actual, complete)
			}
		}
	}
}
