package controller

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestStateDirectoryUnderModifyOnlyParent(t *testing.T) {
	parent := t.TempDir()
	owner, err := stateOwnerSID()
	if err != nil {
		t.Fatal(err)
	}
	// Match a workspace that grants Modify but not WRITE_OWNER. Newly created
	// children inherit this grant even though the current user owns them.
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;0x1301bf;;;" + owner.String() + ")")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	before, err := windows.GetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(parent, "state")
	if err := SecureStateDirectory(state); err != nil {
		t.Fatalf("create private state under Modify-only parent: %v", err)
	}
	if err := validateStateDirectory(state); err != nil {
		t.Fatalf("state directory is not private: %v", err)
	}
	token := filepath.Join(state, "mcp.token")
	if err := os.WriteFile(token, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ProtectStateFile(token); err != nil {
		t.Fatalf("protect credential file: %v", err)
	}
	after, err := windows.GetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if before.String() != after.String() {
		t.Fatal("workspace owner or permissions were modified")
	}
}

func makeSharedForTest(path string) error {
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;WD)")
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
