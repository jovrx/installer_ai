//go:build !windows

package controller

import "os"

func makeSharedForTest(path string) error { return os.Chmod(path, 0755) }
