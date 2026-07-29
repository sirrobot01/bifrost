//go:build unix

package config

import (
	"fmt"
	"io/fs"
)

func secretPermissions(_ string, info fs.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("secret file permissions %04o allow group or other access", info.Mode().Perm())
	}
	return nil
}
