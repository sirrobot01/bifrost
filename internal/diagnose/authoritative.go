package diagnose

import (
	"context"
	"net/netip"

	"github.com/sirrobot01/bifrost/internal/dnsprobe"
)

// lookupAuthoritativeAAAA asks the zone's own nameservers for name, bypassing
// every recursive cache, so a finding can distinguish "not published" from
// "the local resolver has not caught up".
func lookupAuthoritativeAAAA(ctx context.Context, name string) ([]netip.Addr, string, error) {
	return dnsprobe.LookupAAAA(ctx, name)
}
