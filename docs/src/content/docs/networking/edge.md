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

It needs public inbound IPv4 and outbound IPv6 to the home service. Install Bifrost the same way as at home:

```sh
curl -fsSL https://bifrost.biodun.dev/install.sh | sh
```

## Enrol the edge

One token carries the shared key and the names the edge may serve. On the **home** host:

```sh
sudo bifrost edge invite --address 203.0.113.10
```

It creates the key if absent and prints a token plus the configuration block to add. Running it again prints the same key, so an enrolled edge keeps working.

On the **edge** host, paste the token:

```sh
sudo bifrost edge join bfe1.eyJrIjoi...
```

`join` writes `/etc/bifrost/edge-key` and `/etc/bifrost/edge.yaml` with the allowlist from the token, sets ownership and modes, and starts the service. `--start=false` writes the files without starting it.

The token contains the key. Send it over a private channel; keep it out of chat, shell history, and repositories.

## Configure the home host

Add the `edge` block `invite` printed, and set `edge: true` on each service the edge serves. Those use splice mode automatically: the edge connects to a Bifrost listener, and direct mode creates none.

The home daemon publishes the edge address only after it owns the DNS name. The edge never touches DNS.

Restart to publish the A records:

```sh
sudo systemctl restart bifrost
sudo bifrost check --config /etc/bifrost/config.yaml
```

## Placement

Put the edge near the viewers, not near the home host. Every byte crosses the edge, so a request that travels to another continent and back pays that distance twice, and TCP throughput falls as round-trip time rises.

The edge is for reach, not speed. Direct IPv6 clients bypass it entirely and are always faster.

Publish more than one when viewers are spread out:

```yaml
edge:
  enabled: true
  ipv4_addresses:
    - 203.0.113.10
    - 198.51.100.20
  key_file: /etc/bifrost/edge-key
```

Each address becomes an A record for every edge-enabled service. Run `bifrost edge join` on each host with a token from the same home deployment; they share one key.

Multiple A records provide alternatives, not geographic steering: ordinary DNS does not guarantee that a client chooses the nearest edge. Use a GeoDNS or latency-aware DNS provider when regional routing matters. `bifrost check` tests every listed edge for every edge-enabled service, so one healthy edge cannot hide a broken address still being handed to clients.

## Route TLS traffic

The edge reads the TLS ClientHello and extracts the SNI name. It does not end TLS and does not hold the site certificate.

The edge rejects a connection when the SNI name is missing, invalid, or absent from `allow`. It reads no more than 64 KiB of handshake data and applies `handshake_timeout`.

Encrypted ClientHello can hide the SNI name. Use a static port map when the protocol does not provide a visible SNI name:

```yaml
static_maps:
  "2222": ssh.example.com:22
```

Edge port 2222 resolves the AAAA for `ssh.example.com` and connects to home port 22. Each static port must be unique and must not collide with the TLS listener.

## DNS caching

Successful AAAA answers are cached for their TTL; empty or failed answers for `negative_ttl`. During a transient DNS error the last valid address is reused for `stale_on_error`. One lookup per name runs at a time.

A failed dial invalidates the cached answer and re-resolves immediately, so a home prefix change is followed within one request rather than at TTL expiry.

The edge dials only global IPv6 addresses, rejecting private, loopback, link-local, multicast, documentation, transition, and discard ranges.

## Client metadata

The edge sends authenticated client metadata; the home listener verifies it before use.

A backend that supports PROXY protocol v2 receives the original IPv4 source. Otherwise Bifrost consumes the header and the backend sees the Bifrost process as its peer.

The home listener waits `header_timeout` (default 250ms) to identify the edge header. Raise it only if edge latency requires it; a larger value delays server-first protocols.

See the [edge protocol](../../reference/edge-protocol/) for the frame format and replay controls.
