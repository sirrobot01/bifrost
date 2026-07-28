package diagnose

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"time"

	"github.com/sirrobot01/bifrost/internal/serviceaddr"
)

// defaultEgressTarget is an IPv6 anycast address that answers on TCP 443 from
// every network with working IPv6 egress. Preflight only needs to learn whether
// this host can open an IPv6 connection at all, so a stable well-known endpoint
// is enough and avoids depending on name resolution to run the test.
const defaultEgressTarget = "[2606:4700:4700::1111]:443"

// PreflightInput describes the host state that Preflight judges. Every field
// comes from inspecting the machine, not from a Bifrost configuration file:
// preflight has to run before a configuration exists.
type PreflightInput struct {
	// Interface is the publication interface being evaluated.
	Interface string
	// MTU is that interface's MTU.
	MTU int
	// Candidates are the IPv6 addresses observed on the interface.
	Candidates []serviceaddr.Candidate
	// Privileged reports whether the caller can manage addresses and bind
	// privileged ports.
	Privileged bool
	// DockerSocket is checked for reachability when it is set.
	DockerSocket string
	// Probe, when set, is asked to reach a temporary listener on this host,
	// which is the only way to learn whether inbound traffic arrives at all.
	Probe ExternalProber
	// EgressTarget overrides the IPv6 endpoint used for the egress test.
	EgressTarget string
	// SkipNetwork omits every check that leaves the host.
	SkipNetwork bool
}

// Preflight reports whether this host can run Bifrost at all. It answers the
// question an operator has before they own a configuration file, so it takes no
// configuration and never contacts a DNS provider.
func (c *Checker) Preflight(ctx context.Context, input PreflightInput) (Report, error) {
	if input.Interface == "" {
		return Report{}, errors.New("preflight interface is required")
	}
	report := Report{GeneratedAt: c.now()}

	if runtime.GOOS == "linux" {
		report.Findings = append(report.Findings, Finding{Check: "platform", Severity: SeverityInfo, Summary: "host runs Linux"})
	} else {
		report.Findings = append(report.Findings, Finding{
			Check:       "platform",
			Severity:    SeverityError,
			Summary:     "the Bifrost home role requires Linux",
			Detail:      "this host runs " + runtime.GOOS,
			Remediation: "run Bifrost on the Linux host that owns the public IPv6 addresses",
		})
	}

	report.Findings = append(report.Findings, Finding{
		Check:    "interface",
		Severity: SeverityInfo,
		Summary:  "publication interface is " + input.Interface,
	})
	report.Findings = append(report.Findings, mtuFinding(input.MTU))
	report.Findings = append(report.Findings, prefixFinding(input.Candidates, c.now()))
	report.Findings = append(report.Findings, c.inboundFinding(ctx, input))

	if input.Privileged {
		report.Findings = append(report.Findings, Finding{Check: "privileges", Severity: SeverityInfo, Summary: "running with the privilege needed to manage addresses and bind service ports"})
	} else {
		report.Findings = append(report.Findings, Finding{
			Check:       "privileges",
			Severity:    SeverityWarning,
			Summary:     "not running with address-management privilege",
			Detail:      "splice mode needs CAP_NET_ADMIN and ports below 1024 need CAP_NET_BIND_SERVICE",
			Remediation: "re-run this command with sudo to test privileged operations, or rely on the supplied systemd unit which grants both capabilities",
		})
	}

	report.Findings = append(report.Findings, c.egressFinding(ctx, input))
	if finding, ok := c.dockerFinding(ctx, input.DockerSocket); ok {
		report.Findings = append(report.Findings, finding)
	}

	chains, err := c.firewall.Audit(ctx)
	if err != nil {
		report.Findings = append(report.Findings, Finding{Check: "firewall", Severity: SeverityWarning, Summary: "host firewall could not be audited", Detail: err.Error(), Remediation: "inspect the authoritative IPv6 input policy manually"})
	} else {
		report.Findings = append(report.Findings, firewallFindings(chains)...)
	}
	return report, nil
}

// prefixFinding reports whether the interface holds a /64 that Bifrost can
// publish from, and turns a selection failure into the specific reason it
// failed rather than a bare "no prefix" message.
func prefixFinding(candidates []serviceaddr.Candidate, now time.Time) Finding {
	selection, err := serviceaddr.SelectPrefix(candidates, netip.Prefix{}, now)
	if err == nil {
		detail := fmt.Sprintf("selected %s", selection.Prefix)
		if len(selection.Candidates) > 1 {
			detail = fmt.Sprintf("selected %s of %d eligible prefixes %v; set prefix_override to pin a different one",
				selection.Prefix, len(selection.Candidates), selection.Candidates)
		}
		return Finding{Check: "ipv6-prefix", Severity: SeverityInfo, Summary: "interface holds a usable global IPv6 /64", Detail: detail}
	}

	finding := Finding{Check: "ipv6-prefix", Severity: SeverityError, Summary: "no usable global IPv6 /64 on the publication interface", Detail: err.Error()}
	var noPrefix *serviceaddr.NoEligiblePrefixError
	if errors.As(err, &noPrefix) {
		finding.Remediation = noPrefix.Remediation()
	}
	return finding
}

func (c *Checker) egressFinding(ctx context.Context, input PreflightInput) Finding {
	if input.SkipNetwork {
		return Finding{Check: "ipv6-egress", Severity: SeverityWarning, Summary: "IPv6 egress was not tested", Remediation: "re-run without --offline to confirm this host can reach the IPv6 internet"}
	}
	target := input.EgressTarget
	if target == "" {
		target = defaultEgressTarget
	}
	connection, err := c.dial(ctx, "tcp6", target)
	if err != nil {
		return Finding{
			Check:       "ipv6-egress",
			Severity:    SeverityError,
			Summary:     "this host cannot open an outbound IPv6 connection",
			Detail:      err.Error(),
			Remediation: "confirm the host has an IPv6 default route and that the router forwards IPv6; inbound publication cannot work while egress fails",
		}
	}
	_ = connection.Close()
	return Finding{Check: "ipv6-egress", Severity: SeverityInfo, Summary: "outbound IPv6 works", Detail: "reached " + target}
}

// dockerFinding reports on the Docker socket only when one was requested. The
// second result is false when Docker is not part of this host's setup, so the
// report stays silent about a feature the operator did not ask for.
func (c *Checker) dockerFinding(ctx context.Context, socket string) (Finding, bool) {
	if socket == "" {
		return Finding{}, false
	}
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	connection, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return Finding{
			Check:       "docker",
			Severity:    SeverityWarning,
			Summary:     "the Docker socket is not reachable",
			Detail:      err.Error(),
			Remediation: "start Docker, or leave docker.enabled false and declare services in static_services",
		}, true
	}
	_ = connection.Close()
	return Finding{
		Check:       "docker",
		Severity:    SeverityInfo,
		Summary:     "the Docker socket is reachable",
		Detail:      socket,
		Remediation: "socket access grants root-level control of this host; prefer a restricted socket proxy",
	}, true
}

// inboundFinding is the only preflight check that cannot be answered from the
// host. Everything else here observes local state, and local state is
// compatible with a router that silently drops every inbound packet. When a
// probe is available this binds a temporary listener on an observed global
// address and asks the probe to reach it, which exercises the customer-edge
// router before the operator has configured anything worth losing.
func (c *Checker) inboundFinding(ctx context.Context, input PreflightInput) Finding {
	if input.Probe == nil {
		return Finding{
			Check:       "inbound",
			Severity:    SeverityWarning,
			Summary:     "inbound reachability was not tested",
			Detail:      "every other check here observes this host, and a host looks identical whether or not the internet can reach it",
			Remediation: "re-run with --probe to have an outside vantage open a connection to this host",
		}
	}
	address, ok := eligibleGlobalAddress(input.Candidates, c.now())
	if !ok {
		return Finding{Check: "inbound", Severity: SeverityWarning, Summary: "inbound reachability was not tested", Detail: "no eligible global IPv6 address to listen on"}
	}

	// Bind the wildcard rather than the address itself: the address may be
	// managed by something else, and accepting on any address still proves the
	// packet arrived.
	listener, err := net.Listen("tcp6", "[::]:0")
	if err != nil {
		return Finding{Check: "inbound", Severity: SeverityWarning, Summary: "inbound reachability was not tested", Detail: "could not bind a temporary listener: " + err.Error()}
	}
	defer func() { _ = listener.Close() }()
	port := uint16(listener.Addr().(*net.TCPAddr).Port)

	accepted := make(chan struct{}, 1)
	go func() {
		if connection, err := listener.Accept(); err == nil {
			_ = connection.Close()
			accepted <- struct{}{}
		}
	}()

	probeContext, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	result, err := input.Probe.Probe(probeContext, ProbeRequest{Address: address, Port: port})
	if err != nil {
		return Finding{Check: "inbound", Severity: SeverityWarning, Summary: "the inbound probe could not be run", Detail: err.Error()}
	}

	select {
	case <-accepted:
		return Finding{Check: "inbound", Severity: SeverityInfo, Summary: "inbound IPv6 from the internet reaches this host", Detail: fmt.Sprintf("an outside vantage connected to [%s]:%d", address, port)}
	case <-time.After(2 * time.Second):
	}
	if result.Reachable {
		// The probe believes it connected but nothing arrived, which means
		// something answered on its behalf rather than this host.
		return Finding{Check: "inbound", Severity: SeverityWarning, Summary: "the inbound probe reported success but no connection arrived", Detail: "something between the probe and this host answered instead"}
	}
	return Finding{
		Check:       "inbound",
		Severity:    SeverityError,
		Summary:     "inbound IPv6 from the internet does not reach this host",
		Detail:      fmt.Sprintf("an outside vantage could not open a connection to [%s]:%d", address, port),
		Remediation: "permit inbound IPv6 on the customer-edge router; nothing on this host can open that path",
	}
}

// eligibleGlobalAddress picks an address a probe could plausibly reach:
// global, stable, and not on its way out. Temporary and deprecated addresses
// are excluded for the same reason they are never published.
func eligibleGlobalAddress(candidates []serviceaddr.Candidate, now time.Time) (netip.Addr, bool) {
	for _, candidate := range candidates {
		address := candidate.Prefix.Addr()
		if !address.Is6() || !address.IsGlobalUnicast() || address.IsPrivate() {
			continue
		}
		if candidate.Temporary || candidate.Deprecated {
			continue
		}
		if !candidate.ValidUntil.IsZero() && !now.Before(candidate.ValidUntil) {
			continue
		}
		return address, true
	}
	return netip.Addr{}, false
}
