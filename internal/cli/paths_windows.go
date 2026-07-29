//go:build windows

package cli

import (
	"os"
	"path/filepath"
)

func platformConfigDirectory() string {
	if directory := os.Getenv("ProgramData"); directory != "" {
		return filepath.Join(directory, "Bifrost")
	}
	return `C:\ProgramData\Bifrost`
}

func elevatedCommand(command string) string { return command }
