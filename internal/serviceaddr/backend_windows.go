//go:build windows

package serviceaddr

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ipHelperAPI                     = windows.NewLazySystemDLL("iphlpapi.dll")
	initializeUnicastIPAddressEntry = ipHelperAPI.NewProc("InitializeUnicastIpAddressEntry")
	createUnicastIPAddressEntry     = ipHelperAPI.NewProc("CreateUnicastIpAddressEntry")
	deleteUnicastIPAddressEntry     = ipHelperAPI.NewProc("DeleteUnicastIpAddressEntry")
)

type WindowsBackend struct {
	interfaceIndex uint32
}

func NewWindowsBackend(interfaceName string) (*WindowsBackend, error) {
	if interfaceName == "" {
		return nil, errors.New("network interface is required")
	}
	device, err := net.InterfaceByName(interfaceName)
	if err != nil {
		return nil, fmt.Errorf("find network interface %q: %w", interfaceName, err)
	}
	return &WindowsBackend{interfaceIndex: uint32(device.Index)}, nil
}

func (b *WindowsBackend) Ensure(prefix netip.Prefix) error {
	row, err := b.row(prefix)
	if err != nil {
		return err
	}
	if err := windows.GetUnicastIpAddressEntry(&row); err == nil {
		return nil
	} else if !errors.Is(err, windows.ERROR_NOT_FOUND) {
		return err
	}
	row, err = b.row(prefix)
	if err != nil {
		return err
	}
	result, _, _ := createUnicastIPAddressEntry.Call(uintptr(unsafe.Pointer(&row)))
	if result != 0 && syscall.Errno(result) != windows.ERROR_OBJECT_ALREADY_EXISTS {
		return syscall.Errno(result)
	}
	return nil
}

func (b *WindowsBackend) Remove(prefix netip.Prefix) error {
	row, err := b.row(prefix)
	if err != nil {
		return err
	}
	result, _, _ := deleteUnicastIPAddressEntry.Call(uintptr(unsafe.Pointer(&row)))
	if result != 0 && syscall.Errno(result) != windows.ERROR_NOT_FOUND {
		return syscall.Errno(result)
	}
	return nil
}

func (b *WindowsBackend) Status(address netip.Addr) (AddressStatus, error) {
	row, err := b.row(netip.PrefixFrom(address, 64))
	if err != nil {
		return AddressAbsent, err
	}
	if err := windows.GetUnicastIpAddressEntry(&row); err != nil {
		if errors.Is(err, windows.ERROR_NOT_FOUND) {
			return AddressAbsent, nil
		}
		return AddressAbsent, err
	}
	switch row.DadState {
	case windows.IpDadStateDuplicate:
		return AddressDADFailed, nil
	case windows.IpDadStatePreferred, windows.IpDadStateDeprecated:
		return AddressReady, nil
	default:
		return AddressTentative, nil
	}
}

func (b *WindowsBackend) row(prefix netip.Prefix) (windows.MibUnicastIpAddressRow, error) {
	if !prefix.IsValid() || !prefix.Addr().Is6() || prefix.Bits() != 64 {
		return windows.MibUnicastIpAddressRow{}, errors.New("managed address must be an IPv6 /64")
	}
	var row windows.MibUnicastIpAddressRow
	initializeUnicastIPAddressEntry.Call(uintptr(unsafe.Pointer(&row)))
	row.Address.Family = windows.AF_INET6
	row.Address.Addr = prefix.Addr().As16()
	row.InterfaceIndex = b.interfaceIndex
	row.OnLinkPrefixLength = uint8(prefix.Bits())
	row.SkipAsSource = 1
	return row, nil
}

var _ AddressBackend = (*WindowsBackend)(nil)
