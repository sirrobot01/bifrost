package netwatch

import (
	"net/netip"
	"time"

	"github.com/sirrobot01/bifrost/internal/serviceaddr"
)

const infiniteLifetime = ^uint32(0)

type addressState struct {
	prefix            netip.Prefix
	temporary         bool
	deprecated        bool
	tentative         bool
	dadFailed         bool
	preferredLifetime uint32
	validLifetime     uint32
}

func (a addressState) candidate(observedAt time.Time) (serviceaddr.Candidate, bool) {
	if a.tentative || a.dadFailed {
		return serviceaddr.Candidate{}, false
	}

	candidate := serviceaddr.Candidate{
		Prefix:     a.prefix,
		Temporary:  a.temporary,
		Deprecated: a.deprecated || a.preferredLifetime == 0,
	}
	if a.preferredLifetime != infiniteLifetime {
		candidate.PreferredUntil = observedAt.Add(time.Duration(a.preferredLifetime) * time.Second)
	}
	if a.validLifetime != infiniteLifetime {
		candidate.ValidUntil = observedAt.Add(time.Duration(a.validLifetime) * time.Second)
	}

	return candidate, true
}

func lifetimeSeconds(seconds int) uint32 {
	if seconds < 0 || uint64(seconds) >= uint64(infiniteLifetime) {
		return infiniteLifetime
	}
	return uint32(seconds)
}
