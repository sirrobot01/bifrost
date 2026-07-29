---
title: Quickstart
description: Publish one service through native IPv6, from an unconfigured host to a working name.
---

This publishes one Jellyfin server at `media.example.com`. The server runs on a Linux host at home and listens on `127.0.0.1:8096`. The connection has global IPv6 and CGNAT IPv4. DNS is on Cloudflare.

Substitute your own name, backend, and provider. The sequence does not change.

## Before you start

You need three things:

- A Linux host on a connection with global IPv6.
- A DNS name you control, on Cloudflare, deSEC, dynv6, or an RFC 2136 server.
- Access to your router to permit inbound IPv6.

## 1. Install Bifrost

```sh
curl -fsSL https://bifrost.biodun.dev/install.sh | sh
```

The script downloads the latest release for this machine, verifies its checksum, and installs the deb or rpm package. The package creates the service accounts and `/etc/bifrost`, and installs the systemd units. It starts nothing, so it is safe to install before knowing whether the host qualifies; `apt-get remove bifrost` undoes it.

See [installation](../installation/) for direct package downloads, archives, containers, and signature verification.

## 2. Check the host

Run `doctor` before you configure anything. It reads no configuration and changes nothing.

```sh
sudo bifrost doctor
```

```
✓ INFO    platform    host runs Linux
✓ INFO    interface   publication interface is eth0
✓ INFO    mtu         interface MTU is suitable for IPv6
                      MTU 1500
✓ INFO    ipv6-prefix interface holds a usable global IPv6 /64
                      selected 2001:db8:abcd:1200::/64
! WARNING inbound     inbound reachability was not tested
                      every other check here observes this host, and a host looks
                      identical whether or not the internet can reach it
                      fix: re-run with --probe to have an outside vantage open a
                      connection to this host
✓ INFO    privileges  running with the privilege needed to manage addresses and bind service ports
✓ INFO    ipv6-egress outbound IPv6 works
                      reached [2606:4700:4700::1111]:443
✓ INFO    firewall    no nftables IPv6 input base chain with a drop policy was found
                      inbound IPv6 reaches this host unfiltered unless a router or another firewall filters it
                      fix: set firewall.mode to managed so Bifrost scopes inbound IPv6 to the published services

This host can run Bifrost, with 1 warning to review.
Next: bifrost init --interactive
```

Every `ERROR` line names the problem, the fix, and a link into [troubleshooting](../troubleshooting/). Do not continue until they are gone. Warnings do not block publication.

The `inbound` warning is the honest one: nothing running on this host can tell whether your router lets the internet in. `sudo bifrost doctor --probe <url>` answers it by having an outside vantage open a connection to a temporary listener here. Step 6 covers the same question for a configured service.

## 3. Create the configuration

```sh
sudo bifrost init --interactive
```

`init` asks only for what it cannot work out, then writes every file itself. Blank answers accept the value in brackets. The interface, the service name, the public port, and the exposure mode are derived and shown rather than asked about. The zone is looked up from your provider account and offered as the default.

```
This creates the Bifrost configuration, the address secret, and the DNS credential.
Run bifrost doctor first if you have not confirmed this host has usable IPv6.

Publishing from eth0.

Describe the first service to publish. More can be added later in /etc/bifrost/config.yaml.
Public DNS name (for example media.example.com): media.example.com
Address the service already listens on [127.0.0.1:8096]:
  service "media", published on port 443, mode auto
  Bifrost will terminate TLS and forward to the backend

DNS provider (cloudflare/desec/dynv6/rfc2136) [cloudflare]:
Cloudflare API token (input hidden):
  looking up the Cloudflare zone for media.example.com
  found zone example.com (023e105f4ecef8ad9ca31a8372d0c353)

About to create /etc/bifrost and these files:
  /etc/bifrost/cloudflare-token            Cloudflare API token
  /etc/bifrost/address-secret              address secret
  /etc/bifrost/config.yaml                 configuration
Write them? (Y/n):
```

The token is not echoed, is written with mode `0600`, and needs `Zone:Read` and `DNS:Edit` on the zone serving your name. Nothing is written until you answer the last question.

The public port is 443, so the name works in a browser without one, and Bifrost terminates TLS there with a certificate it issues for `media.example.com` — Jellyfin keeps speaking plain HTTP on 8096 behind it. A backend that handles TLS itself sets `tls: off` and gets raw passthrough instead. See [application notes](../../guides/applications/).

Each service gets its own IPv6 address, so several can share port 443 without a reverse proxy to tell them apart.

## 4. Settle the firewall

Two firewalls sit in front of a published service. Bifrost owns one and can only ask the other.

**The host.** Add this to `/etc/bifrost/config.yaml` and Bifrost scopes inbound IPv6 to the services it publishes, each port on that service's address alone:

```yaml
firewall:
  mode: managed
  allow_ports:
    - 22
  pcp: true
```

Keep `22` in `allow_ports` if you administer this host over IPv6. Some routers permit all inbound IPv6, which leaves every listening port on the host exposed until something here filters it.

**The router.** `pcp: true` asks it to open each published port. Most routers do not answer, and the request costs nothing when they do not. If yours blocks inbound IPv6 and ignores PCP, add the rule by hand: permit TCP 443 to the address Bifrost publishes — the setting is usually under IPv6 firewall, pinholes, or port control. Permit the port, not the whole host. Permit ICMPv6 types 1 to 4 as well; blocking them produces connections that open and then stall.

See [firewall](../../networking/firewall/).

## 5. Review the plan, then start

`--dry-run` prints the DNS records and host addresses Bifrost would change, and changes nothing.

```sh
sudo bifrost serve --config /etc/bifrost/config.yaml --dry-run
```

Read the output. Start the service only when it matches what you expect.

```sh
sudo systemctl enable --now bifrost
```

## 6. Confirm the path

```sh
sudo bifrost check --config /etc/bifrost/config.yaml
```

`check` tests the service address, the TLS certificate, the local listener, public DNS and its ownership marker, firewall policy, and the client-address behaviour of the selected mode. It exits non-zero when any check fails.

Read the last line. Every check above it ran on this host, and a host looks the same whether or not the internet can reach it, so `check` says so rather than implying success:

```
Every local check passed.
Outside verification did not prove every service reachable from the internet.
Configure or repair an external probe, then check again.
```

With a `probe.endpoint` configured, `check` reaches each service from outside. An [edge](../../networking/edge/) does the same for each edge-enabled service, through every configured edge. Only when every service is confirmed does the line become `Every check passed, and the services answered from outside this network.` Add `--require-external` to make partial or missing verification a non-zero exit.

Then, from an IPv6 client on another network:

```sh
curl -6 -sI https://media.example.com/
```

## What you have now

Bifrost holds a dedicated IPv6 address for Jellyfin, keeps `media.example.com` pointing at it, and re-derives both when your ISP changes your prefix. Traffic reaches Jellyfin over IPv6 without a relay, under a certificate Bifrost renews.

Bifrost does not authenticate users. Anyone who reaches the name gets the application's own login screen, so its authentication and updates remain yours to keep healthy.

## Next

- IPv4-only clients cannot reach this service. Add the [IPv4 edge](../../networking/edge/) if they need to.
- Add more services with `sudo bifrost publish <name> <backend>`, or enable [Docker discovery](../../guides/configuration/).
- Read the [configuration guide](../../guides/configuration/) for every field.
