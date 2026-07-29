//go:build windows

package certauto

import (
	"os"

	"golang.org/x/sys/windows"
)

func applyFilePermissions(path string, mode os.FileMode) error {
	if mode.Perm()&0o077 != 0 {
		return nil
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;SY)(A;;FA;;;BA)")
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
