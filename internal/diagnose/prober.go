package diagnose

import (
	"fmt"
	"net/netip"
)

// SelectProber decides how reachability is confirmed from outside the host.
//
// A configured endpoint wins. Otherwise a configured edge is used, because an
// edge already sits outside the network and reaching a service through it
// exercises the same inbound path a real client takes, including the customer
// edge router. Only a deployment with neither goes unverified, and the nil
// return says so rather than substituting a local check that cannot answer the
// question.
//
// It takes plain values rather than the configuration type so that both the CLI
// and the daemon select a prober the same way.
func SelectProber(endpoint string, edgeAddresses []netip.Addr) (ExternalProber, error) {
	if endpoint != "" {
		return NewHTTPProber(endpoint, nil)
	}
	// One edge is enough: reaching a service through any of them proves the
	// inbound path, and probing all of them would multiply the cost of a check
	// that already runs on a timer.
	if len(edgeAddresses) > 0 {
		address := edgeAddresses[0]
		if !address.IsValid() {
			return nil, fmt.Errorf("edge address %q is not usable", address)
		}
		return NewEdgeProber(address), nil
	}
	return nil, nil
}
