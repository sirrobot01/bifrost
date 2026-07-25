//go:build linux

package cli

import (
	"github.com/sirrobot01/bifrost/internal/netwatch"
)

func platformSnapshot(interfaceName string) (netwatch.Snapshot, error) {
	observer, err := netwatch.New(interfaceName)
	if err != nil {
		return netwatch.Snapshot{}, err
	}
	return observer.Snapshot()
}
