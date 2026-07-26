<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/src/assets/bifrost-lockup-dark.png">
    <source media="(prefers-color-scheme: light)" srcset="docs/src/assets/bifrost-lockup-light.png">
    <img alt="Bifrost" src="docs/src/assets/bifrost-lockup-light.png" width="360">
  </picture>
</p>

Bifrost publishes self-hosted TCP services through native IPv6. It is for Linux hosts that have public IPv6 and use CGNAT for IPv4.

Bifrost watches the delegated prefix, manages owned DNS records, and checks the inbound network path. It can also give an IPv4-only backend a stable IPv6 address.

IPv6 traffic stays at home. An optional public edge carries only traffic from IPv4 clients.

## Select a service mode

| Mode | Network path | Client address at backend | Use case |
| --- | --- | --- | --- |
| `direct` | Client to backend IPv6 | Preserved | The backend owns a public IPv6 address and listens on the public port. |
| `splice` | Client to Bifrost to backend | Not preserved unless the backend accepts PROXY v2 | The backend is IPv4-only or cannot own the public address. |
| `auto` | Direct when all checks pass; otherwise splice | Depends on the selected mode | Bifrost must select and report the safe path. |

Bifrost does not use a persistent tunnel. It does not require client software. It does not use NAT traversal or hole punching.

## Requirements

- Linux
- A global IPv6 `/64` on the Bifrost host
- Permission to add IPv6 addresses and bind service ports
- An inbound IPv6 rule on the home router
- Cloudflare, deSEC, dynv6, or RFC 2136 credentials
- Go 1.26 to build from source

The v1 home role must run on the host that owns the managed service addresses. Bifrost does not configure another host interface or an ISP router.

## Quick start

Build the binary:

```sh
make build
sudo install -m 0755 bin/bifrost /usr/local/bin/bifrost
```

Create the service account and configuration directory:

```sh
sudo useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin bifrost
sudo install -d -o bifrost -g bifrost -m 0750 /etc/bifrost
sudo -u bifrost /usr/local/bin/bifrost init \
  --interface eth0 \
  --output /etc/bifrost/config.yaml
```

Create the address secret and a Cloudflare token file:

```sh
sudo -u bifrost install -m 0600 /dev/null /etc/bifrost/address-secret
openssl rand -hex 32 | sudo -u bifrost tee /etc/bifrost/address-secret >/dev/null
sudo -u bifrost install -m 0600 /dev/null /etc/bifrost/cloudflare-token
sudoedit /etc/bifrost/cloudflare-token
sudoedit /etc/bifrost/config.yaml
sudo chown bifrost:bifrost /etc/bifrost/address-secret \
  /etc/bifrost/cloudflare-token \
  /etc/bifrost/config.yaml
sudo chmod 0600 /etc/bifrost/address-secret \
  /etc/bifrost/cloudflare-token \
  /etc/bifrost/config.yaml
```

If you do not use Cloudflare, create the credential file that your provider configuration references instead of `cloudflare-token`.

Review the configuration and planned changes:

```sh
sudo -u bifrost /usr/local/bin/bifrost status \
  --offline \
  --config /etc/bifrost/config.yaml
sudo /usr/local/bin/bifrost serve \
  --config /etc/bifrost/config.yaml \
  --dry-run
```

Install the systemd service only after the dry run is correct:

```sh
sudo install -m 0644 deploy/bifrost.service /etc/systemd/system/bifrost.service
sudo systemctl daemon-reload
sudo systemctl enable --now bifrost
sudo /usr/local/bin/bifrost check --config /etc/bifrost/config.yaml
```

Read the [installation guide](https://bifrost.biodun.dev/getting-started/installation/) for release verification, secret creation, container use, and upgrades. The [configuration guide](https://bifrost.biodun.dev/guides/configuration/) describes all fields.

## Docker discovery

Static YAML and Docker labels use the same service model.

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
```

Set `bifrost.network` when a container uses more than one Docker network. Docker socket access gives root-level control of the host. Use a restricted socket proxy when possible.

## Optional IPv4 edge

The edge reads an allowed TLS SNI name and connects to the home AAAA address. It does not end TLS. Non-TLS services use explicit static port maps.

The home listener verifies edge metadata with a shared key. This metadata can preserve the original IPv4 address only when the final backend accepts PROXY protocol v2.

See the [edge guide](https://bifrost.biodun.dev/networking/edge/) before you enable this role.

## Operations

```sh
bifrost status --config /etc/bifrost/config.yaml
bifrost status --config /etc/bifrost/config.yaml --json
sudo bifrost check --config /etc/bifrost/config.yaml
curl --fail http://127.0.0.1:9098/healthz
curl --fail http://127.0.0.1:9098/metrics
```

The local server provides `/healthz`, `/status`, and Prometheus `/metrics`. Logs use JSON by default. Use `--log-format text` for plain text.

## Limits

Bifrost supports TCP in v1. It does not end TLS, issue certificates, authenticate users, proxy UDP or QUIC, change application settings, or bypass vendor licensing.

Use an HTTP CDN proxy or a tunnel when it meets the requirements. Use Bifrost when a direct IPv6 path or a non-HTTP, high-bandwidth service must avoid a full-time relay.

Read the [documentation](https://bifrost.biodun.dev/) and the [security policy](SECURITY.md).

## Development

```sh
make verify
make docs
```

## License

MIT
