package config

import (
	"fmt"
	"slices"
	"strings"
)

// Reloadable reports whether next can replace c in a running daemon.
//
// Adding a service should not cost an outage. A restart withdraws every DNS
// record and drops every connection first, so operators batch changes or avoid
// them, and neither is a good reason to keep publishing a stale configuration.
//
// The split is drawn where re-applying a value is genuinely safe in place.
// Anything that was consumed while building the runtime -- the interface the
// observer watches, the secret addresses derive from, the DNS credentials, the
// listeners already bound -- needs a restart, and saying so plainly is better
// than a partial apply that leaves the process disagreeing with its own file.
func (c Config) Reloadable(next Config) error {
	var fixed []string
	require := func(name string, unchanged bool) {
		if !unchanged {
			fixed = append(fixed, name)
		}
	}

	require("version", c.Version == next.Version)
	require("interface", c.Interface == next.Interface)
	require("prefix_override", c.PrefixOverride == next.PrefixOverride)
	require("owner_id", c.OwnerID == next.OwnerID)
	require("secret_file", c.SecretFile == next.SecretFile)

	// TTL is applied per publication, so it reloads. Everything else about the
	// provider was used to build the client.
	require("dns.provider", c.DNS.Provider == next.DNS.Provider)
	require("dns credentials", c.DNS.Cloudflare == next.DNS.Cloudflare &&
		c.DNS.DESEC == next.DNS.DESEC &&
		c.DNS.Dynv6 == next.DNS.Dynv6 &&
		c.DNS.RFC2136 == next.DNS.RFC2136)

	// The table's contents follow the published services and reload with them.
	// Owning the table at all, and asking the router for pinholes, are decided
	// once at start.
	require("firewall.mode", c.Firewall.Mode == next.Firewall.Mode)
	require("firewall.pcp", c.Firewall.PCP == next.Firewall.PCP)

	require("metrics.listen", c.Metrics.Listen == next.Metrics.Listen)
	require("docker", c.Docker == next.Docker)
	require("acme", c.ACME == next.ACME)
	require("probe.endpoint", c.Probe.Endpoint == next.Probe.Endpoint)
	require("verify.enabled", c.VerificationEnabled() == next.VerificationEnabled())
	require("notify", c.Notify == next.Notify)
	require("edge", c.Edge.Enabled == next.Edge.Enabled &&
		c.Edge.KeyFile == next.Edge.KeyFile &&
		c.Edge.MaxClockSkew == next.Edge.MaxClockSkew &&
		c.Edge.HeaderTimeout == next.Edge.HeaderTimeout &&
		slices.Equal(c.Edge.IPv4Addresses, next.Edge.IPv4Addresses))

	if len(fixed) == 0 {
		return nil
	}
	return fmt.Errorf("these settings only change on restart: %s", strings.Join(fixed, ", "))
}
