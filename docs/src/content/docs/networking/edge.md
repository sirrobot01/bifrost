---
title: IPv4 edge
description: Give IPv4-only clients an optional path to services at home.
---

The edge is optional. It gives IPv4-only clients a public IPv4 path. IPv6 clients continue to connect directly to the home service.

```text
IPv6 client --AAAA---------------------------> home service
IPv4 client --A--> public edge --fresh AAAA--> home service
```

The home host does not keep a tunnel open to the edge. An edge failure affects the IPv4 path only.

## Prepare the edge host

The edge host needs these network paths:

- Public inbound IPv4
- Outbound IPv6 to the home service

Create a shared key with at least 32 bytes:

```sh
umask 077
openssl rand -hex 32 > edge-key
sudo install -o bifrost-edge -g bifrost-edge -m 0600 edge-key /etc/bifrost/edge-key
```

Install the same file at `/etc/bifrost/edge-key` on the home host. Set its owner to `bifrost` and its mode to `0600`. Do not put the key in chat, shell history, or a repository.

## Configure the home host

Add the global `edge` block. Set `edge: true` on each splice service that must accept edge traffic.

The home daemon publishes the edge IPv4 address only after it owns the DNS name. The edge does not update DNS.

## Configure the edge service

Add every permitted TLS name to `allow`. Then install the example configuration and systemd unit:

```sh
sudo useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin bifrost-edge
sudo install -d -o bifrost-edge -g bifrost-edge -m 0750 /etc/bifrost
sudo install -o bifrost-edge -g bifrost-edge -m 0600 configs/edge.example.yaml /etc/bifrost/edge.yaml
sudo install -m 0644 deploy/bifrost-edge.service /etc/systemd/system/bifrost-edge.service
sudo systemctl daemon-reload
sudo systemctl enable --now bifrost-edge
```

## Route TLS traffic

The edge reads the TLS ClientHello and extracts the SNI name. It does not end TLS and does not hold the site certificate.

The edge rejects a connection when the SNI name is missing, invalid, or absent from `allow`. It reads no more than 64 KiB of handshake data and applies `handshake_timeout`.

Encrypted ClientHello can hide the SNI name. Use a static port map when the protocol does not provide a visible SNI name:

```yaml
static_maps:
  "2222": ssh.example.com:22
```

This rule listens on edge TCP port 2222. It resolves the AAAA record for `ssh.example.com` and connects to home TCP port 22. Each static edge port must be unique. It must not conflict with the TLS listener.

## Understand DNS cache behavior

The edge caches successful AAAA answers for their DNS TTL. It caches empty or failed answers for `negative_ttl`.

During a temporary DNS error, the edge can use the last valid address for `stale_on_error`. Only one lookup for a name runs at a time.

The edge connects only to permitted global IPv6 addresses. It rejects private, loopback, link-local, multicast, documentation, transition, discard, and other configured non-production ranges.

## Preserve client metadata

The edge sends authenticated client metadata to the home listener. The home listener verifies the metadata before it uses it.

If the final backend supports PROXY protocol v2, Bifrost can send the original IPv4 source to that backend. Otherwise, Bifrost consumes the edge header and the backend sees the home Bifrost process as its TCP peer.

An edge-enabled home listener waits for `header_timeout` to identify the edge header. The default value is 250 milliseconds. Increase it only when edge latency requires more time. A larger value delays server-first protocols.

See the [edge protocol](../../reference/edge-protocol/) for the frame format and replay controls.
