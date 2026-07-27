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

The script downloads the latest release for this machine, verifies its checksum, and installs the deb or rpm package. The package creates the `bifrost` account, creates `/etc/bifrost`, and installs the systemd unit. It does not start anything, so it is safe to install before knowing whether the host qualifies; `apt-get remove bifrost` undoes it.

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
✓ INFO    privileges  running with the privilege needed to manage addresses and bind service ports
✓ INFO    ipv6-egress outbound IPv6 works
                      reached [2606:4700:4700::1111]:443
✓ INFO    firewall    no nftables IPv6 input base chain with a drop policy was found
                      inbound IPv6 reaches this host unfiltered unless a router or another firewall filters it
                      fix: set firewall.mode to managed so Bifrost scopes inbound IPv6 to the published services

This host can run Bifrost.
Next: bifrost init --interactive
```

Every `ERROR` line names the problem, the fix, and a link into [troubleshooting](../troubleshooting/). Do not continue until they are gone.

## 3. Create the configuration

```sh
sudo bifrost init --interactive
```

`init` asks for what it cannot detect, then writes every file itself. Blank answers accept the value in brackets.

```
This creates the Bifrost configuration, the address secret, and the DNS credential.
Run bifrost doctor first if you have not confirmed this host has usable IPv6.

Publication interface [eth0]:
Configuration directory [/etc/bifrost]:

Describe the first service to publish. More can be added later in /etc/bifrost/config.yaml.
Service name [myservice]: jellyfin
Public DNS name (for example media.example.com): media.example.com
Address the service already listens on [127.0.0.1:8096]:
Public port clients connect to [443]: 8096
  direct mode is unavailable: it needs the backend to own the public IPv6 address
Service mode (auto/splice) [auto]:

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

The token is not echoed and is written with mode `0600`. The zone ID is read from your account, so you do not need to open the Cloudflare dashboard.

The Cloudflare token needs `Zone:Read` and `DNS:Edit` on the zone that serves your name.

Nothing is written until you answer the last question.

The public port is 8096, matching Jellyfin's own port so the URL stays predictable. Bifrost terminates TLS on the listener with an automatically issued certificate for `media.example.com`, so clients connect with `https://` even though Jellyfin itself speaks plain HTTP. A backend that handles TLS itself, such as Plex, sets `tls: off` and passes raw TCP through.

## 4. Settle the firewall

Two firewalls sit in front of a published service, and Bifrost handles only one of them.

**The host.** Add `firewall.mode: managed` to `/etc/bifrost/config.yaml` and Bifrost scopes inbound IPv6 to the services it publishes, opening each port on that service's address alone. Keep your own access reachable at the same time:

```yaml
firewall:
  mode: managed
  allow_ports:
    - 22
```

This matters even when a router already blocks inbound traffic, and it matters most when the router does not: some routers permit all inbound IPv6, which leaves every listening port on the host reachable from the internet until something on the host filters it. See [firewall](../../networking/firewall/).

**The router.** Bifrost cannot configure this one, and on a router that blocks inbound IPv6 the published name will not answer until you do. Add an inbound rule permitting TCP 8096 to the address Bifrost publishes; the setting is usually under IPv6 firewall, pinholes, or port control. Permit the port, not the whole host. Also permit ICMPv6 types 1 to 4, since blocking them breaks path MTU discovery and produces connections that open and then stall.

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

`check` tests the service address, the local listener, DNS, firewall policy, and the client-address behaviour of the selected mode. It exits non-zero when any check fails.

From an IPv6 client elsewhere:

```sh
curl -6 -sI https://media.example.com:8096/
```

The URL uses `https` because Bifrost terminates TLS on the listener with a certificate for the published name; Jellyfin itself keeps speaking plain HTTP behind it.

## What you have now

Bifrost holds a dedicated IPv6 address for Jellyfin, keeps the AAAA record for `media.example.com` pointing at it, and re-derives both when your ISP changes your prefix. Traffic reaches Jellyfin over IPv6 without a relay.

The service is published to the internet with a valid certificate. Bifrost does not authenticate users: anyone who reaches the name gets the application's own login screen, so the application's authentication and updates remain yours to keep healthy.

## Next

- IPv4-only clients cannot reach this service. Add the [IPv4 edge](../../networking/edge/) if they need to.
- Add more services by editing `static_services` in `/etc/bifrost/config.yaml`, or enable [Docker discovery](../../guides/configuration/).
- Read the [configuration guide](../../guides/configuration/) for every field.
