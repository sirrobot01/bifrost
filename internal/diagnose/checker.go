package diagnose

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"time"
)

type Service struct {
	Name       string
	DNSName    string
	Address    netip.Addr
	Port       uint16
	CheckLocal bool
}

type Input struct {
	Interface string
	Services  []Service
	Probe     ExternalProber
}

type Checker struct {
	resolver netResolver
	firewall FirewallAuditor
	dial     func(context.Context, string, string) (net.Conn, error)
	now      func() time.Time
}

type netResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

func NewChecker(resolver netResolver, firewall FirewallAuditor) *Checker {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if firewall == nil {
		firewall = DefaultFirewallAuditor()
	}
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	return &Checker{resolver: resolver, firewall: firewall, dial: dialer.DialContext, now: time.Now}
}

func (c *Checker) Check(ctx context.Context, input Input) (Report, error) {
	if input.Interface == "" {
		return Report{}, errors.New("diagnostic interface is required")
	}
	if len(input.Services) == 0 {
		return Report{}, errors.New("at least one service is required")
	}
	for _, service := range input.Services {
		if err := validateService(service); err != nil {
			return Report{}, fmt.Errorf("service %q: %w", service.Name, err)
		}
	}

	report := Report{GeneratedAt: c.now()}
	networkInterface, err := net.InterfaceByName(input.Interface)
	if err != nil {
		report.Findings = append(report.Findings, Finding{Check: "interface", Severity: SeverityError, Summary: "network interface is unavailable", Detail: err.Error()})
		return report, nil
	}
	report.Findings = append(report.Findings, mtuFinding(networkInterface.MTU))
	addresses, err := networkInterface.Addrs()
	if err != nil {
		return Report{}, fmt.Errorf("list addresses on %s: %w", input.Interface, err)
	}
	localAddresses := addressSet(addresses)

	for _, service := range input.Services {
		report.Findings = append(report.Findings, c.serviceFindings(ctx, service, localAddresses, networkInterface.MTU, input.Probe)...)
	}
	chains, err := c.firewall.Audit(ctx)
	if err != nil {
		report.Findings = append(report.Findings, Finding{Check: "firewall", Severity: SeverityWarning, Summary: "host firewall could not be audited", Detail: err.Error(), Remediation: "inspect the authoritative IPv6 input policy manually"})
	} else {
		report.Findings = append(report.Findings, firewallFindings(chains)...)
	}
	return report, nil
}

func (c *Checker) serviceFindings(ctx context.Context, service Service, local map[netip.Addr]struct{}, interfaceMTU int, prober ExternalProber) []Finding {
	findings := make([]Finding, 0, 4)
	if service.CheckLocal {
		if _, exists := local[service.Address]; exists {
			findings = append(findings, Finding{Check: "address", Severity: SeverityInfo, Summary: service.Name + ": service address is present on the host"})
			connection, err := c.dial(ctx, "tcp6", netip.AddrPortFrom(service.Address, service.Port).String())
			if err != nil {
				findings = append(findings, Finding{Check: "listener", Severity: SeverityError, Summary: service.Name + ": local TCP listener is unavailable", Detail: err.Error()})
			} else {
				_ = connection.Close()
				findings = append(findings, Finding{Check: "listener", Severity: SeverityInfo, Summary: service.Name + ": local TCP listener accepted a connection"})
			}
		} else {
			findings = append(findings, Finding{Check: "address", Severity: SeverityError, Summary: service.Name + ": service address is missing from the host"})
		}
	}

	resolved, err := c.resolver.LookupNetIP(ctx, "ip6", service.DNSName)
	if err != nil {
		findings = append(findings, Finding{Check: "dns", Severity: SeverityError, Summary: service.Name + ": DNS lookup failed", Detail: err.Error()})
	} else if !slices.Contains(resolved, service.Address) {
		findings = append(findings, Finding{Check: "dns", Severity: SeverityError, Summary: service.Name + ": DNS does not contain the expected IPv6 address", Detail: fmt.Sprintf("expected %s, resolved %v", service.Address, resolved)})
	} else {
		findings = append(findings, Finding{Check: "dns", Severity: SeverityInfo, Summary: service.Name + ": DNS contains the expected IPv6 address"})
	}

	if prober == nil {
		findings = append(findings, Finding{Check: "external", Severity: SeverityWarning, Summary: service.Name + ": external reachability and PMTU were not tested", Remediation: "configure an explicit external probe endpoint to distinguish router filtering from host state"})
		return findings
	}
	result, err := prober.Probe(ctx, ProbeRequest{Address: service.Address, Port: service.Port})
	if err != nil {
		findings = append(findings, Finding{Check: "external", Severity: SeverityWarning, Summary: service.Name + ": external probe failed", Detail: err.Error()})
		return findings
	}
	if !result.Reachable {
		findings = append(findings, Finding{Check: "external", Severity: SeverityError, Summary: service.Name + ": service is not reachable from the external probe", Remediation: "if local address, listener, and host policy are correct, inspect the customer-edge router IPv6 firewall"})
		return findings
	}
	findings = append(findings, Finding{Check: "external", Severity: SeverityInfo, Summary: service.Name + ": service is externally reachable"})
	if !result.PacketTooBigWorks {
		findings = append(findings, Finding{Check: "pmtu", Severity: SeverityError, Summary: service.Name + ": the probe detected a likely PMTU blackhole", Detail: fmt.Sprintf("interface MTU %d, observed path MTU %d", interfaceMTU, result.PathMTU), Remediation: "allow ICMPv6 Packet Too Big end-to-end and verify reduced-MTU links such as PPPoE"})
	} else {
		findings = append(findings, Finding{Check: "pmtu", Severity: SeverityInfo, Summary: service.Name + ": Packet Too Big delivery succeeded", Detail: fmt.Sprintf("interface MTU %d, observed path MTU %d", interfaceMTU, result.PathMTU)})
	}
	return findings
}

func validateService(service Service) error {
	if service.Name == "" || service.DNSName == "" {
		return errors.New("name and DNS name are required")
	}
	if !service.Address.IsValid() || !service.Address.Is6() || service.Address.Is4In6() || !service.Address.IsGlobalUnicast() || service.Address.IsPrivate() {
		return errors.New("address must be public IPv6")
	}
	if service.Port == 0 {
		return errors.New("port is required")
	}
	return nil
}

func mtuFinding(mtu int) Finding {
	switch {
	case mtu < 1280:
		return Finding{Check: "mtu", Severity: SeverityError, Summary: "interface MTU is below the IPv6 minimum", Detail: fmt.Sprintf("MTU %d", mtu)}
	case mtu < 1500:
		return Finding{Check: "mtu", Severity: SeverityWarning, Summary: "interface uses a reduced MTU", Detail: fmt.Sprintf("MTU %d; this can be valid for PPPoE but requires working PMTU discovery", mtu)}
	default:
		return Finding{Check: "mtu", Severity: SeverityInfo, Summary: "interface MTU is suitable for IPv6", Detail: fmt.Sprintf("MTU %d", mtu)}
	}
}

func addressSet(addresses []net.Addr) map[netip.Addr]struct{} {
	result := make(map[netip.Addr]struct{}, len(addresses))
	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address.String())
		if err == nil {
			result[prefix.Addr()] = struct{}{}
		}
	}
	return result
}
