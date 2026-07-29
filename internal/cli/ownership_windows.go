//go:build windows

package cli

import (
	"fmt"
	"io/fs"

	"golang.org/x/sys/windows"
)

func applyServiceOwnership(configDir string, files []pendingFile) (bool, error) {
	if err := protectConfigPath(configDir); err != nil {
		return false, err
	}
	for _, file := range files {
		if err := protectConfigPath(file.path); err != nil {
			return false, err
		}
	}
	return true, nil
}

func applyAccountOwnership(_ string, configDir string, files []pendingFile) error {
	_, err := applyServiceOwnership(configDir, files)
	return err
}

func protectConfigPath(path string) error {
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("protect %s: %w", path, err)
	}
	return nil
}

func matchConfigOwnership(path, source string, _ fs.FileInfo) error {
	descriptor, err := windows.GetNamedSecurityInfo(source, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
