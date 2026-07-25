# Security model

Bifrost publishes applications to the internet. It is not an authentication layer, WAF, VPN, or certificate manager. Every exposed backend must be safe to expose and must maintain its own authentication, patching, and rate controls.

## Trust boundaries

- The DNS API credential can change records in its scope. Limit it to the intended zone and protect its file with mode `0600`.
- The address secret determines stable splice addresses. Rotation changes those addresses but does not grant DNS access.
- The edge shared key authorizes source metadata accepted by edge-enabled home listeners. Use a different secret from DNS and address derivation.
- Docker socket access is root-equivalent. A read-only filesystem mount does not restrict Docker API methods.
- `CAP_NET_ADMIN` permits network changes in the current namespace. Prefer the hardened systemd unit or a tightly constrained container.
- The external probe receives each public service address and port only when explicitly configured.

## DNS ownership

Bifrost stores `bifrost-owner=<owner_id>` in `_bifrost.<service-name>` TXT records. It refuses to create, replace, or delete address records that exist without its matching marker. Removal deletes A/AAAA records before the marker. Do not share an owner ID between independently administered installations.

`bifrost check` reads provider state and fails if the ownership marker, address set, TTL, or DNS-only state differs from the configuration. It does not mutate provider state.

## Data-plane behavior

Direct mode adds no process between client and backend and preserves the peer address. Splice mode creates a new backend connection, so the backend sees Bifrost unless it explicitly consumes PROXY protocol v2. The optional edge uses authenticated metadata, but the backend still sees home Bifrost unless final PROXY emission is enabled.

The edge allowlist is an SSRF boundary. Resolved destinations are restricted to acceptable global IPv6, positive/negative cache duration is bounded, ClientHello input is capped, and connection/rate state has hard limits. Static maps are explicit configuration, not client-selected destinations.

## Telemetry

Bifrost has no analytics, crash reporting, update checks, or default external service. DNS-provider calls, Docker calls, and an explicitly configured external probe are functional traffic. Structured logs stay local unless the operator forwards them.

## Recommended deployment

- Keep management interfaces and metrics on loopback or a private administration network.
- Expose only the exact service addresses and ports at host and router firewalls.
- Preserve essential ICMPv6 error traffic, especially Packet Too Big.
- Run separate home and edge service users.
- Use automatic security updates for the host and applications.
- Review `serve --dry-run` before first reconciliation and after configuration changes.
- Monitor `/healthz`, reconcile errors, DNS drift, and rejected connections.
