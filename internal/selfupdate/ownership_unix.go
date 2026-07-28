//go:build unix

package selfupdate

import (
	"io/fs"
	"os"
	"syscall"
)

// matchOwnership gives the replacement the same owner as the binary it
// replaces, so an update run as root does not hand a package-owned path to a
// different account.
func matchOwnership(path string, info fs.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return os.Chown(path, int(stat.Uid), int(stat.Gid))
}
