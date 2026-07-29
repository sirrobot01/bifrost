//go:build windows

package config

import (
	"errors"
	"io/fs"
	"unsafe"

	"golang.org/x/sys/windows"
)

func secretPermissions(path string, _ fs.FileInfo) error {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if dacl == nil {
		return errors.New("secret file has no access-control list")
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask&(windows.GENERIC_READ|windows.GENERIC_ALL|windows.FILE_GENERIC_READ|windows.FILE_READ_DATA) == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsWellKnown(windows.WinLocalSystemSid) && !sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
			return errors.New("secret file access-control list permits unprivileged readers")
		}
	}
	return nil
}
