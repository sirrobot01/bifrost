//go:build windows

package selfupdate

import "errors"

func executableName() string { return "bifrost.exe" }

func Replace(string, []byte) error {
	return errors.New("in-place upgrade is unavailable on Windows; run install.ps1 from an Administrator PowerShell")
}
