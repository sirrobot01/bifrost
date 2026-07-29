//go:build windows

package config

import (
	"os"
	"path/filepath"
)

func defaultConfigDirectory() string { return filepath.Join(programData(), "Bifrost") }
func defaultStateDirectory() string  { return filepath.Join(programData(), "Bifrost", "state") }
func defaultDockerSocket() string    { return `\\.\pipe\docker_engine` }

func programData() string {
	if directory := os.Getenv("ProgramData"); directory != "" {
		return directory
	}
	return `C:\ProgramData`
}
