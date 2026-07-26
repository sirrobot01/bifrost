package serviceaddr

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"
)

// Candidate describes an address observed on the publication interface.
type Candidate struct {
	Prefix         netip.Prefix
	Temporary      bool
	Deprecated     bool
	PreferredUntil time.Time
	ValidUntil     time.Time
}

// Selection contains the chosen on-link prefix and all eligible prefixes.
type Selection struct {
	Prefix     netip.Prefix
	Candidates []netip.Prefix
}

// RejectionReason explains why one observed address cannot carry service
// addresses.
type RejectionReason string

const (
	RejectionNotSlash64       RejectionReason = "not an IPv6 /64"
	RejectionNotGlobalUnicast RejectionReason = "not a public address"
	RejectionTemporary        RejectionReason = "a temporary privacy address"
	RejectionDeprecated       RejectionReason = "deprecated by the router"
	RejectionExpired          RejectionReason = "past its advertised lifetime"
)

// rejectionOrder fixes the order reasons appear in a message so that the same
// interface state always renders the same text.
var rejectionOrder = []RejectionReason{
	RejectionNotSlash64,
	RejectionNotGlobalUnicast,
	RejectionTemporary,
	RejectionDeprecated,
	RejectionExpired,
}

// NoEligiblePrefixError reports that the publication interface carries no /64
// that Bifrost can publish from, and counts why each observed address was
// rejected.
//
// The counts carry the diagnosis. "Three temporary addresses", "one deprecated
// address", and "no IPv6 at all" each need a different fix, and an operator who
// only learns that selection failed cannot tell which one they are looking at.
type NoEligiblePrefixError struct {
	Examined int
	Rejected map[RejectionReason]int
}

func (e *NoEligiblePrefixError) Error() string {
	if e.Examined == 0 {
		return "no IPv6 addresses are present on the publication interface"
	}
	reasons := make([]string, 0, len(rejectionOrder))
	for _, reason := range rejectionOrder {
		if count := e.Rejected[reason]; count > 0 {
			reasons = append(reasons, fmt.Sprintf("%d %s", count, reason))
		}
	}
	return fmt.Sprintf("no eligible IPv6 /64 among %s on the publication interface: %s",
		countAddresses(e.Examined), strings.Join(reasons, ", "))
}

// Remediation returns the operator action that fits the observed rejections.
func (e *NoEligiblePrefixError) Remediation() string {
	switch {
	case e.Examined == 0 || e.Rejected[RejectionNotGlobalUnicast] == e.Examined:
		return "confirm the ISP delegates a routed IPv6 prefix and that the publication interface receives a global address"
	case e.Rejected[RejectionTemporary] > 0:
		return "disable IPv6 privacy extensions on the publication interface so it keeps a stable global address, for example sysctl -w net.ipv6.conf.<interface>.use_tempaddr=0"
	case e.Rejected[RejectionDeprecated] > 0 || e.Rejected[RejectionExpired] > 0:
		return "the advertised lifetimes have lapsed; confirm the router still advertises the delegated prefix on this link"
	default:
		return "confirm the publication interface holds a stable global IPv6 /64"
	}
}

func countAddresses(count int) string {
	if count == 1 {
		return "1 address"
	}
	return fmt.Sprintf("%d addresses", count)
}

// SelectPrefix deterministically chooses an eligible on-link /64.
func SelectPrefix(candidates []Candidate, override netip.Prefix, now time.Time) (Selection, error) {
	eligible := make(map[netip.Prefix]struct{}, len(candidates))
	rejected := make(map[RejectionReason]int, len(rejectionOrder))
	for _, candidate := range candidates {
		prefix := candidate.Prefix.Masked()
		if !prefix.IsValid() || !prefix.Addr().Is6() || prefix.Bits() != 64 {
			rejected[RejectionNotSlash64]++
			continue
		}
		if !prefix.Addr().IsGlobalUnicast() || prefix.Addr().IsPrivate() {
			rejected[RejectionNotGlobalUnicast]++
			continue
		}
		if candidate.Temporary {
			rejected[RejectionTemporary]++
			continue
		}
		if candidate.Deprecated {
			rejected[RejectionDeprecated]++
			continue
		}
		if !candidate.PreferredUntil.IsZero() && !now.Before(candidate.PreferredUntil) {
			rejected[RejectionExpired]++
			continue
		}
		if !candidate.ValidUntil.IsZero() && !now.Before(candidate.ValidUntil) {
			rejected[RejectionExpired]++
			continue
		}

		eligible[prefix] = struct{}{}
	}

	prefixes := make([]netip.Prefix, 0, len(eligible))
	for prefix := range eligible {
		prefixes = append(prefixes, prefix)
	}
	slices.SortFunc(prefixes, func(a, b netip.Prefix) int {
		return a.Addr().Compare(b.Addr())
	})

	if len(prefixes) == 0 {
		return Selection{}, &NoEligiblePrefixError{Examined: len(candidates), Rejected: rejected}
	}

	selected := prefixes[0]
	if override.IsValid() {
		override = override.Masked()
		if !override.Addr().Is6() || override.Bits() != 64 {
			return Selection{}, errors.New("prefix_override must be an IPv6 /64")
		}
		if _, ok := eligible[override]; !ok {
			return Selection{}, fmt.Errorf("prefix_override %s is not one of the eligible prefixes on this interface (%v)", override, prefixes)
		}
		selected = override
	}

	return Selection{Prefix: selected, Candidates: prefixes}, nil
}
