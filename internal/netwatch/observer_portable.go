//go:build darwin || freebsd || openbsd || windows

package netwatch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"time"

	"github.com/sirrobot01/bifrost/internal/serviceaddr"
)

const portablePollInterval = 2 * time.Second

// portableObserver uses the operating system's interface API. Polling is
// deliberately bounded and compares complete snapshots, so an OS without a
// Go-exposed route event stream still converges without producing reconcile
// churn.
type portableObserver struct {
	interfaceName string
}

// NewPolling returns an observer backed by the operating system's portable
// interface view. Native platform packages use it where Go exposes no stable
// route-notification API.
func NewPolling(interfaceName string) (Observer, error) {
	if interfaceName == "" {
		return nil, errors.New("network interface is required")
	}
	if _, err := net.InterfaceByName(interfaceName); err != nil {
		return nil, fmt.Errorf("find network interface %q: %w", interfaceName, err)
	}
	return &portableObserver{interfaceName: interfaceName}, nil
}

func (o *portableObserver) Snapshot() (Snapshot, error) {
	device, err := net.InterfaceByName(o.interfaceName)
	if err != nil {
		return Snapshot{}, fmt.Errorf("find network interface %q: %w", o.interfaceName, err)
	}
	addresses, err := device.Addrs()
	if err != nil {
		return Snapshot{}, fmt.Errorf("list addresses on %q: %w", o.interfaceName, err)
	}
	snapshot := Snapshot{InterfaceName: device.Name, InterfaceIndex: device.Index, MTU: device.MTU}
	for _, raw := range addresses {
		prefix, err := netip.ParsePrefix(raw.String())
		if err != nil || !prefix.Addr().Is6() {
			continue
		}
		snapshot.Candidates = append(snapshot.Candidates, serviceaddr.Candidate{Prefix: prefix})
	}
	return snapshot, nil
}

func (o *portableObserver) Observe(ctx context.Context, snapshots chan<- Snapshot) error {
	var previous Snapshot
	ticker := time.NewTicker(portablePollInterval)
	defer ticker.Stop()
	for {
		current, err := o.Snapshot()
		if err != nil {
			return err
		}
		if current.InterfaceName != previous.InterfaceName || current.InterfaceIndex != previous.InterfaceIndex || current.MTU != previous.MTU || !slices.Equal(current.Candidates, previous.Candidates) {
			select {
			case snapshots <- current:
				previous = current
			case <-ctx.Done():
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
