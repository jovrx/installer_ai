//go:build !windows

package main

import (
	"fmt"
	"os"
	"remoteops/internal/i18n"
)

func showNativeError(message string, language ...i18n.Language) { fmt.Fprintln(os.Stderr, message) }
