//go:build windows

package main

import (
	"golang.org/x/sys/windows"
	"remoteops/internal/i18n"
	"unsafe"
)

func showNativeError(message string, language ...i18n.Language) {
	lang := i18n.Chinese
	if len(language) > 0 {
		lang = language[0]
	}
	title, _ := windows.UTF16PtrFromString(i18n.Text(lang, "worker.startup.title"))
	text, err := windows.UTF16PtrFromString(message)
	if err != nil {
		text, _ = windows.UTF16PtrFromString(i18n.Text(lang, "worker.startup.generic"))
	}
	windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x10)
}
