package controller

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// SecureStateDirectory creates a dedicated protected leaf directory. Existing
// directories must already be secure: it never rewrites a workspace's ACL.
func SecureStateDirectory(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err = rejectStateLinks(absolute); err != nil {
		return err
	}
	created := false
	if _, err = os.Lstat(absolute); os.IsNotExist(err) {
		if err = os.Mkdir(absolute, 0700); err != nil {
			return fmt.Errorf("create dedicated state directory: %w", err)
		}
		created = true
	} else if err != nil {
		return err
	}
	if created {
		if err = protectStatePath(absolute, true); err != nil {
			return err
		}
	}
	if err = validateStateDirectory(absolute); err != nil {
		return fmt.Errorf("state directory must be dedicated and private; use a new directory instead of a shared workspace: %w", err)
	}
	return nil
}

// ProtectStateFile is called before a secret is written. Its parent directory
// must inherit access only to the current account and privileged system users.
func ProtectStateFile(path string) error {
	if err := rejectStateLinks(path); err != nil {
		return err
	}
	if err := validateStateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("state file must be a regular file")
	}
	if err = protectStatePath(path, false); err != nil {
		return err
	}
	return validateStatePath(path, false)
}
func validateStateDirectory(path string) error {
	if err := rejectStateLinks(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("state parent is not a directory")
	}
	return validateStatePath(path, true)
}
func rejectStateLinks(path string) error {
	p, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for {
		if info, err := os.Lstat(p); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return errors.New("state path cannot contain a symbolic link")
			}
			if err = rejectPlatformLink(p); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		parent := filepath.Dir(p)
		if parent == p {
			return nil
		}
		p = parent
	}
}
