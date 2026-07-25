package serviceaddr

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
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

// SelectPrefix deterministically chooses an eligible on-link /64.
func SelectPrefix(candidates []Candidate, override netip.Prefix, now time.Time) (Selection, error) {
	eligible := make(map[netip.Prefix]struct{}, len(candidates))
	for _, candidate := range candidates {
		prefix := candidate.Prefix.Masked()
		if !prefix.IsValid() || !prefix.Addr().Is6() || prefix.Bits() != 64 {
			continue
		}
		if !prefix.Addr().IsGlobalUnicast() || prefix.Addr().IsPrivate() {
			continue
		}
		if candidate.Temporary || candidate.Deprecated {
			continue
		}
		if !candidate.PreferredUntil.IsZero() && !now.Before(candidate.PreferredUntil) {
			continue
		}
		if !candidate.ValidUntil.IsZero() && !now.Before(candidate.ValidUntil) {
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
		return Selection{}, errors.New("no eligible IPv6 /64 prefix found")
	}

	selected := prefixes[0]
	if override.IsValid() {
		override = override.Masked()
		if !override.Addr().Is6() || override.Bits() != 64 {
			return Selection{}, errors.New("prefix override must be an IPv6 /64")
		}
		if _, ok := eligible[override]; !ok {
			return Selection{}, fmt.Errorf("prefix override %s is not eligible", override)
		}
		selected = override
	}

	return Selection{Prefix: selected, Candidates: prefixes}, nil
}
