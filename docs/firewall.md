# Firewall, ICMPv6, and PMTU

Bifrost does not own the host's global firewall policy. Its advisory audit can identify common drops, but an accept rule in a separate nftables base chain cannot reliably override a drop in another authoritative chain. Add service pinholes to the firewall manager that already owns input policy.

At minimum, permit the configured TCP port to the exact service address. A conceptual nftables rule is:

```nft
ip6 daddr 2001:db8:1234:1::10 tcp dport 443 ct state new accept
```

Replace the documentation address. Scope the rule to the service address, port, and appropriate interface or zone. Do not expose the metrics listener; v1 requires it to bind loopback.

## Essential ICMPv6

IPv6 routers do not fragment transit packets. Packet Too Big messages are required for Path MTU Discovery, and filtering them creates deceptive partial failures: small requests work while larger TLS responses or media streams stall.

The effective policy must allow these ICMPv6 errors back to established traffic:

- Destination Unreachable (type 1)
- Packet Too Big (type 2)
- Time Exceeded (type 3)
- Parameter Problem (type 4)

Echo Request and Echo Reply (128/129) are useful for diagnostics but are not substitutes for types 1–4.

Neighbor Discovery and Router Discovery types 133–137 are link-local control traffic. Accept them only with correct interface, address scope, and hop-limit constraints. Do not copy a blanket internet-facing accept rule for all five types. The exact rule belongs to NetworkManager, systemd-networkd, firewalld, ufw, or the authoritative nftables ruleset on the host.

## Router boundary

Opening the host does not open the customer-edge router. Many residential routers have a separate inbound IPv6 firewall even though there is no NAT. Create a pinhole for the destination service address and port, or a suitably narrow delegated-prefix rule when the router cannot track individual rotating addresses.

If local address/listener checks pass but the optional external probe fails, the router is the likely boundary. Bifrost cannot fix a router it does not control.

## Diagnosing PMTU

`bifrost check` always reports the interface MTU and audits visible host ICMPv6 policy. When `probe.endpoint` is configured, it also asks that explicit external service to connect and test Packet Too Big delivery. Reduced MTUs such as 1492-byte PPPoE are valid when PMTU works.

No public Bifrost-operated probe is configured by default. The probe protocol and privacy boundary are documented in [external-probe.md](external-probe.md).
