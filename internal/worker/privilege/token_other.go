//go:build !windows

package privilege

import "errors"

type helperHandle uintptr

func Elevated() bool                            { return false }
func launchHelper(string) (helperHandle, error) { return 0, errors.New("unsupported_platform") }
func closeHelper(helperHandle)                  {}
func secureDirectory(string) error              { return errors.New("unsupported_platform") }
