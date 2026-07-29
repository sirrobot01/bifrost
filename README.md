<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/src/assets/bifrost-lockup-dark.png">
    <source media="(prefers-color-scheme: light)" srcset="docs/src/assets/bifrost-lockup-light.png">
    <img alt="Bifrost" src="docs/src/assets/bifrost-lockup-light.png" width="360">
  </picture>
</p>

Bifrost publishes self-hosted TCP services through native IPv6. It runs on Linux, macOS, FreeBSD, OpenBSD, and Windows hosts that have public IPv6 and use CGNAT for IPv4.

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

- Linux, macOS, FreeBSD, OpenBSD, or Windows 10/Server 2016 and newer
- A global IPv6 `/64` on the Bifrost host
- Permission to add IPv6 addresses and bind service ports
- An inbound IPv6 rule on the home router
- Cloudflare, deSEC, dynv6, or RFC 2136 credentials
- Go 1.26 to build from source

The home role must run on the host that owns the managed service addresses. Bifrost does not configure another host interface or an ISP router. Linux supports advisory and managed nftables modes; macOS, the BSDs, and Windows use advisory firewall mode.

## Quick start

Install the latest release. The script verifies the checksum and installs native services without starting them. On Unix:

```sh
curl -fsSL https://bifrost.biodun.dev/install.sh | sh
```

On Windows, run an Administrator PowerShell:

```powershell
irm https://bifrost.biodun.dev/install.ps1 | iex
```

Check that the host can run Bifrost. This reads no configuration and changes nothing.

```sh
sudo bifrost doctor
```

Every `ERROR` line names the problem, the fix, and a troubleshooting link. Continue once they are gone.

Answer the setup questions. Bifrost creates the configuration, the address secret, and the DNS credential itself, with the right permissions, and reads your zone ID from your DNS account:

```sh
sudo bifrost init --interactive
```

Permit inbound IPv6 on your router to the published port. Bifrost cannot do this, and the name will not answer until it is done.

Review the planned changes, then start:

```sh
sudo bifrost serve --config /etc/bifrost/config.yaml --dry-run
sudo systemctl enable --now bifrost
sudo bifrost check --config /etc/bifrost/config.yaml
```

The [quickstart](https://bifrost.biodun.dev/getting-started/quickstart/) walks through this with a worked example. [Troubleshooting](https://bifrost.biodun.dev/getting-started/troubleshooting/) explains every `doctor` and `check` finding. The [installation guide](https://bifrost.biodun.dev/getting-started/installation/) covers RPM, archives, containers, signature verification, and upgrades.

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
      bifrost.tls: auto
      bifrost.network: media
```

Set `bifrost.network` when a container uses more than one Docker network. Docker socket access gives root-level control of the host. Use a restricted socket proxy when possible.

## Optional IPv4 edge

The edge reads an allowed TLS SNI name and connects to the home AAAA address. It does not end TLS. Non-TLS services use explicit static port maps.

The home listener verifies edge metadata with a shared key. This metadata can preserve the original IPv4 address only when the final backend accepts PROXY protocol v2.

See the [edge guide](https://bifrost.biodun.dev/networking/edge/) before you enable this role.

## Operations

```sh
sudo bifrost doctor
bifrost status --config /etc/bifrost/config.yaml
bifrost status --config /etc/bifrost/config.yaml --json
sudo bifrost check --config /etc/bifrost/config.yaml
curl --fail http://127.0.0.1:9098/healthz
curl --fail http://127.0.0.1:9098/metrics
```

`doctor` judges the host and needs no configuration. `check` judges a configured deployment end to end. Both accept `--json` and exit non-zero on any error finding.

The local server provides `/healthz`, `/status`, and Prometheus `/metrics`. Logs use JSON by default. Use `--log-format text` for plain text.

## Limits

Bifrost supports TCP in v1. Splice services terminate TLS with automatically issued certificates by default; direct services and `tls: off` pass raw TCP through. Bifrost does not authenticate users, proxy UDP or QUIC, change application settings, or bypass vendor licensing.

Use an HTTP CDN proxy or a tunnel when it meets the requirements. Use Bifrost when a direct IPv6 path or a non-HTTP, high-bandwidth service must avoid a full-time relay.

Read the [documentation](https://bifrost.biodun.dev/) and the [security policy](SECURITY.md).

## Development

```sh
make verify
make docs
```

## License

MIT
