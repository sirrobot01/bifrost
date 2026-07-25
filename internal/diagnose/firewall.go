package diagnose

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

var essentialICMPv6Types = []uint8{1, 2, 3, 4}

type FirewallChain struct {
	Family            string
	Table             string
	Name              string
	Priority          int
	DropPolicy        bool
	AcceptedICMPTypes []uint8
	AcceptsAllICMPv6  bool
	AnalysisComplete  bool
}

type FirewallAuditor interface {
	Audit(context.Context) ([]FirewallChain, error)
}

func firewallFindings(chains []FirewallChain) []Finding {
	dropChains := make([]FirewallChain, 0, len(chains))
	for _, chain := range chains {
		if chain.DropPolicy {
			dropChains = append(dropChains, chain)
		}
	}
	if len(dropChains) == 0 {
		return []Finding{{Check: "firewall", Severity: SeverityInfo, Summary: "no nftables IPv6 input base chain with a drop policy was found"}}
	}

	findings := []Finding{{
		Check:       "firewall",
		Severity:    SeverityWarning,
		Summary:     "an authoritative nftables input chain can drop inbound IPv6",
		Detail:      formatDropChains(dropChains),
		Remediation: "add service-scoped accepts to the authoritative firewall manager; a separate Bifrost table cannot override this drop",
	}}

	for _, chain := range dropChains {
		if chain.AcceptsAllICMPv6 {
			continue
		}
		missing := missingICMPTypes(chain.AcceptedICMPTypes)
		if len(missing) == 0 && chain.AnalysisComplete {
			continue
		}
		detail := fmt.Sprintf("chain %s/%s/%s does not have recognizable accepts for ICMPv6 types %v", chain.Family, chain.Table, chain.Name, missing)
		if !chain.AnalysisComplete {
			detail = fmt.Sprintf("chain %s/%s/%s contains rules Bifrost cannot evaluate completely", chain.Family, chain.Table, chain.Name)
		}
		findings = append(findings, Finding{
			Check:       "icmpv6",
			Severity:    SeverityWarning,
			Summary:     "essential ICMPv6 error traffic may be blocked",
			Detail:      detail,
			Remediation: "verify scoped accepts for Destination Unreachable (1), Packet Too Big (2), Time Exceeded (3), and Parameter Problem (4)",
		})
	}
	if len(findings) == 1 {
		findings = append(findings, Finding{Check: "icmpv6", Severity: SeverityInfo, Summary: "essential ICMPv6 error types have recognizable accept rules"})
	}
	return findings
}

func missingICMPTypes(accepted []uint8) []uint8 {
	missing := make([]uint8, 0, len(essentialICMPv6Types))
	for _, required := range essentialICMPv6Types {
		if !slices.Contains(accepted, required) {
			missing = append(missing, required)
		}
	}
	return missing
}

func formatDropChains(chains []FirewallChain) string {
	detail := ""
	for index, chain := range chains {
		if index > 0 {
			detail += ", "
		}
		detail += fmt.Sprintf("%s/%s/%s priority %d", chain.Family, chain.Table, chain.Name, chain.Priority)
	}
	return detail
}

type unavailableFirewallAuditor struct {
	err error
}

func (a unavailableFirewallAuditor) Audit(context.Context) ([]FirewallChain, error) {
	return nil, a.err
}

func UnavailableFirewallAuditor(err error) FirewallAuditor {
	if err == nil {
		err = errors.New("firewall auditing is unavailable")
	}
	return unavailableFirewallAuditor{err: err}
}
