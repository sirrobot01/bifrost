//go:build linux

package serviceaddr

import (
	"errors"
	"fmt"
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// NetlinkBackend manages addresses on one Linux network interface.
type NetlinkBackend struct {
	interfaceName string
}

// NewNetlinkBackend returns an address backend for interfaceName.
func NewNetlinkBackend(interfaceName string) (*NetlinkBackend, error) {
	if interfaceName == "" {
		return nil, errors.New("network interface is required")
	}
	if _, err := netlink.LinkByName(interfaceName); err != nil {
		return nil, fmt.Errorf("find network interface %q: %w", interfaceName, err)
	}
	return &NetlinkBackend{interfaceName: interfaceName}, nil
}

// Ensure adds prefix when it is not already present.
func (b *NetlinkBackend) Ensure(prefix netip.Prefix) error {
	link, err := netlink.LinkByName(b.interfaceName)
	if err != nil {
		return err
	}
	address, err := netlinkAddress(prefix)
	if err != nil {
		return err
	}
	address.Flags = unix.IFA_F_NOPREFIXROUTE
	if err := netlink.AddrAdd(link, address); err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	return nil
}

// Remove deletes prefix when it is present.
func (b *NetlinkBackend) Remove(prefix netip.Prefix) error {
	link, err := netlink.LinkByName(b.interfaceName)
	if err != nil {
		return err
	}
	address, err := netlinkAddress(prefix)
	if err != nil {
		return err
	}
	if err := netlink.AddrDel(link, address); err != nil && !errors.Is(err, unix.EADDRNOTAVAIL) && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return nil
}

// Status reports the DAD state of address.
func (b *NetlinkBackend) Status(address netip.Addr) (AddressStatus, error) {
	link, err := netlink.LinkByName(b.interfaceName)
	if err != nil {
		return AddressAbsent, err
	}
	addresses, err := netlink.AddrList(link, netlink.FAMILY_V6)
	if err != nil {
		return AddressAbsent, err
	}

	for _, current := range addresses {
		currentAddress, ok := netip.AddrFromSlice(current.IP)
		if !ok || currentAddress != address {
			continue
		}
		flags := uint32(current.Flags)
		switch {
		case flags&unix.IFA_F_DADFAILED != 0:
			return AddressDADFailed, nil
		case flags&unix.IFA_F_TENTATIVE != 0:
			return AddressTentative, nil
		default:
			return AddressReady, nil
		}
	}

	return AddressAbsent, nil
}

func netlinkAddress(prefix netip.Prefix) (*netlink.Addr, error) {
	if !prefix.IsValid() || !prefix.Addr().Is6() || prefix.Bits() != 64 {
		return nil, errors.New("managed address must be an IPv6 /64")
	}
	return &netlink.Addr{IPNet: &net.IPNet{
		IP:   net.IP(prefix.Addr().AsSlice()),
		Mask: net.CIDRMask(prefix.Bits(), 128),
	}}, nil
}
