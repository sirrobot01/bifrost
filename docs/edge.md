# Edge deployment

The edge is optional. It gives IPv4-only clients a public A-record path without moving IPv6-capable clients off the home AAAA path.

## Network model

```text
IPv6 client ──AAAA──────────────────────────────▶ home service
IPv4 client ──A──▶ public edge ──fresh AAAA────▶ home service
```

There is no persistent home-to-edge tunnel. A failed edge affects the A path, while the AAAA path and home daemon remain independent.

## Provisioning

The VPS needs public IPv4 and working outbound IPv6. Generate a shared 32-byte-or-longer secret and install the same value on both hosts:

```sh
umask 077
openssl rand -hex 32 > edge-key
sudo install -o bifrost-edge -g bifrost-edge -m 0600 edge-key /etc/bifrost/edge-key
```

Copy the same file to `/etc/bifrost/edge-key` on home with owner `bifrost`. Do not send it through chat, shell history, or an unencrypted repository.

On home, enable the global `edge` block and set `edge: true` on each participating splice service. On the VPS, add every TLS DNS name to `allow`. Then install `configs/edge.example.yaml` and `deploy/bifrost-edge.service`.

```sh
sudo useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin bifrost-edge
sudo install -d -o bifrost-edge -g bifrost-edge -m 0750 /etc/bifrost
sudo install -o bifrost-edge -g bifrost-edge -m 0600 configs/edge.example.yaml /etc/bifrost/edge.yaml
sudo install -m 0644 deploy/bifrost-edge.service /etc/systemd/system/bifrost-edge.service
sudo systemctl daemon-reload
sudo systemctl enable --now bifrost-edge
```

The home daemon publishes the VPS IPv4 as an A record only after it owns the name's TXT marker. The edge never writes DNS.

## TLS routing

The edge accepts TCP on `listen`, reads at most 64 KiB of TLS handshake data within `handshake_timeout`, and extracts the plaintext SNI hostname. It rejects missing, malformed, or unlisted SNI. It does not terminate TLS or possess the site's certificate.

Encrypted ClientHello can hide SNI and make this routing mode unusable for affected clients. Use an explicit static port mapping when the protocol is non-TLS or cannot expose SNI:

```yaml
static_maps:
  "2222": ssh.example.com:22
```

This listens on edge TCP/2222, resolves `ssh.example.com` AAAA, and dials home TCP/22. Each static edge port must be unique and cannot conflict with the TLS listener.

## Failure and cache behavior

Positive AAAA answers are cached for their DNS TTL. Empty or failed lookups are negatively cached for `negative_ttl`. A previously valid address may be used for `stale_on_error` beyond its TTL during a transient resolver failure. Only one lookup per hostname runs at once.

The edge rejects private, loopback, link-local, multicast, documentation, transition, discard, and other configured non-production IPv6 ranges. It dials only TCP over IPv6. Global connections, per-source rate, rate burst, handshake size/time, and idle time are bounded.

## Client identity

The home listener authenticates the edge metadata before using it. If the final backend declares PROXY v2 support, Bifrost re-emits the original IPv4 source and edge destination. Otherwise the header is consumed and the backend sees the home Bifrost process as its TCP peer.

Edge-enabled home listeners wait up to `header_timeout` to distinguish a server-first native connection from an edge header. The default 250 ms favors ordinary client-first TLS and HTTP protocols. Increase it only when edge latency requires it; decrease it if server-first protocol latency matters.
