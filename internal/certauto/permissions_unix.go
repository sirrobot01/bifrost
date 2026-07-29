//go:build unix

package certauto

import "os"

func applyFilePermissions(path string, mode os.FileMode) error {
	return os.Chmod(path, mode)
}
