//go:build !linux

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"

	"github.com/sirrobot01/bifrost/internal/config"
	"github.com/sirrobot01/bifrost/internal/netwatch"
	"github.com/sirrobot01/bifrost/internal/serviceaddr"
)

func platformSnapshot(interfaceName string) (netwatch.Snapshot, error) {
	networkInterface, err := net.InterfaceByName(interfaceName)
	if err != nil {
		return netwatch.Snapshot{}, fmt.Errorf("find network interface %q: %w", interfaceName, err)
	}
	addresses, err := networkInterface.Addrs()
	if err != nil {
		return netwatch.Snapshot{}, fmt.Errorf("list addresses on %q: %w", interfaceName, err)
	}
	snapshot := netwatch.Snapshot{InterfaceName: interfaceName, InterfaceIndex: networkInterface.Index, MTU: networkInterface.MTU}
	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address.String())
		if err == nil && prefix.Addr().Is6() {
			snapshot.Candidates = append(snapshot.Candidates, serviceaddr.Candidate{Prefix: prefix})
		}
	}
	return snapshot, nil
}

func platformServe(context.Context, config.Config, bool, *slog.Logger, io.Writer) error {
	return errors.New("the Bifrost home runtime requires Linux")
}
