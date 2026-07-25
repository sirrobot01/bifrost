# Bifrost

Bifrost is a Linux ingress daemon for self-hosters who have public IPv6 but sit behind IPv4 CGNAT. It follows prefix changes, publishes owned DNS records, creates stable per-service IPv6 addresses, and exposes TCP services without putting the native IPv6 path through a VPS.

When a backend already has a usable IPv6 address, `direct` mode publishes it with no Bifrost data path. When a backend is IPv4-only, `splice` mode accepts IPv6 on a Bifrost-owned address and bridges TCP to the backend. An optional dual-stack edge carries only IPv4-client traffic while AAAA traffic continues directly to home.

## Why Bifrost

- Native IPv6 stays native: no persistent tunnel and no relay bandwidth bill.
- Prefix rotation is one lifecycle across service addresses, DNS, listeners, and drain periods.
- DNS ownership is recoverable from TXT sidecars, so Bifrost refuses to replace records it does not own.
- Firewall policy remains yours. Bifrost audits it and gives precise advice; v1 does not install a global firewall policy.
- `bifrost check` covers local addresses/listeners, provider-owned DNS state, public DNS, ICMPv6 policy, and optional external PMTU testing.
- Docker labels and static YAML use the same service model.

Bifrost is not a NAT traversal or hole-punching system. If there is no usable IPv6 path and no edge, Bifrost cannot create one. Requiring a companion client would exclude shared users on stock browsers, TVs, and mobile apps; transparency to unmodified clients remains a core property.

## Requirements

- Linux for `bifrost serve`; the edge role also targets Linux releases.
- A globally routed IPv6 /64 on the Bifrost host.
- Permission to add IPv6 addresses and bind the configured ports.
- An inbound rule on the customer-edge router. Bifrost cannot change an ISP router's firewall.
- Cloudflare, deSEC, dynv6, or RFC 2136 credentials scoped to the managed zone.
- Go 1.26 when building from source.

The v1 home role runs on the host that owns Bifrost-managed service addresses. A LAN backend can sit elsewhere, but it normally uses `splice`; Bifrost does not configure another machine's interface or the router.

## Quick start

Build and install the binary:

```sh
make build verify
sudo install -m 0755 bin/bifrost /usr/local/bin/bifrost
```

Create the configuration and secrets:

```sh
sudo useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin bifrost
sudo install -d -o bifrost -g bifrost -m 0750 /etc/bifrost
sudo -u bifrost /usr/local/bin/bifrost init --interface eth0 --secret-file /etc/bifrost/address-secret --output /etc/bifrost/config.yaml
sudo -u bifrost install -m 0600 /dev/null /etc/bifrost/address-secret
openssl rand -hex 32 | sudo -u bifrost tee /etc/bifrost/address-secret >/dev/null
sudo -u bifrost install -m 0600 /dev/null /etc/bifrost/cloudflare-token
sudoedit /etc/bifrost/cloudflare-token
sudoedit /etc/bifrost/config.yaml
sudo chown bifrost:bifrost /etc/bifrost/address-secret /etc/bifrost/cloudflare-token /etc/bifrost/config.yaml
sudo chmod 0600 /etc/bifrost/address-secret /etc/bifrost/cloudflare-token /etc/bifrost/config.yaml
```

Review the complete plan before changing addresses or DNS:

```sh
sudo -u bifrost /usr/local/bin/bifrost status --offline --config /etc/bifrost/config.yaml
sudo bifrost serve --config /etc/bifrost/config.yaml --dry-run
```

Install the service:

```sh
sudo install -m 0644 deploy/bifrost.service /etc/systemd/system/bifrost.service
sudo systemctl daemon-reload
sudo systemctl enable --now bifrost
sudo bifrost check --config /etc/bifrost/config.yaml
```

Copy [configs/bifrost.example.yaml](configs/bifrost.example.yaml) if you prefer to start from the full example. See [installation](docs/installation.md), [configuration](docs/configuration.md), and [firewall and PMTU](docs/firewall.md) before exposing a real service.

## Service modes

| Mode | Path | Backend sees client IP | Use when |
|---|---|---|---|
| `direct` | client → backend IPv6 | Yes | The backend owns and listens on a public IPv6 address and port |
| `splice` | client → Bifrost IPv6 → backend | No, unless the backend explicitly supports PROXY v2 | The backend is IPv4-only or cannot own the public address |
| `auto` | conservative direct check, otherwise splice | Depends on selection | You want Bifrost to choose and report the result |

Plex and Jellyfin do not consume PROXY protocol in the common deployment, so their splice backends see the Bifrost host as the peer. Prefer direct mode where the application and topology support it. Edge-enabled v1 services use `splice` because the home endpoint must authenticate and consume edge metadata.

## Docker labels

```yaml
services:
  jellyfin:
    image: jellyfin/jellyfin
    labels:
      bifrost.enable: "true"
      bifrost.name: jellyfin
      bifrost.port: "8096"
      bifrost.listen: "443"
      bifrost.dns: media.example.com
      bifrost.mode: splice
      bifrost.network: media
      bifrost.edge: "false"
```

If a container joins multiple networks, `bifrost.network` is required. `bifrost.backend` can provide an explicit IP and port instead. Access to the Docker socket is root-equivalent even when mounted read-only; use a narrowly configured socket proxy when practical.

## Optional IPv4 edge

The edge listens on IPv4, extracts an allowlisted TLS SNI name without terminating TLS, resolves only acceptable global AAAA destinations, and connects home over IPv6. It authenticates client metadata with a fresh HMAC-bound PROXY v2 TLV. Non-TLS services require explicit static port mappings.

Generate one shared key, install it with mode `0600` on both systems, enable the matching service in the home config, and run `bifrost edge` with [configs/edge.example.yaml](configs/edge.example.yaml). The A and AAAA records share a Bifrost ownership marker but lead to independent paths. See [edge deployment](docs/edge.md) and the [edge protocol](docs/edge-protocol.md).

## Operations

```sh
bifrost status --config /etc/bifrost/config.yaml
bifrost status --config /etc/bifrost/config.yaml --json
bifrost check --config /etc/bifrost/config.yaml
curl http://127.0.0.1:9098/healthz
curl http://127.0.0.1:9098/metrics
```

The local observability server exposes `/healthz`, `/status`, and Prometheus `/metrics`. Logs are structured JSON by default; pass `--log-format text` for journald-friendly text.

## Scope

v1 supports TCP. It does not terminate TLS, issue certificates, provide authentication, proxy UDP/QUIC, configure ISP routers, rewrite application configuration, or bypass vendor licensing. If an HTTP-only Cloudflare proxy or a tunnel product already meets your requirements, use it; Bifrost is aimed at direct IPv6 and non-HTTP or high-bandwidth paths where avoiding full-time relay is valuable.

Application notes: [Jellyfin](docs/jellyfin.md), [Immich](docs/immich.md), and [Plex](docs/plex.md). Security boundaries are in [SECURITY.md](SECURITY.md) and [docs/security.md](docs/security.md).

## Development

```sh
make test
make lint
make verify
```

Releases contain static Linux archives for x86-64, arm64, and arm/v7, a SHA-256 checksum file, a keyless Sigstore bundle for that checksum, and a signed multi-architecture container image. See [docs/releasing.md](docs/releasing.md).

## License

MIT
