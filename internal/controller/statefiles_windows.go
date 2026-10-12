package controller

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"unsafe"
)

func stateOwnerSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid.Copy()
}
func protectStatePath(path string, dir bool) error {
	owner, err := stateOwnerSID()
	if err != nil {
		return err
	}
	inherit := ""
	if dir {
		inherit = "OICI"
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;" + inherit + ";FA;;;" + owner.String() + ")(A;" + inherit + ";FA;;;SY)(A;" + inherit + ";FA;;;BA)")
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	// Protected DACL drops inherited grants; child files inherit only these ACEs.
	// Preserve the OS-assigned owner: a workspace's Modify grant excludes
	// WRITE_OWNER, while the current owner can still set the DACL. The caller
	// validates that the owner is trusted after applying these permissions.
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		return fmt.Errorf("set private permissions on state path %q: %w", path, err)
	}
	return nil
}
func validateStatePath(path string, dir bool) error {
	current, err := stateOwnerSID()
	if err != nil {
		return err
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return errors.New("state path has no trusted owner")
	}
	trusted := func(s *windows.SID) bool {
		v := s.String()
		return v == current.String() || v == "S-1-5-18" || v == "S-1-5-32-544"
	}
	if !trusted(owner) {
		return errors.New("state path owner is not current user, Administrators, or SYSTEM")
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return errors.New("state path has no restrictive DACL")
	}
	currentAccess := false
	for i := uint32(0); i < 4096; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		err = windows.GetAce(acl, i, &ace)
		if err == windows.ERROR_INVALID_PARAMETER {
			if !currentAccess {
				return errors.New("state DACL must grant inheritable current-user access")
			}
			return nil
		}
		if err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("unsupported state DACL entry")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !trusted(sid) {
			return fmt.Errorf("state path grants access to an untrusted principal")
		}
		if dir && ace.Header.AceFlags&3 != 3 {
			return errors.New("state directory ACL must inherit to files and directories")
		}
		const fileAllAccess = 0x1f01ff
		if sid.String() == current.String() && ace.Header.AceFlags&8 == 0 && uint32(ace.Mask)&fileAllAccess == fileAllAccess {
			currentAccess = true
		}
	}
	return errors.New("state DACL is too large")
}
func rejectPlatformLink(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil {
		return err
	}
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("state path cannot contain a reparse point")
	}
	return nil
}
