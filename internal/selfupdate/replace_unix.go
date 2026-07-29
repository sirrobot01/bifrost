//go:build unix

package selfupdate

import (
	"fmt"
	"os"
	"path/filepath"
)

func executableName() string { return "bifrost" }

func Replace(path string, contents []byte) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(resolved), ".bifrost-update-*")
	if err != nil {
		return fmt.Errorf("write beside %s: %w", resolved, err)
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, info.Mode().Perm()); err != nil {
		return err
	}
	if err := matchOwnership(name, info); err != nil {
		return err
	}
	return os.Rename(name, resolved)
}
