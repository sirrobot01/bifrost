//go:build linux

package netwatch

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/sirrobot01/bifrost/internal/serviceaddr"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const eventBufferSize = 16

// Observer reports IPv6 state changes for one Linux network interface.
type Observer struct {
	interfaceName string
}

// New returns an Observer for interfaceName.
func New(interfaceName string) (*Observer, error) {
	if interfaceName == "" {
		return nil, errors.New("network interface is required")
	}
	if _, err := netlink.LinkByName(interfaceName); err != nil {
		return nil, fmt.Errorf("find network interface %q: %w", interfaceName, err)
	}

	return &Observer{interfaceName: interfaceName}, nil
}

// Snapshot reads the current IPv6 state of the observed interface.
func (o *Observer) Snapshot() (Snapshot, error) {
	link, err := netlink.LinkByName(o.interfaceName)
	if err != nil {
		return Snapshot{}, fmt.Errorf("find network interface %q: %w", o.interfaceName, err)
	}

	addresses, err := netlink.AddrList(link, netlink.FAMILY_V6)
	if err != nil {
		return Snapshot{}, fmt.Errorf("list IPv6 addresses on %q: %w", o.interfaceName, err)
	}

	attrs := link.Attrs()
	observedAt := time.Now()
	snapshot := Snapshot{
		InterfaceName:  attrs.Name,
		InterfaceIndex: attrs.Index,
		MTU:            attrs.MTU,
		Candidates:     make([]serviceaddr.Candidate, 0, len(addresses)),
	}

	for _, address := range addresses {
		candidate, ok := candidateFromNetlink(address, observedAt)
		if ok {
			snapshot.Candidates = append(snapshot.Candidates, candidate)
		}
	}

	return snapshot, nil
}

// Observe sends an initial snapshot followed by a new snapshot after each relevant netlink event.
func (o *Observer) Observe(ctx context.Context, snapshots chan<- Snapshot) error {
	link, err := netlink.LinkByName(o.interfaceName)
	if err != nil {
		return fmt.Errorf("find network interface %q: %w", o.interfaceName, err)
	}
	interfaceIndex := link.Attrs().Index

	done := make(chan struct{})
	defer close(done)

	addressUpdates := make(chan netlink.AddrUpdate, eventBufferSize)
	linkUpdates := make(chan netlink.LinkUpdate, eventBufferSize)
	routeUpdates := make(chan netlink.RouteUpdate, eventBufferSize)
	subscriptionErrors := make(chan error, 1)
	onSubscriptionError := func(err error) {
		select {
		case subscriptionErrors <- err:
		default:
		}
	}

	if err := netlink.AddrSubscribeWithOptions(addressUpdates, done, netlink.AddrSubscribeOptions{
		ErrorCallback: onSubscriptionError,
	}); err != nil {
		return fmt.Errorf("subscribe to address changes: %w", err)
	}
	if err := netlink.LinkSubscribeWithOptions(linkUpdates, done, netlink.LinkSubscribeOptions{
		ErrorCallback: onSubscriptionError,
	}); err != nil {
		return fmt.Errorf("subscribe to link changes: %w", err)
	}
	if err := netlink.RouteSubscribeWithOptions(routeUpdates, done, netlink.RouteSubscribeOptions{
		ErrorCallback: onSubscriptionError,
	}); err != nil {
		return fmt.Errorf("subscribe to route changes: %w", err)
	}

	emit := func() error {
		snapshot, err := o.Snapshot()
		if err != nil {
			var notFound netlink.LinkNotFoundError
			if !errors.As(err, &notFound) {
				return err
			}
			snapshot = Snapshot{InterfaceName: o.interfaceName}
		}
		interfaceIndex = snapshot.InterfaceIndex
		select {
		case snapshots <- snapshot:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if err := emit(); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-subscriptionErrors:
			return fmt.Errorf("netlink subscription: %w", err)
		case update, ok := <-addressUpdates:
			if !ok {
				return errors.New("address subscription closed")
			}
			if update.LinkIndex == interfaceIndex && update.LinkAddress.IP.To4() == nil {
				if err := emit(); err != nil {
					return err
				}
			}
		case update, ok := <-linkUpdates:
			if !ok {
				return errors.New("link subscription closed")
			}
			if update.Link != nil && (update.Attrs().Index == interfaceIndex || update.Attrs().Name == o.interfaceName) {
				if err := emit(); err != nil {
					return err
				}
			}
		case update, ok := <-routeUpdates:
			if !ok {
				return errors.New("route subscription closed")
			}
			if update.LinkIndex == interfaceIndex && update.Family == unix.AF_INET6 {
				if err := emit(); err != nil {
					return err
				}
			}
		}
	}
}

func candidateFromNetlink(address netlink.Addr, observedAt time.Time) (serviceaddr.Candidate, bool) {
	if address.IPNet == nil || address.Scope != int(netlink.SCOPE_UNIVERSE) {
		return serviceaddr.Candidate{}, false
	}

	ip, ok := netip.AddrFromSlice(address.IP)
	if !ok || !ip.Is6() || ip.Is4In6() {
		return serviceaddr.Candidate{}, false
	}
	ones, bits := address.Mask.Size()
	if bits != 128 {
		return serviceaddr.Candidate{}, false
	}

	flags := uint32(address.Flags)
	state := addressState{
		prefix:            netip.PrefixFrom(ip, ones),
		temporary:         flags&unix.IFA_F_TEMPORARY != 0,
		deprecated:        flags&unix.IFA_F_DEPRECATED != 0,
		tentative:         flags&unix.IFA_F_TENTATIVE != 0,
		dadFailed:         flags&unix.IFA_F_DADFAILED != 0,
		preferredLifetime: lifetimeSeconds(address.PreferedLft),
		validLifetime:     lifetimeSeconds(address.ValidLft),
	}

	return state.candidate(observedAt)
}
