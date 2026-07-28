---
title: Troubleshooting setup
description: What each doctor and check finding means, and what to do about it.
---

Two commands produce findings. `bifrost doctor` judges the host and needs no configuration. `bifrost check` judges a configured deployment end to end.

Both accept `--json` for scripts, and both exit non-zero when any finding is an error.

```sh
sudo bifrost doctor                                     # --probe <url> to test inbound
sudo bifrost check --config /etc/bifrost/config.yaml    # --require-external to demand proof
```

Findings are `INFO`, `WARNING`, or `ERROR`. Only errors block publication. A warning is a condition to understand, not always a fault.

## doctor findings

### `platform`

The home role manages addresses on a Linux interface and has no equivalent elsewhere. Run it on the Linux host that owns the public IPv6 addresses.

### `interface`

Bifrost auto-detects the interface holding a public IPv6 address. It stops when there is none, or more than one.

With more than one, name the interface that faces your ISP:

```sh
sudo bifrost doctor --interface eth0
```

With none, the host has no public IPv6 yet. That is an ISP or router condition, not a Bifrost one.

### `mtu`

Below 1280 the link cannot carry IPv6 and the error is fatal. Between 1280 and 1500 is a warning, normal on PPPoE, and safe only while path MTU discovery works. See `pmtu` below.

### `ipv6-prefix`

The interface holds no `/64` that Bifrost can publish from. The detail line counts why each address was rejected.

**Temporary privacy addresses.** The kernel rotates these, so a published record would break every few hours. Bifrost refuses them. Keep a stable address on the publication interface:

```sh
sudo sysctl -w net.ipv6.conf.eth0.use_tempaddr=0
```

Persist it in `/etc/sysctl.d/` and re-run `doctor`. Privacy addresses on other interfaces are unaffected.

**Not a public address.** Only link-local (`fe80::`) or unique-local (`fd00::`) addresses are present. The ISP is not delegating a routed prefix to this link, or the router is not advertising it.

**Deprecated, or past its advertised lifetime.** The router stopped advertising the prefix. Check that the router still holds its delegation.

**No IPv6 addresses at all.** Confirm IPv6 is enabled on the host and that the ISP delegates a prefix.

### `privileges`

A warning when you run `doctor` as an ordinary user. Splice mode needs `CAP_NET_ADMIN` and ports below 1024 need `CAP_NET_BIND_SERVICE`. The supplied systemd unit grants both, so this warning does not apply to the running service. Use `sudo` to exercise privileged checks.

### `ipv6-egress`

This host cannot open an outbound IPv6 connection. Inbound publication cannot work while this fails, so fix it first. Check for an IPv6 default route:

```sh
ip -6 route show default
```

Use `--offline` to skip this and every other check that leaves the host.

### `firewall`

In managed mode this reports that Bifrost owns the policy, and warns when another table's drop policy can override it. In advisory mode Bifrost only reads nftables and reports an authoritative input chain that could drop inbound IPv6, or reports that nothing filters inbound IPv6 at all: on a router that permits all inbound IPv6, that means every listening port on the host is reachable from the internet, not only the published services. Switching to `firewall.mode: managed` is the fix.

Add service-scoped accepts to whichever firewall manager owns the policy. A separate Bifrost table cannot override another table's drop. See [firewall](../../networking/firewall/).

### `icmpv6`

Essential ICMPv6 error traffic may be blocked. Path MTU discovery needs Destination Unreachable (1), Packet Too Big (2), Time Exceeded (3), and Parameter Problem (4); blocking them produces connections that open and then stall. Add scoped accepts for those types in the firewall manager that owns the policy. See [firewall](../../networking/firewall/).

### `inbound`

The one question no local check can answer: whether the internet can reach this host at all. Without `--probe` it is a warning, because every other finding here observes the host, and a host looks identical whether or not inbound traffic arrives.

`sudo bifrost doctor --probe <url>` binds a temporary listener and asks an outside vantage to reach it. An error means the customer-edge router is dropping inbound traffic; nothing on the host can change that.

### `docker`

Reported only when you pass `--docker-socket`. Docker socket access grants root-level control of the host. Prefer a restricted socket proxy.

## check findings

`check` covers everything above, plus the following.

### `address`

The derived service address is missing from the host. The service is not running, or it has not reconciled yet. Read `systemctl status bifrost` and the daemon logs.

### `tls`

The listener's certificate failed the handshake, expires within 14 days, or has expired. Issuance and renewal errors appear in the serve log; the usual causes are the provider rejecting the challenge record (check `dns-owner` and the provider credentials) or the CA being unreachable from the host. Renewal starts 30 days before expiry, so an expiry warning means renewals have been failing for at least two weeks. `bifrost_certificate_expiry_seconds` exposes the same signal to monitoring.

### `listener`

The address exists but nothing accepts TCP on the published port. In splice mode Bifrost owns that listener, so this points at the daemon. In direct mode the backend owns it, so confirm the backend is listening on the public address.

### `dns`

The published name does not resolve to the expected address. When the local answer is wrong, `check` also asks the zone's authoritative nameservers and reports which side disagrees:

- A warning that the authoritative nameserver has the address means only the local resolver is behind, usually a cached negative answer from lookups made before publication. Wait for the cache to expire or flush the resolver.
- An error that the authoritative nameserver does not serve the address means the record never reached the nameservers, even when `dns-owner` reports correct provider state. Confirm the DNS account is activated and the zone is delegated to the provider's nameservers.
- If the authoritative check itself fails, confirm the DNS name is correct and the host can reach the internet.

### `dns-owner`

Bifrost marks the records it owns with a TXT record and refuses to modify records it does not own. This finding means the credential is wrong, the zone is wrong, or a record at that name belongs to something else.

Resolve the conflict at the provider, or publish under a different name. Do not delete the ownership marker while the service is running.

### `external`

The service was not reachable from outside, while the local address, listener, and host policy are all correct. The finding names a failed edge when an edge pool is the vantage; every published edge must work. Otherwise the remaining hop is the router, so confirm its inbound IPv6 rule.

With no way to look from outside this is a warning instead, and `check` closes by saying that outside verification did not cover every service. An enabled edge serves as the prober automatically; `probe.endpoint` uses an HTTPS service instead. `--require-external` makes the unverified case a non-zero exit, which is what you want in a monitoring job. See [external probe](../../reference/external-probe/).

### `pmtu`

The probe delivered a packet that should have produced ICMPv6 Packet Too Big and did not. This is the classic blackhole: connections open, then stall on the first large response.

Permit ICMPv6 types 1 to 4 end to end, on the host and on the router.

### `client-ip`

A warning that splice mode hides the client address from the backend. That is inherent to splice. Use direct mode to preserve it, or enable PROXY protocol v2 only when the backend explicitly supports it. A backend that does not parse PROXY v2 will treat the header as request data.

## Setup problems without a finding

**`init` says a file already exists.** It refuses to overwrite secrets. Move the file aside, or pass `--force` when you intend to replace it.

**The Cloudflare zone lookup fails.** The token cannot read the zone. It needs `Zone:Read` and `DNS:Edit` on the zone that serves your name. `init` falls back to asking for the zone ID directly.

**A secret file is rejected at startup.** Bifrost refuses secrets readable by group or other:

```sh
sudo chmod 0600 /etc/bifrost/address-secret
sudo chown bifrost:bifrost /etc/bifrost/address-secret
```

**The published name works from inside the house but not outside.** The router is not forwarding inbound IPv6. Loopback from inside often bypasses it.
