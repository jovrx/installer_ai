//go:build windows

package gui

import (
	"remoteops/internal/i18n"
	"remoteops/internal/worker/client"
	"strings"
	"time"
	"unsafe"
)

func (w *window) setFonts() {
	old, oldTitle := w.font, w.titleFont
	makeFont := func(size, weight int) uintptr {
		h, _, _ := gdi32.NewProc("CreateFontW").Call(uintptr(int32(-scale(size, w.dpi))), 0, 0, 0, uintptr(weight), 0, 0, 0, 1, 0, 0, 5, 0, ptr(u16("Microsoft YaHei UI")))
		return h
	}
	w.font = makeFont(14, 400)
	w.titleFont = makeFont(23, 600)
	for _, h := range w.controls {
		sendMessage.Call(h, 0x30, w.font, 1)
	}
	if old != 0 {
		deleteObject.Call(old)
	}
	if oldTitle != 0 {
		deleteObject.Call(oldTitle)
	}
}
func (w *window) paint() {
	var ps paintStruct
	dc, _, _ := user32.NewProc("BeginPaint").Call(w.hwnd, uintptr(unsafe.Pointer(&ps)))
	defer user32.NewProc("EndPaint").Call(w.hwnd, uintptr(unsafe.Pointer(&ps)))
	w.paintDC(dc)
}
func (w *window) paintDC(dc uintptr) {
	var bounds winRect
	getClientRect.Call(w.hwnd, uintptr(unsafe.Pointer(&bounds)))
	width, height := int(bounds.Right)*96/w.dpi, int(bounds.Bottom)*96/w.dpi
	// Double buffering avoids flicker while indeterminate progress is animated.
	mem, _, _ := gdi32.NewProc("CreateCompatibleDC").Call(dc)
	bitmap, _, _ := gdi32.NewProc("CreateCompatibleBitmap").Call(dc, uintptr(bounds.Right), uintptr(bounds.Bottom))
	oldBitmap, _, _ := selectObject.Call(mem, bitmap)
	defer func() {
		gdi32.NewProc("BitBlt").Call(dc, 0, 0, uintptr(bounds.Right), uintptr(bounds.Bottom), mem, 0, 0, 0xcc0020)
		selectObject.Call(mem, oldBitmap)
		deleteObject.Call(bitmap)
		gdi32.NewProc("DeleteDC").Call(mem)
	}()
	fill := func(x, y, ww, hh int, color uintptr) {
		r := winRect{int32(scale(x, w.dpi)), int32(scale(y, w.dpi)), int32(scale(x+ww, w.dpi)), int32(scale(y+hh, w.dpi))}
		b, _, _ := createBrush.Call(color)
		user32.NewProc("FillRect").Call(mem, uintptr(unsafe.Pointer(&r)), b)
		deleteObject.Call(b)
	}
	text := func(s string, x, y, ww, hh int, color uintptr, title bool) {
		f := w.font
		if title {
			f = w.titleFont
		}
		selectObject.Call(mem, f)
		gdi32.NewProc("SetTextColor").Call(mem, color)
		gdi32.NewProc("SetBkMode").Call(mem, 1)
		r := winRect{int32(scale(x, w.dpi)), int32(scale(y, w.dpi)), int32(scale(x+ww, w.dpi)), int32(scale(y+hh, w.dpi))}
		user32.NewProc("DrawTextW").Call(mem, ptr(u16(s)), ^uintptr(0), uintptr(unsafe.Pointer(&r)), 0x10|0x800|0x8000)
	}
	fill(0, 0, width, height, 0xf6f3ef)
	fill(0, 0, width, 90, 0x352319)
	fill(0, 86, width, 4, 0xafa21c)
	text("RemoteOps", 28, 16, 220, 32, 0xffffff, true)
	if !w.connected() {
		text(i18n.Text(w.view.Language, "worker.gui.tagline"), 30, 54, width-60, 24, 0xe3d8ca, false)
	}
	text(client.StateText(w.view.State, w.view.Language), width-340, 25, 316, 30, 0xe4eeb1, false)
	text(i18n.Text(w.view.Language, "worker.gui.end_anytime"), 32, height-41, width-320, 24, 0x706451, false)
	if !w.connected() {
		fill(20, 104, width-40, height-166, 0xffffff)
	} else {
		fill(20, 104, width-40, 170, 0xffffff)
		fill(20, 286, width-40, height-344, 0xffffff)
		heading := i18n.Text(w.view.Language, "worker.gui.current_task")
		v, ok := selectedTask(w.view.Tasks, w.selected)
		if !ok {
			v, ok = client.PrimaryTask(w.view.Tasks)
		}
		stage := i18n.Text(w.view.Language, "worker.gui.waiting")
		detail := i18n.Text(w.view.Language, "worker.gui.progress_hint")
		if w.view.State == "connecting" {
			stage = i18n.Text(w.view.Language, "worker.gui.connecting")
			detail = i18n.Text(w.view.Language, "worker.gui.disconnect_hint")
		}
		if w.view.State == "disconnected" {
			stage = i18n.Text(w.view.Language, "worker.gui.ended")
			detail = i18n.Text(w.view.Language, "worker.gui.ended_hint")
		}
		if ok {
			heading = client.TaskTitle(v, w.view.Language)
			stage = taskStatus(v, w.view.Language)
			if v.Task.State == "running" {
				stage = client.ProgressText(v.Progress, w.view.Language)
			}
			start := v.Task.CreatedAt
			if v.Task.StartedAt != nil {
				start = *v.Task.StartedAt
			}
			end := time.Now()
			if v.Task.FinishedAt != nil {
				end = *v.Task.FinishedAt
			}
			elapsed := max(time.Duration(0), end.Sub(start))
			if start.IsZero() {
				elapsed = 0
			}
			detail = i18n.Text(w.view.Language, "worker.gui.elapsed", int(elapsed.Minutes()), int(elapsed.Seconds())%60)
		}
		text(heading, 32, 118, width-64, 34, 0x352619, true)
		text(stage, 32, 154, width-64, 36, 0x65543e, false)
		fill(32, 198, width-64, 8, 0xede7df)
		if ok && v.Task.State == "running" && v.Progress.Stage == "downloading" && v.Progress.TotalBytes > 0 {
			ratio := float64(max(int64(0), min(v.Progress.CompletedBytes, v.Progress.TotalBytes))) / float64(v.Progress.TotalBytes)
			fill(32, 198, int(float64(width-64)*ratio), 8, 0xafa21c)
		} else if w.view.State == "connecting" || (ok && (v.Task.State == "running" || v.Task.State == "queued")) {
			bar := max(24, (width-64)/5)
			offset := (w.tickCount * 7) % max(1, width-64-bar)
			fill(32+offset, 198, bar, 8, 0xafa21c)
		} else if ok && v.Task.State == "succeeded" {
			fill(32, 198, width-64, 8, 0xafa21c)
		}
		text(i18n.Text(w.view.Language, "worker.gui.stages"), 32, 211, width-64, 22, 0x847462, false)
		text(detail, 32, 234, width-238, 44, 0x706451, false)
		scope := i18n.Text(w.view.Language, "worker.gui.recent_scope")
		if len(w.view.Scope) > 0 {
			scope += "  |  " + strings.Join(w.view.Scope, " / ")
		}
		text(scope, 32, 290, width-64, 24, 0x65543e, false)
	}
	if w.connected() {
		line := i18n.Text(w.view.Language, "worker.gui.controller") + client.SafeText(w.view.Controller)
		if !w.view.ExpiresAt.IsZero() && w.view.State == "online" {
			remaining := max(time.Duration(0), time.Until(w.view.ExpiresAt))
			line += i18n.Text(w.view.Language, "worker.gui.remaining", int(remaining.Minutes()), int(remaining.Seconds())%60)
		}
		text(line, 30, 54, width-60, 24, 0xe3d8ca, false)
	}
}
