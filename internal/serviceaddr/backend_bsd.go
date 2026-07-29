//go:build darwin || freebsd || openbsd

package serviceaddr

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"strings"
)

// IfconfigStyle names the command-line grammar used by one BSD ifconfig.
type IfconfigStyle uint8

const (
	IfconfigDarwin IfconfigStyle = iota
	IfconfigFreeBSD
	IfconfigOpenBSD
)

// ifconfigBackend manages IPv6 aliases with the native ifconfig shipped by
// macOS and the BSDs. It reads DAD flags back from the kernel before reporting
// an address ready.
type ifconfigBackend struct {
	interfaceName string
	style         IfconfigStyle
	run           func(...string) ([]byte, error)
}

// NewIfconfigBackend returns an address backend using the platform's base
// ifconfig utility and the explicitly selected grammar.
func NewIfconfigBackend(interfaceName string, style IfconfigStyle) (AddressBackend, error) {
	if interfaceName == "" {
		return nil, errors.New("network interface is required")
	}
	if _, err := net.InterfaceByName(interfaceName); err != nil {
		return nil, fmt.Errorf("find network interface %q: %w", interfaceName, err)
	}
	backend := &ifconfigBackend{interfaceName: interfaceName, style: style}
	backend.run = func(arguments ...string) ([]byte, error) {
		command := exec.Command("/sbin/ifconfig", arguments...)
		output, err := command.CombinedOutput()
		if err != nil {
			return output, fmt.Errorf("ifconfig %s: %w: %s", strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
		}
		return output, nil
	}
	return backend, nil
}

func (b *ifconfigBackend) Ensure(prefix netip.Prefix) error {
	if err := validateManagedPrefix(prefix); err != nil {
		return err
	}
	status, err := b.Status(prefix.Addr())
	if err != nil {
		return err
	}
	if status != AddressAbsent {
		return nil
	}
	_, err = b.run(b.addArguments(prefix)...)
	return err
}

func (b *ifconfigBackend) Remove(prefix netip.Prefix) error {
	if err := validateManagedPrefix(prefix); err != nil {
		return err
	}
	status, err := b.Status(prefix.Addr())
	if err != nil || status == AddressAbsent {
		return err
	}
	_, err = b.run(b.removeArguments(prefix)...)
	return err
}

func (b *ifconfigBackend) Status(address netip.Addr) (AddressStatus, error) {
	output, err := b.run(b.interfaceName, "inet6")
	if err != nil {
		return AddressAbsent, err
	}
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		fields := strings.Fields(string(line))
		if len(fields) < 2 || fields[0] != "inet6" {
			continue
		}
		raw := strings.Split(fields[1], "%")[0]
		current, parseErr := netip.ParseAddr(raw)
		if parseErr != nil || current != address {
			continue
		}
		lower := strings.ToLower(string(line))
		switch {
		case strings.Contains(lower, "duplicated") || strings.Contains(lower, "dadfailed"):
			return AddressDADFailed, nil
		case strings.Contains(lower, "tentative"):
			return AddressTentative, nil
		default:
			return AddressReady, nil
		}
	}
	return AddressAbsent, nil
}

func (b *ifconfigBackend) addArguments(prefix netip.Prefix) []string {
	address := prefix.Addr().String()
	switch b.style {
	case IfconfigFreeBSD:
		return []string{b.interfaceName, "inet6", prefix.String(), "alias"}
	case IfconfigOpenBSD:
		return []string{b.interfaceName, "inet6", address, "prefixlen", "64", "alias"}
	default: // darwin
		return []string{b.interfaceName, "inet6", address, "prefixlen", "64", "alias"}
	}
}

func (b *ifconfigBackend) removeArguments(prefix netip.Prefix) []string {
	address := prefix.Addr().String()
	switch b.style {
	case IfconfigFreeBSD:
		return []string{b.interfaceName, "inet6", prefix.String(), "-alias"}
	case IfconfigOpenBSD:
		return []string{b.interfaceName, "inet6", address, "delete"}
	default: // darwin
		return []string{b.interfaceName, "inet6", address, "-alias"}
	}
}

func validateManagedPrefix(prefix netip.Prefix) error {
	if !prefix.IsValid() || !prefix.Addr().Is6() || prefix.Bits() != 64 {
		return errors.New("managed address must be an IPv6 /64")
	}
	return nil
}
