//go:build !windows

package main

import "errors"

func launchBrowser(string) error { return errors.New("unsupported_platform") }
