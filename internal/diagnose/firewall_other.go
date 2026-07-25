//go:build !linux

package diagnose

import "errors"

func DefaultFirewallAuditor() FirewallAuditor {
	return UnavailableFirewallAuditor(errors.New("nftables auditing requires Linux"))
}
