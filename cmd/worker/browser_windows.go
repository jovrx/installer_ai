//go:build windows

package main

import "golang.org/x/sys/windows"

func launchBrowser(url string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	target, _ := windows.UTF16PtrFromString(url)
	return windows.ShellExecute(0, verb, target, nil, nil, 1)
}
