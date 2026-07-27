---
title: Configure Bifrost
description: Configure the home role, DNS providers, services, Docker, and the edge.
---

Bifrost reads strict YAML. Bifrost rejects unknown fields. Bifrost also rejects more than one YAML document.

Store each secret in a separate file. The file must be a regular file. Group and other users must not have access to it.

## Create the files by hand

`bifrost init --interactive` creates the configuration, the address secret, and the DNS credential with correct permissions. Prefer it.

To write the files yourself, for example from a configuration manager, generate a template and fill it in:

```sh
sudo -u bifrost bifrost init --output /etc/bifrost/config.yaml
```

That template is not runnable. It has no service, no credential, no address secret, and a placeholder zone ID. You must also:

```sh
openssl rand -hex 32 | sudo -u bifrost tee /etc/bifrost/address-secret >/dev/null
sudo -u bifrost install -m 0600 /dev/null /etc/bifrost/cloudflare-token
sudoedit /etc/bifrost/cloudflare-token
sudoedit /etc/bifrost/config.yaml
sudo chmod 0600 /etc/bifrost/config.yaml /etc/bifrost/address-secret /etc/bifrost/cloudflare-token
```

Bifrost refuses any secret file readable by group or other. Review with `bifrost serve --dry-run` before the first start.

## Configure the home role

Start with this file:

```yaml
version: 1
interface: eth0
owner_id: home-1
secret_file: /etc/bifrost/address-secret
settle_window: 10s
drain_grace: 2m

dns:
  provider: cloudflare
  ttl: 60s
  cloudflare:
    zone_id: CHANGE_ME
    api_token_file: /etc/bifrost/cloudflare-token

firewall:
  mode: advisory

probe: {}

metrics:
  listen: 127.0.0.1:9098

docker:
  enabled: false
  socket: /var/run/docker.sock

edge:
  enabled: false
  max_clock_skew: 30s
  header_timeout: 250ms

static_services: []
```

### Home fields

| Field | Requirement |
|---|---|
| `version` | Use `1`. |
| `interface` | Set the interface that has the global IPv6 `/64`. |
| `prefix_override` | Set an IPv6 `/64` only when you must select one prefix. |
| `owner_id` | Use one stable ID for this Bifrost installation. |
| `secret_file` | Set the path to the address secret. |
| `settle_window` | Set the delay after a network or Docker change. |
| `drain_grace` | Set the overlap and connection drain time. |
| `firewall.mode` | Use `advisory` in v1. |
| `probe.endpoint` | Set an HTTPS probe URL only when you trust that service. |
| `metrics.listen` | Use a loopback IP address and port. |

Do not change `owner_id` after Bifrost creates DNS records. A new ID does not own the old records.

Do not change the address secret unless you want new splice addresses.

### Prefix selection

Bifrost selects an eligible global `/64`. It ignores temporary and deprecated addresses. It logs the selected prefix. It also logs each ignored prefix.

Set `prefix_override` when the automatic selection is not correct:

```yaml
prefix_override: 2001:db8:1234:1::/64
```

Replace the example prefix with a real prefix on the configured interface.

## Configure a DNS provider

Bifrost supports Cloudflare, deSEC, dynv6, and RFC 2136.

If you are unsure which to use: with a domain already on Cloudflare, use Cloudflare. With no domain at all, deSEC and dynv6 both give you a free name (`yourname.dedyn.io`, `yourname.dynv6.net`); deSEC enforces a 3600-second minimum TTL on new domains, dynv6 does not. With your own authoritative server such as BIND, Knot, or PowerDNS, use RFC 2136. Any provider not listed here can still work if it serves your zone from an RFC 2136 server, and `init --interactive` discovers the correct zone from your account for everything except RFC 2136.

The DNS TTL must be from 60 seconds through 24 hours. The value must use whole seconds.

### Cloudflare

```yaml
dns:
  provider: cloudflare
  ttl: 60s
  cloudflare:
    zone_id: ZONE_ID
    api_token_file: /etc/bifrost/cloudflare-token
```

Limit the token to the required zone.

### deSEC

```yaml
dns:
  provider: desec
  ttl: 3600s
  desec:
    zone: example.com
    token_file: /etc/bifrost/desec-token
```

deSEC enforces a minimum record TTL of 3600 seconds on new domains, so `ttl` must be at least `3600s` there. deSEC support lowers the domain minimum on request for dynamic-address use; after that, a shorter `ttl` such as `60s` recovers faster from a prefix change.

For a dedyn.io name such as `service.example.dedyn.io`, the zone is `example.dedyn.io`, not `dedyn.io`.

### dynv6

```yaml
dns:
  provider: dynv6
  ttl: 60s
  dynv6:
    zone: example.com
    token_file: /etc/bifrost/dynv6-token
```

### RFC 2136

```yaml
dns:
  provider: rfc2136
  ttl: 60s
  rfc2136:
    server: 192.0.2.53:53
    zone: example.com
    key_name: bifrost-key
    key_file: /etc/bifrost/rfc2136-key
    algorithm: hmac-sha256
```

`server` must include a port. Configure both `key_name` and `key_file`, or omit both fields.

## Configure automatic certificates

Splice services terminate TLS by default. The certificate for each published name is obtained and renewed through ACME DNS-01 challenges, using the same DNS provider credentials configured above, so no additional accounts or ports are needed. Renewal runs 30 days before expiry.

```yaml
acme:
  email: you@example.com
  state_dir: /var/lib/bifrost
```

| Field | Requirement |
|---|---|
| `email` | Optional. Receives expiry warnings from the certificate authority. |
| `directory` | Optional ACME directory URL; empty means Let's Encrypt. Use the Let's Encrypt staging URL to test without rate limits. |
| `state_dir` | Holds the ACME account key and issued certificates. The packages create `/var/lib/bifrost` through systemd. |

The challenge TXT records use `dns.ttl`, so provider minimums such as deSEC's 3600-second floor hold for challenges too. A service with `tls: off` never touches ACME.

Requesting a certificate accepts the certificate authority's subscriber agreement on your behalf, as every ACME client does. No account setup is needed: Bifrost creates and stores the ACME account key itself. Let's Encrypt rate-limits issuance per domain; when testing repeatedly, set `directory` to the [staging environment](https://letsencrypt.org/docs/staging-environment/) first.

## Configure a static service

This example publishes an IPv4-only backend through splice mode:

```yaml
static_services:
  - name: photos
    backend: 127.0.0.1:2283
    listen: 443
    dns: photos.example.com
    mode: splice
    proxy_protocol: false
    edge: false
```

### Service fields

| Field | Requirement |
|---|---|
| `name` | Use a unique and stable service ID. |
| `backend` | Set an IP address and TCP port. Do not set a host name. |
| `listen` | Set the public TCP port. |
| `dns` | Set the public DNS name. |
| `mode` | Use `direct`, `splice`, or `auto`. |
| `tls` | Use `auto` (the default) to terminate TLS with an automatic certificate on the splice listener, or `off` for a backend that speaks TLS itself. Direct mode ignores it. |
| `public_address` | Set the backend IPv6 address when direct mode needs an explicit address. |
| `proxy_protocol` | Enable this only when the backend accepts PROXY v2. |
| `edge` | Enable this only for a configured IPv4 edge. |

### Direct mode

Direct mode has no Bifrost TCP listener. The backend must own the public IPv6 address. The backend port must equal the public port.

Use an explicit address when the backend listens on one address:

```yaml
static_services:
  - name: web
    backend: "[2001:db8:1234:1::20]:443"
    public_address: 2001:db8:1234:1::20
    listen: 443
    dns: web.example.com
    mode: direct
```

Replace the documentation address with a real address on the Bifrost host.

### Splice mode

Splice mode adds a stable service address to the Bifrost host. Bifrost listens on that address. Bifrost then opens a new TCP connection to the backend.

The backend does not receive the original client address. It receives the Bifrost host address. Enable `proxy_protocol` only when the backend explicitly accepts PROXY v2.

### Auto mode

Auto mode selects direct mode only when all direct checks pass. Auto mode uses splice mode for all other cases. Check the selected mode with `bifrost status`.

## Configure Docker discovery

Enable Docker discovery:

```yaml
docker:
  enabled: true
  socket: /var/run/docker.sock
```

Add labels to a container:

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
      bifrost.proxy_protocol: "false"
      bifrost.edge: "false"
```

Set `bifrost.network` when the container has more than one Docker network. You can set `bifrost.backend` to an explicit IP address and port. This value overrides Docker address discovery.

Bifrost lists running containers when it starts. It also lists them every 30 seconds. Docker events cause an earlier update.

The Docker socket gives root-level control of the host. A read-only file mount does not make the Docker API read-only.

## Configure edge publication

Enable the edge in the home configuration:

```yaml
edge:
  enabled: true
  ipv4_address: 198.51.100.20
  key_file: /etc/bifrost/edge-key
  max_clock_skew: 30s
  header_timeout: 250ms
```

Replace the documentation address with the public IPv4 address of the edge server.

Enable the edge for a service:

```yaml
static_services:
  - name: media
    backend: 127.0.0.1:8096
    listen: 443
    dns: media.example.com
    mode: splice
    proxy_protocol: false
    edge: true
```

An edge-enabled service must use splice mode in v1. See [the edge guide](../../networking/edge/) for the edge server configuration.

### Configure the edge host

The edge host uses a separate configuration file:

```yaml
version: 1
listen: :443
allow:
  - media.example.com
  - photos.example.com
key_file: /etc/bifrost/edge-key
handshake_timeout: 5s
idle_timeout: 5m
max_connections: 4096
rate_per_minute: 120
rate_burst: 20
negative_ttl: 15s
stale_on_error: 30s
static_maps:
  "2222": ssh.example.com:22
```

| Field | Requirement |
|---|---|
| `version` | Use `1`. |
| `listen` | Set the TLS routing address and port. |
| `allow` | List each permitted TLS SNI name. |
| `key_file` | Set the path to the shared edge key. |
| `handshake_timeout` | Limit the time to read a TLS ClientHello. |
| `idle_timeout` | Close an inactive connection after this time. |
| `max_connections` | Limit all active edge connections. |
| `rate_per_minute` | Set the sustained connection rate for one source. |
| `rate_burst` | Set the permitted connection burst for one source. |
| `negative_ttl` | Cache failed or empty AAAA lookups for this time. |
| `stale_on_error` | Permit a previous address for this time after a DNS error. |
| `static_maps` | Map an edge port to one DNS name and home port. |

Each `allow` entry must be a DNS name. Each static map target must contain a DNS name and port. A static map port must not equal the TLS listener port.

## Check the configuration

Show the derived state without contacting the running daemon:

```sh
bifrost status --offline --config /etc/bifrost/config.yaml
```

Show the planned changes:

```sh
sudo bifrost serve --config /etc/bifrost/config.yaml --dry-run
```

Check the running service:

```sh
sudo bifrost check --config /etc/bifrost/config.yaml
```
