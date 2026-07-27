---
title: Firewall and path MTU
description: Open narrow IPv6 pinholes and keep essential ICMPv6 traffic working.
---

Bifrost can manage the host firewall for you, or stay out of the way and report what it finds. Choose with `firewall.mode`.

## Let Bifrost manage the policy

```yaml
firewall:
  mode: managed
  trusted_interfaces:
    - tailscale0
  allow_ports:
    - 22
```

In managed mode Bifrost owns one nftables table named `bifrost`, and rebuilds it whenever the published services change. The table drops inbound IPv6 except for:

- Established and related traffic, so connections in progress survive every rule change.
- The loopback interface and every interface in `trusted_interfaces`.
- Essential ICMPv6, including the types path MTU discovery needs.
- Each published service, scoped to its own derived address and port. Opening a port for one service does not open it on any other address the host holds.
- Every port in `allow_ports`, on all addresses.

Bifrost never flushes the whole ruleset, so Docker, firewalld, and the distribution's own rules are untouched. Because nftables drops a packet when any base chain drops it, the managed table narrows a permissive host even when another table accepts. The reverse is not true: it cannot override another table's drop, which is what advisory mode reports.

:::caution
`allow_ports` is how you keep administering the host. If you reach this machine over IPv6 SSH, put `22` in `allow_ports` before enabling managed mode, or add the interface you administer over to `trusted_interfaces`. Existing sessions survive the change, but new ones are dropped without an allowance. Review `bifrost serve --dry-run`, which prints the exact policy, before starting the service.
:::

A clean `systemctl stop` removes the managed table, leaving the host exactly as it was before Bifrost started. A crash leaves the table in place, which fails closed.

## Manage the policy yourself

Set `firewall.mode: advisory` to have Bifrost only inspect and report. Add each pinhole to the firewall tool that already controls the host.

## Open a service port

Permit the configured TCP port for the exact service address. This nftables rule shows the required scope:

```text
ip6 daddr 2001:db8:1234:1::10 tcp dport 443 ct state new accept
```

Replace the example address. Limit the rule to the correct address, port, interface, and zone.

Do not expose the metrics listener. Bifrost requires this listener to use a loopback address.

## Permit essential ICMPv6

IPv6 routers do not fragment packets in transit. They return Packet Too Big messages to the sender. If a firewall drops these messages, small requests can work while large responses and media streams stop.

Permit these ICMPv6 errors for established traffic:

- Destination Unreachable, type 1
- Packet Too Big, type 2
- Time Exceeded, type 3
- Parameter Problem, type 4

Echo Request and Echo Reply, types 128 and 129, help with tests. They do not replace types 1 through 4.

Router Discovery and Neighbor Discovery use types 133 through 137. Permit this link-local traffic only on the correct interface. Keep the required address scope and hop-limit checks. Add these rules through NetworkManager, systemd-networkd, firewalld, ufw, or the main nftables ruleset.

## Open the router firewall

A host firewall rule does not change the customer-edge router, in either mode. Many home routers block inbound IPv6 traffic even when they do not use NAT, and some permit all of it: managed mode is worth enabling in both cases, because it is the only policy Bifrost can guarantee.

Create a router pinhole for the service address and port. Use a narrow delegated-prefix rule only when the router cannot track individual addresses.

If local checks pass and an external probe fails, inspect the router firewall. Bifrost cannot change a router that it does not control.

## Check the path MTU

Run:

```sh
sudo bifrost check --config /etc/bifrost/config.yaml
```

The command reports the interface MTU and visible ICMPv6 policy. If you configure an external probe, the command also tests inbound reachability and Packet Too Big delivery.

An MTU such as 1492 on a PPPoE link is valid when Path MTU Discovery works. Do not lower every interface MTU before you test ICMPv6.

See the [external probe contract](../../reference/external-probe/) for the privacy boundary.
