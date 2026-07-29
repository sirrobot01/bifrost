---
title: Configure Bifrost
description: Configure the home role, DNS providers, services, Docker, and the edge.
---

Bifrost reads strict YAML: unknown fields and multiple documents are rejected.

Each secret lives in its own regular file, unreadable by group and other.

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

Omit anything with a default, including `owner_id` and `secret_file`. A minimal file is easier to keep correct than a full one.

## Configure the home role

Only the interface, a DNS provider, and its credential are required. Everything else has a default:

```yaml
version: 1
interface: eth0

dns:
  provider: cloudflare
  cloudflare:
    zone_id: ZONE_ID
    api_token_file: /etc/bifrost/cloudflare-token

static_services:
  - name: media
    backend: 127.0.0.1:8096
    listen: 443
    dns: media.example.com
```

### Home fields

| Field | Default | Notes |
|---|---|---|
| `version` | — | Must be `1`. |
| `interface` | — | The interface holding the global IPv6 `/64`. Required. |
| `prefix_override` | automatic | An IPv6 `/64`, when you must pin the selection. |
| `owner_id` | this host's hostname | Identifies the records this installation owns. |
| `secret_file` | `/etc/bifrost/address-secret` | Created by `init`. |
| `settle_window` | `10s` | Delay after a network or Docker change. |
| `drain_grace` | `2m` | Overlap and connection drain during a prefix change. |
| `dns.ttl` | `60s` | Between 60s and 24h. |
| `firewall.mode` | `advisory` | `managed` lets Bifrost own the inbound IPv6 policy; `advisory` only reports on the existing one. |
| `firewall.trusted_interfaces` | none | Managed mode only. Interfaces accepted in full, such as a VPN link. |
| `firewall.allow_ports` | none | Managed mode only. Extra inbound TCP ports on every address. Put SSH here if you administer the host over IPv6. |
| `firewall.pcp` | `false` | Ask the router to open each published socket. Most routers do not answer, and nothing changes when they do not. |
| `probe.endpoint` | none | An HTTPS probe URL. An enabled edge already serves this purpose. |
| `verify.enabled` | `true` | Keep checking external reachability while the daemon runs. |
| `verify.interval` | `5m` | Between 1m and 24h. |
| `notify.webhook` | none | HTTPS URL receiving operational events. |
| `notify.format` | `json` | `json` includes a `content` field, so a Discord webhook works unmodified; `text` suits ntfy. |
| `notify.min_interval` | `30m` | Suppresses a repeat of the same event for the same service. |
| `metrics.listen` | `127.0.0.1:9098` | Must be loopback. |
| `docker.socket` | `/var/run/docker.sock` | Only read when `docker.enabled` is set. |
| `acme.state_dir` | `/var/lib/bifrost` | Where certificates and the account key are kept. |

Do not change `owner_id` after Bifrost creates DNS records: a new ID does not own the old ones. Do not change the address secret unless you want new splice addresses.

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

The DNS TTL must be from 60 seconds through 24 hours.

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
| `wildcard` | Optional. Issue one certificate per parent domain instead of one per name. |

The challenge TXT records use `dns.ttl`, so provider minimums such as deSEC's 3600-second floor hold for challenges too. A service with `tls: off` never touches ACME.

### Wildcard certificates

```yaml
acme:
  wildcard: true
```

`media.example.com` and `photos.example.com` then share one `*.example.com` certificate. Adding a third service under that parent costs no ACME order at all, and renewals are one order instead of one per name — which matters because Let's Encrypt rate-limits per registered domain.

A wildcard matches exactly one label. `*.example.com` covers `media.example.com` but not `cam.house.example.com`, which gets its own `*.house.example.com`. A two-label name such as `example.com` keeps its own certificate, since `*.com` is not issuable.

Only enable this when the parent is a zone you control. It is off by default because that is not true of every name — a shared suffix would produce a wildcard no CA will sign.

Existing per-name certificates stay on disk and are ignored. Switching back is equally safe.

Requesting a certificate accepts the certificate authority's subscriber agreement on your behalf, as every ACME client does. No account setup is needed: Bifrost creates and stores the ACME account key itself. Let's Encrypt rate-limits issuance per domain; when testing repeatedly, set `directory` to the [staging environment](https://letsencrypt.org/docs/staging-environment/) first.

## Configure a static service

Three fields are required. This publishes an IPv4-only backend, terminating TLS on 443:

```yaml
static_services:
  - name: photos
    backend: 127.0.0.1:2283
    listen: 443
    dns: photos.example.com
```

### Service fields

| Field | Default | Notes |
|---|---|---|
| `name` | — | A unique, stable service ID. Required. |
| `backend` | — | IP address and TCP port. Not a host name. Required. |
| `dns` | — | The public DNS name. Required. |
| `listen` | the backend port | The public TCP port. |
| `mode` | `auto` | `direct`, `splice`, or `auto`. |
| `tls` | `auto` | Terminate TLS with an automatic certificate; `off` for a backend that speaks TLS itself. Direct mode ignores it. |
| `public_address` | observed address | The backend IPv6 address, when direct mode needs an explicit one. |
| `proxy_protocol` | `false` | Only when the backend accepts PROXY v2. |
| `edge` | `false` | Only with a configured IPv4 edge. Such services use splice mode automatically. |

### Direct mode

No Bifrost listener: the backend owns the public IPv6 address and its port equals the public port. Bifrost publishes DNS and stays out of the data path, so it cannot terminate TLS.

Set an explicit address when the backend listens on only one:

```yaml
static_services:
  - name: web
    backend: "[2001:db8:1234:1::20]:443"
    public_address: 2001:db8:1234:1::20
    listen: 443
    dns: web.example.com
    mode: direct
```

### Splice mode

Bifrost adds a stable service address to the host, listens on it, and opens a new connection to the backend. The backend therefore sees the Bifrost host as its peer, not the client. Enable `proxy_protocol` only when the backend explicitly accepts PROXY v2.

### Auto mode

Auto mode selects direct mode only when every direct condition holds, and splice mode otherwise. A service with `edge: true` is always spliced, since the edge has to reach a Bifrost listener. Check the selected mode with `bifrost status`.

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
      bifrost.tls: auto
      bifrost.network: media
      bifrost.proxy_protocol: "false"
      bifrost.edge: "false"
```

`bifrost.tls` matches the `tls` field on a static service: `auto` terminates TLS with an automatic certificate, `off` passes raw TCP to a backend that speaks TLS itself. Omitting it means `auto`.

Set `bifrost.network` when the container has more than one Docker network. You can set `bifrost.backend` to an explicit IP address and port. This value overrides Docker address discovery.

Containers are listed at startup and every 30 seconds; Docker events trigger an earlier update.

The Docker socket grants root-level control of the host, and a read-only file mount does not make the API read-only.

## Configure edge publication

`bifrost edge invite` on the home host and `bifrost edge join <token>` on the edge host write both sides of this, including the shared key. Enrolling that way is preferred; the fields below are what it produces.

In the home configuration:

```yaml
edge:
  enabled: true
  ipv4_addresses:
    - 198.51.100.20
  key_file: /etc/bifrost/edge-key
```

`ipv4_address` is the older singular spelling and still works. List every edge that fronts these services: each one publishes an A record for the name, and `check` verifies all of them.

Then enable it per service:

```yaml
static_services:
  - name: media
    backend: 127.0.0.1:8096
    listen: 443
    dns: media.example.com
    edge: true
```

Mode is coerced to `splice`, because the edge has to reach a Bifrost listener; `mode: direct` is rejected. See [the edge guide](../../networking/edge/).

### Configure the edge host

`edge join` writes this file. Its shape, for reference:

```yaml
version: 1
listen: :443
allow:
  - media.example.com
  - photos.example.com
key_file: /etc/bifrost/edge-key
handshake_timeout: 5s
idle_timeout: 5m
max_connections: 1024
rate_per_minute: 120
rate_burst: 20
negative_ttl: 15s
stale_on_error: 30s
static_maps:
  "2222": ssh.example.com:22
```

| Field | Notes |
|---|---|
| `version` | Must be `1`. |
| `listen` | The TLS routing address and port. |
| `allow` | Each permitted TLS SNI name. |
| `key_file` | Path to the shared edge key. |
| `handshake_timeout` | Limit on reading a TLS ClientHello. |
| `idle_timeout` | Close an inactive connection after this. |
| `max_connections` | Cap on active edge connections. |
| `rate_per_minute` | Sustained connection rate for one source. |
| `rate_burst` | Permitted burst for one source. |
| `negative_ttl` | Cache empty or failed AAAA lookups for this. |
| `stale_on_error` | Reuse a previous address for this long after a DNS error. |
| `static_maps` | Map an edge port to one DNS name and home port. |

Each `allow` entry must be a DNS name. Each static map target must contain a DNS name and port. A static map port must not equal the TLS listener port.

## Apply a change without a restart

```sh
sudo systemctl reload bifrost
```

A restart withdraws every DNS record and drops every connection before it starts again. A reload re-reads the file and reconciles the difference, so adding a service leaves the others untouched.

Reloadable: `static_services`, `dns.ttl`, `settle_window`, `drain_grace`, `verify.interval`, and the `firewall` allowances (`allow_ports`, `trusted_interfaces`).

Everything else was consumed while building the daemon — the interface, the address secret, the DNS credentials, `metrics.listen`, `docker`, `acme`, `edge`, `notify`, `probe.endpoint`, `firewall.mode`, and `firewall.pcp` — and needs a restart. A reload that touches one of them is rejected whole and names the fields, rather than applying half of it.

A file that fails to parse or validate is also rejected, and the running configuration keeps serving. Check the log after reloading:

```sh
journalctl -u bifrost -n 20
```

Without systemd, send `SIGHUP` to the process directly.

## Watch a running deployment

`check` answers whether services are reachable once, while you are setting the host up. The daemon keeps asking:

```yaml
verify:
  interval: 5m

notify:
  webhook: https://ntfy.sh/your-topic
  format: text
```

With an [edge](../../networking/edge/) or a `probe.endpoint`, each service is probed from outside every `verify.interval`. The result appears as `bifrost_external_reachable{service}` and in `/status`. Without either, no verdict is exported at all — an absent metric rather than an optimistic one.

Only transitions are reported. A service that has been reachable for a month produces nothing; one that stops answering produces a single event, and one event again when it recovers. `notify.min_interval` bounds repeats, and recovery is never suppressed by the outage that preceded it.

Events sent: external reachability lost and restored, certificate renewal failure, reconciliation failure, and prefix change. The prefix one matters because it explains whatever follows it.

:::caution
Event details carry error text from DNS providers and the certificate authority. Send them somewhere you control.
:::

Set `verify.enabled: false` to turn the checks off. They cost one TCP connection and TLS handshake per service per interval, made from your own edge.

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

`check` ends by saying whether anything outside the network confirmed reachability. Add `--require-external` to exit non-zero when nothing did.

Query the running daemon instead of deriving state locally by dropping `--offline` from `bifrost status`; it reads `/status` on `metrics.listen`.
