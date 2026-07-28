---
title: Firewall and path MTU
description: Scope inbound IPv6 to the published services and keep essential ICMPv6 working.
---

Two firewalls sit in front of a published service. Bifrost can manage the host one. It can ask the router, but cannot insist.

## Managed mode

```yaml
firewall:
  mode: managed
  trusted_interfaces:
    - tailscale0
  allow_ports:
    - 22
  pcp: true
```

Bifrost owns one nftables table, `bifrost`, rebuilt whenever the published services change. It drops inbound IPv6 except:

- Established and related traffic.
- Loopback and every interface in `trusted_interfaces`.
- Essential ICMPv6.
- Each published service, on its own derived address and port.
- Every port in `allow_ports`, on all addresses.

Nothing outside that table is touched, so Docker and the distribution firewall keep their rules.

:::caution
Put `22` in `allow_ports` before enabling this if you administer the host over IPv6. Existing sessions survive; new ones are dropped without an allowance. `bifrost serve --dry-run` prints the exact policy first.
:::

`systemctl stop` removes the table. A crash leaves it in place, which fails closed.

With `pcp: true`, Bifrost also asks the router to permit each published socket, using PCP. This is independent of managed mode, so it also works with `mode: advisory`. Bifrost refreshes each mapping halfway through the lifetime the router grants and releases it before removing the service address. Most routers do not answer; nothing changes when they do not.

## Advisory mode

`firewall.mode: advisory` inspects and reports only. Add pinholes to whichever tool owns the host policy.

Scope each rule to one service address and port:

```text
ip6 daddr 2001:db8:1234:1::10 tcp dport 443 ct state new accept
```

Never expose the metrics listener. Bifrost requires it to bind loopback.

## Essential ICMPv6

IPv6 routers do not fragment in transit; they return Packet Too Big to the sender. Dropping these produces connections that open and then stall on the first large response.

Permit for established traffic:

| Type | Message |
|---|---|
| 1 | Destination Unreachable |
| 2 | Packet Too Big |
| 3 | Time Exceeded |
| 4 | Parameter Problem |

Echo Request and Reply (128, 129) help with testing but do not replace types 1 to 4.

Neighbor and Router Discovery use types 133 to 137. Permit that link-local traffic on the correct interface only, keeping scope and hop-limit checks.

## The router

A host rule does not change the customer-edge router. Many home routers block inbound IPv6; some permit all of it. Managed mode is worth enabling either way, since it is the only policy Bifrost controls.

Create a pinhole for the service address and port. Use a prefix-wide rule only when the router cannot match individual addresses.

If local checks pass and the external probe fails, the router is the remaining hop.

## Path MTU

```sh
sudo bifrost check --config /etc/bifrost/config.yaml
```

Reports the interface MTU and visible ICMPv6 policy. An enabled edge tests inbound reachability only; a configured `probe.endpoint` can also test Packet Too Big delivery when it returns a measured path result. See [external probe](../../reference/external-probe/).

An MTU of 1492 on PPPoE is fine when Path MTU Discovery works. Test ICMPv6 before lowering MTUs.
