//go:build !windows

package controller

import (
	"errors"
	"os"
)

func protectStatePath(path string, dir bool) error {
	mode := os.FileMode(0600)
	if dir {
		mode = 0700
	}
	return os.Chmod(path, mode)
}
func validateStatePath(path string, dir bool) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0077 != 0 {
		return errors.New("state path grants group or other access")
	}
	return nil
}
func rejectPlatformLink(string) error { return nil }
