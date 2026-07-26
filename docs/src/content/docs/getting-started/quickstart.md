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

The next command tells you whether the first one is true.

## 1. Check the host

Run `doctor` before you install anything else. It reads no configuration and changes nothing.

```sh
sudo bifrost doctor
```

```
INFO    platform    host runs Linux
INFO    interface   publication interface is eth0
INFO    mtu         interface MTU is suitable for IPv6
                    MTU 1500
INFO    ipv6-prefix interface holds a usable global IPv6 /64
                    selected 2001:db8:abcd:1200::/64
INFO    privileges  running with the privilege needed to manage addresses and bind service ports
INFO    ipv6-egress outbound IPv6 works
                    reached [2606:4700:4700::1111]:443
INFO    firewall    no nftables IPv6 input base chain with a drop policy was found

This host can run Bifrost.
Next: bifrost init --interactive
```

Every `ERROR` line names the problem and the fix. Do not continue until they are gone. [Troubleshooting](../troubleshooting/) covers each check.

## 2. Install Bifrost

```sh
curl -fsSLO https://github.com/sirrobot01/bifrost/releases/latest/download/bifrost_linux_amd64.deb
sudo apt-get install -y ./bifrost_linux_amd64.deb
```

The package creates the `bifrost` account, creates `/etc/bifrost`, and installs the systemd unit. It does not start anything.

See [installation](../installation/) for RPM, archives, containers, and signature verification.

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
Public port clients connect to [443]:
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

## 4. Permit inbound IPv6 on the router

Bifrost cannot do this for you, and the published name will not answer until it is done.

Add an inbound rule permitting TCP 443 to the address Bifrost publishes. Routers differ; the rule is usually under IPv6 firewall, pinholes, or port control. Permit the port, not the whole host.

Also permit ICMPv6 types 1 to 4. Blocking them breaks path MTU discovery, which produces connections that open and then stall. See [firewall](../../networking/firewall/).

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
curl -6 -sI https://media.example.com/
```

## What you have now

Bifrost holds a dedicated IPv6 address for Jellyfin, keeps the AAAA record for `media.example.com` pointing at it, and re-derives both when your ISP changes your prefix. Traffic reaches Jellyfin over IPv6 without a relay.

The service is published to the internet. Bifrost does not terminate TLS, issue certificates, or authenticate users. Securing the application remains yours.

## Next

- IPv4-only clients cannot reach this service. Add the [IPv4 edge](../../networking/edge/) if they need to.
- Add more services by editing `static_services` in `/etc/bifrost/config.yaml`, or enable [Docker discovery](../../guides/configuration/).
- Read the [configuration guide](../../guides/configuration/) for every field.
