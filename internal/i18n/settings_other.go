//go:build !windows

package i18n

import "os"

func replaceSettings(source, destination string) error {
	return os.Rename(source, destination)
}
