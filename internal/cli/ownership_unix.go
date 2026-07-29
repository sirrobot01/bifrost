//go:build unix

package cli

import (
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"strconv"
	"syscall"
)

func protectConfigPath(string) error { return nil }

func applyServiceOwnership(configDir string, files []pendingFile) (bool, error) {
	if os.Geteuid() != 0 {
		return false, nil
	}
	account, err := user.Lookup(serviceAccount)
	if err != nil {
		return false, nil
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return false, nil
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return false, nil
	}
	if err := os.Chown(configDir, uid, gid); err != nil {
		return false, fmt.Errorf("set ownership of %s: %w", configDir, err)
	}
	for _, file := range files {
		if err := os.Chown(file.path, uid, gid); err != nil {
			return false, fmt.Errorf("set ownership of %s: %w", file.path, err)
		}
	}
	return true, nil
}

func applyAccountOwnership(account, configDir string, files []pendingFile) error {
	if os.Geteuid() != 0 {
		return nil
	}
	entry, err := user.Lookup(account)
	if err != nil {
		return nil
	}
	uid, err := strconv.Atoi(entry.Uid)
	if err != nil {
		return nil
	}
	gid, err := strconv.Atoi(entry.Gid)
	if err != nil {
		return nil
	}
	for _, file := range files {
		if err := os.Chown(file.path, uid, gid); err != nil {
			return fmt.Errorf("set ownership of %s: %w", file.path, err)
		}
	}
	if err := os.Chmod(configDir, 0o755); err != nil {
		return fmt.Errorf("set mode of %s: %w", configDir, err)
	}
	return nil
}

// matchConfigOwnership keeps a rewritten configuration owned by the account
// that owned it before, so an edit made with sudo does not leave a file the
// service account can no longer read.
func matchConfigOwnership(path, _ string, info fs.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return os.Chown(path, int(stat.Uid), int(stat.Gid))
}
