//go:build !windows

package installers

import (
	"context"
	"errors"
)

func FreeSpace(string) (uint64, error)        { return 0, errors.New("unsupported_platform") }
func SystemExecutable(string) (string, error) { return "", errors.New("unsupported_platform") }
func Run(context.Context, string, []string, string, func(string, string)) (int, error) {
	return -1, errors.New("unsupported_platform")
}
