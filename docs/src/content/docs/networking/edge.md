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

Install Bifrost on it the same way as at home:

```sh
curl -fsSL https://bifrost.biodun.dev/install.sh | sh
```

## Enrol the edge

Both hosts need the same shared key, and the edge needs the list of names it may serve. One token carries both.

On the **home** host:

```sh
sudo bifrost edge invite --address 203.0.113.10
```

That creates the shared key if it does not exist, and prints a token along with the configuration block to add. Reusing the command later prints the same key, so an already-enrolled edge keeps working.

On the **edge** host, paste the token:

```sh
sudo bifrost edge join bfe1.eyJrIjoi...
```

`join` writes `/etc/bifrost/edge-key` and `/etc/bifrost/edge.yaml` with the allowlist from the token, sets their ownership and modes, and starts the service. Pass `--start=false` to write the files without starting it.

The token contains the shared key. Send it over a private channel, and keep it out of chat, shell history, and repositories.

## Configure the home host

Add the `edge` block that `invite` printed, and set `edge: true` on each service the edge should serve. Those services use splice mode automatically, because the edge connects to a Bifrost listener and direct mode creates none.

The home daemon publishes the edge IPv4 address only after it owns the DNS name. The edge does not update DNS.

Restart the home daemon to publish the A records:

```sh
sudo systemctl restart bifrost
sudo bifrost check --config /etc/bifrost/config.yaml
```

## Place the edge near the viewers

An edge relays every byte, so its location decides what IPv4 clients experience. Put it near the people using the services, not near the home host: a request that crosses an ocean to reach the edge and crosses back to reach home pays that distance twice, and TCP throughput falls as round-trip time rises.

Measured from one deployment, a single edge one continent away from both the viewers and the origin was slower than a commercial tunnel with a nearby point of presence. The same services reached directly over IPv6 were several times faster than either. The edge exists so IPv4-only clients can connect at all; it is not the fast path.

Publish more than one when viewers are spread out:

```yaml
edge:
  enabled: true
  ipv4_addresses:
    - 203.0.113.10
    - 198.51.100.20
  key_file: /etc/bifrost/edge-key
```

Every listed address is published as an A record for each edge-enabled service, and clients pick between them. Run `bifrost edge join` on each host with a token from the same home deployment; they share the key.

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
