//go:build !windows

package worker

import (
	"context"
	"errors"
	"remoteops/pkg/protocol"
)

func inspect(context.Context) (*protocol.SystemInfo, error) {
	return nil, errors.New("unsupported_platform")
}
func wingetPath() (string, error) { return "", errors.New("unsupported_platform") }
func installedVersion(context.Context, protocol.CatalogEntry) (string, error) {
	return "", errors.New("unsupported_platform")
}
