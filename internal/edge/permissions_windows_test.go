//go:build windows

package edge

import "golang.org/x/sys/windows"

func protectTestKey(path string) error {
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;GR;;;SY)(A;;GR;;;BA)")
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
