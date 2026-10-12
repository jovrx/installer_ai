package controller

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateDirectoryDoesNotAlterExistingSharedParent(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "shared")
	if err := os.Mkdir(shared, 0755); err != nil {
		t.Fatal(err)
	}
	if err := makeSharedForTest(shared); err != nil {
		t.Fatal(err)
	}
	if err := SecureStateDirectory(shared); err == nil {
		t.Fatal("shared state directory accepted")
	}
	if err := validateStateDirectory(shared); err == nil {
		t.Fatal("rejected parent ACL was modified")
	}
	dedicated := filepath.Join(shared, "remoteops-state")
	if err := SecureStateDirectory(dedicated); err != nil {
		t.Fatal(err)
	}
	if err := validateStateDirectory(dedicated); err != nil {
		t.Fatal("new state directory is not protected", err)
	}
	file := filepath.Join(dedicated, "token")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ProtectStateFile(file); err != nil {
		t.Fatal(err)
	}
	if err := validateStatePath(file, false); err != nil {
		t.Fatal("file ACL is not protected", err)
	}
}

func TestSQLiteSidecarsInheritPrivateStateDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "db.sqlite")
	s, err := New(Options{DBPath: path, AdminToken: admin})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); err != nil {
			t.Fatal("expected SQLite sidecar", suffix, err)
		}
		if err := validateStatePath(path+suffix, false); err != nil {
			t.Fatal("SQLite sidecar access is not private", suffix, err)
		}
	}
}
