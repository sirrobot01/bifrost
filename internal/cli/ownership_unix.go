//go:build unix

package cli

import (
	"io/fs"
	"os"
	"syscall"
)

// matchConfigOwnership keeps a rewritten configuration owned by the account
// that owned it before, so an edit made with sudo does not leave a file the
// service account can no longer read.
func matchConfigOwnership(path string, info fs.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return os.Chown(path, int(stat.Uid), int(stat.Gid))
}
