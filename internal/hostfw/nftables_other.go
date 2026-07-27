//go:build !linux

package hostfw

// New reports that managed firewall mode is unavailable. The home role only
// runs on Linux; this keeps the rest of the tree building elsewhere.
func New() (Manager, error) {
	return nil, ErrUnsupported
}
