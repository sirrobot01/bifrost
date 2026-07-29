//go:build windows

package netwatch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsObserver struct {
	interfaceName string
}

func NewWindows(interfaceName string) (Observer, error) {
	if interfaceName == "" {
		return nil, errors.New("network interface is required")
	}
	if _, err := net.InterfaceByName(interfaceName); err != nil {
		return nil, fmt.Errorf("find network interface %q: %w", interfaceName, err)
	}
	return &windowsObserver{interfaceName: interfaceName}, nil
}

func (o *windowsObserver) Snapshot() (Snapshot, error) {
	device, err := net.InterfaceByName(o.interfaceName)
	if err != nil {
		return Snapshot{}, fmt.Errorf("find network interface %q: %w", o.interfaceName, err)
	}
	var table *windows.MibUnicastIpAddressTable
	if err := windows.GetUnicastIpAddressTable(windows.AF_INET6, &table); err != nil {
		return Snapshot{}, fmt.Errorf("list IPv6 addresses on %q: %w", o.interfaceName, err)
	}
	snapshot := Snapshot{InterfaceName: device.Name, InterfaceIndex: device.Index, MTU: device.MTU}
	if table == nil {
		return snapshot, nil
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))

	observedAt := time.Now()
	rows := unsafe.Slice(&table.Table[0], table.NumEntries)
	for _, row := range rows {
		if row.InterfaceIndex != uint32(device.Index) || row.Address.Family != windows.AF_INET6 || row.OnLinkPrefixLength > 128 {
			continue
		}
		candidate, ok := (addressState{
			prefix:            netip.PrefixFrom(netip.AddrFrom16(row.Address.Addr), int(row.OnLinkPrefixLength)),
			temporary:         row.SuffixOrigin == windows.IpSuffixOriginRandom,
			deprecated:        row.DadState == windows.IpDadStateDeprecated,
			tentative:         row.DadState == windows.IpDadStateTentative,
			dadFailed:         row.DadState == windows.IpDadStateDuplicate,
			preferredLifetime: row.PreferredLifetime,
			validLifetime:     row.ValidLifetime,
		}).candidate(observedAt)
		if ok {
			snapshot.Candidates = append(snapshot.Candidates, candidate)
		}
	}
	return snapshot, nil
}

func (o *windowsObserver) Observe(ctx context.Context, snapshots chan<- Snapshot) error {
	var previous Snapshot
	ticker := time.NewTicker(2 * time.Second)
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
