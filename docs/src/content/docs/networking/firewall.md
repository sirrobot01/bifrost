---
title: Firewall and path MTU
description: Open narrow IPv6 pinholes and keep essential ICMPv6 traffic working.
---

Bifrost does not manage the host firewall. It checks common rules and reports problems. Add each pinhole to the firewall tool that already controls the host.

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

A host firewall rule does not change the customer-edge router. Many home routers block inbound IPv6 traffic even when they do not use NAT.

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
