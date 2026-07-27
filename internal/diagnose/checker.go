package diagnose

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"time"
)

type Service struct {
	Name              string
	DNSName           string
	Address           netip.Addr
	Port              uint16
	CheckLocal        bool
	ClientIPPreserved bool
	// TLS marks services whose listener terminates TLS, so check can judge
	// the served certificate.
	TLS bool
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
	// authoritativeAAAA queries the zone's own nameservers, bypassing the
	// local resolver's cache. Tests replace it.
	authoritativeAAAA func(context.Context, string) ([]netip.Addr, string, error)
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
	return &Checker{resolver: resolver, firewall: firewall, dial: dialer.DialContext, now: time.Now, authoritativeAAAA: lookupAuthoritativeAAAA}
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

// dnsFinding judges the resolver view of the service name and, when that view
// is wrong, asks the zone's authoritative nameservers which side is lying.
// The management API can report success while the nameservers serve nothing
// (unactivated account, missing delegation), and a resolver can serve a stale
// negative answer long after publication; only this comparison tells them apart.
func (c *Checker) dnsFinding(ctx context.Context, service Service) Finding {
	resolved, err := c.resolver.LookupNetIP(ctx, "ip6", service.DNSName)
	if err == nil && slices.Contains(resolved, service.Address) {
		return Finding{Check: "dns", Severity: SeverityInfo, Service: service.Name, Summary: "DNS contains the expected IPv6 address"}
	}
	local := fmt.Sprintf("local resolver answered %v", resolved)
	if err != nil {
		local = "local resolver failed: " + err.Error()
	}
	authoritative, source, authErr := c.authoritativeAAAA(ctx, service.DNSName)
	switch {
	case authErr == nil && slices.Contains(authoritative, service.Address):
		return Finding{
			Check:       "dns",
			Severity:    SeverityWarning,
			Service:     service.Name,
			Summary:     "the authoritative nameserver has the address, the local resolver does not",
			Detail:      source + " answers with the expected address; " + local,
			Remediation: "wait for the local resolver's cached answer to expire, or flush its cache",
		}
	case authErr == nil:
		return Finding{
			Check:       "dns",
			Severity:    SeverityError,
			Service:     service.Name,
			Summary:     "the authoritative nameserver does not serve the expected address",
			Detail:      fmt.Sprintf("%s answered %v, expected %s", source, authoritative, service.Address),
			Remediation: "if dns-owner reports correct provider state, confirm the DNS account is activated and the zone is delegated",
		}
	default:
		return Finding{
			Check:       "dns",
			Severity:    SeverityError,
			Service:     service.Name,
			Summary:     "DNS lookup failed",
			Detail:      local + "; authoritative check failed: " + authErr.Error(),
			Remediation: "confirm the DNS name and provider state, then re-run check",
		}
	}
}

// tlsFinding performs a TLS handshake against the service listener and
// judges the served certificate: it must verify for the DNS name and not be
// close to expiry, since renewal runs 30 days out and anything nearer means
// renewals have been failing.
func (c *Checker) tlsFinding(ctx context.Context, service Service) Finding {
	connection, err := c.dial(ctx, "tcp6", netip.AddrPortFrom(service.Address, service.Port).String())
	if err != nil {
		return Finding{Check: "tls", Severity: SeverityError, Service: service.Name, Summary: "TLS listener is unreachable", Detail: err.Error()}
	}
	defer func() { _ = connection.Close() }()
	client := tls.Client(connection, &tls.Config{ServerName: service.DNSName})
	if err := client.HandshakeContext(ctx); err != nil {
		return Finding{
			Check:       "tls",
			Severity:    SeverityError,
			Service:     service.Name,
			Summary:     "TLS handshake failed",
			Detail:      err.Error(),
			Remediation: "confirm certificate issuance succeeded in the serve log, then re-run check",
		}
	}
	leaf := client.ConnectionState().PeerCertificates[0]
	remaining := leaf.NotAfter.Sub(c.now())
	days := int(remaining.Hours() / 24)
	if remaining <= 0 {
		return Finding{Check: "tls", Severity: SeverityError, Service: service.Name, Summary: "certificate has expired", Detail: "expired " + leaf.NotAfter.Format(time.RFC3339), Remediation: "check certificate renewal errors in the serve log"}
	}
	if remaining < 14*24*time.Hour {
		return Finding{Check: "tls", Severity: SeverityWarning, Service: service.Name, Summary: fmt.Sprintf("certificate expires in %d days", days), Remediation: "renewal runs 30 days before expiry; check renewal errors in the serve log"}
	}
	return Finding{Check: "tls", Severity: SeverityInfo, Service: service.Name, Summary: "certificate is valid", Detail: fmt.Sprintf("expires in %d days", days)}
}

func (c *Checker) serviceFindings(ctx context.Context, service Service, local map[netip.Addr]struct{}, interfaceMTU int, prober ExternalProber) []Finding {
	findings := make([]Finding, 0, 5)
	if service.ClientIPPreserved {
		findings = append(findings, Finding{Check: "client-ip", Severity: SeverityInfo, Service: service.Name, Summary: "backend client identity is preserved"})
	} else {
		findings = append(findings, Finding{Check: "client-ip", Severity: SeverityWarning, Service: service.Name, Summary: "splice mode hides the client address from the backend", Remediation: "prefer direct mode or enable PROXY v2 only when the backend explicitly supports it"})
	}
	if service.CheckLocal {
		if _, exists := local[service.Address]; exists {
			findings = append(findings, Finding{Check: "address", Severity: SeverityInfo, Service: service.Name, Summary: "service address is present on the host"})
			connection, err := c.dial(ctx, "tcp6", netip.AddrPortFrom(service.Address, service.Port).String())
			if err != nil {
				findings = append(findings, Finding{Check: "listener", Severity: SeverityError, Service: service.Name, Summary: "local TCP listener is unavailable", Detail: err.Error()})
			} else {
				_ = connection.Close()
				findings = append(findings, Finding{Check: "listener", Severity: SeverityInfo, Service: service.Name, Summary: "local TCP listener accepted a connection"})
			}
		} else {
			findings = append(findings, Finding{Check: "address", Severity: SeverityError, Service: service.Name, Summary: "service address is missing from the host"})
		}
	}

	if service.CheckLocal && service.TLS {
		findings = append(findings, c.tlsFinding(ctx, service))
	}

	findings = append(findings, c.dnsFinding(ctx, service))

	if prober == nil {
		findings = append(findings, Finding{Check: "external", Severity: SeverityWarning, Service: service.Name, Summary: "external reachability and PMTU were not tested", Remediation: "configure an explicit external probe endpoint to distinguish router filtering from host state"})
		return findings
	}
	result, err := prober.Probe(ctx, ProbeRequest{Address: service.Address, Port: service.Port})
	if err != nil {
		findings = append(findings, Finding{Check: "external", Severity: SeverityWarning, Service: service.Name, Summary: "external probe failed", Detail: err.Error()})
		return findings
	}
	if !result.Reachable {
		findings = append(findings, Finding{Check: "external", Severity: SeverityError, Service: service.Name, Summary: "service is not reachable from the external probe", Remediation: "if local address, listener, and host policy are correct, inspect the customer-edge router IPv6 firewall"})
		return findings
	}
	findings = append(findings, Finding{Check: "external", Severity: SeverityInfo, Service: service.Name, Summary: "service is externally reachable"})
	if !result.PacketTooBigWorks {
		findings = append(findings, Finding{Check: "pmtu", Severity: SeverityError, Service: service.Name, Summary: "the probe detected a likely PMTU blackhole", Detail: fmt.Sprintf("interface MTU %d, observed path MTU %d", interfaceMTU, result.PathMTU), Remediation: "allow ICMPv6 Packet Too Big end-to-end and verify reduced-MTU links such as PPPoE"})
	} else {
		findings = append(findings, Finding{Check: "pmtu", Severity: SeverityInfo, Service: service.Name, Summary: "Packet Too Big delivery succeeded", Detail: fmt.Sprintf("interface MTU %d, observed path MTU %d", interfaceMTU, result.PathMTU)})
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
