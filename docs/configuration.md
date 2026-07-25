# Configuration

Bifrost uses strict YAML. Unknown keys and multiple YAML documents are rejected, secrets are read from separate regular files, and secret files must not be accessible by group or other users.

## Home configuration

| Field | Meaning |
|---|---|
| `interface` | Interface whose on-link global IPv6 /64 is used |
| `prefix_override` | Optional explicit /64 when several eligible prefixes exist |
| `owner_id` | Stable identity stored in `_bifrost.<name>` TXT markers |
| `secret_file` | Stable secret used to derive splice service IIDs |
| `settle_window` | Debounce period for netlink and Docker changes |
| `drain_grace` | DNS/listener overlap allowed during renumbering and shutdown |
| `dns.ttl` | Whole seconds from 60 seconds through 24 hours |
| `firewall.mode` | `advisory` in v1 |
| `probe.endpoint` | Optional explicit HTTPS external-probe endpoint |
| `metrics.listen` | Loopback IP and port; defaults to `127.0.0.1:9098` |
| `docker.socket` | Docker-compatible Unix socket |

`owner_id` and `secret_file` must remain stable across restarts and upgrades. Changing the owner ID makes existing records appear foreign. Changing the address secret assigns new splice addresses.

### DNS providers

Cloudflare:

```yaml
dns:
  provider: cloudflare
  ttl: 60s
  cloudflare:
    zone_id: ZONE_ID
    api_token_file: /etc/bifrost/cloudflare-token
```

deSEC and dynv6 use `zone` plus `token_file` under `desec` or `dynv6`. RFC 2136 uses `server`, `zone`, and optional paired `key_name`/`key_file`; `algorithm` accepts the TSIG algorithms supported by the DNS library. Use credentials limited to the intended zone whenever the provider supports that boundary.

### Static services

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

`backend` is an IP literal and port, not a hostname. `direct` requires an IPv6 backend, the same backend/public port, and either an observed address or an explicit matching `public_address`. `splice` lets the public and backend ports differ. `auto` selects direct only when the declared and observed facts prove it safe; otherwise it reports and uses splice.

Set `proxy_protocol: true` only if the final backend explicitly accepts PROXY protocol v2 on that listener. Enabling it against Plex, Jellyfin, or an ordinary web server port usually breaks the connection.

### Edge publication

```yaml
edge:
  enabled: true
  ipv4_address: 198.51.100.20
  key_file: /etc/bifrost/edge-key
  max_clock_skew: 30s
  header_timeout: 250ms

static_services:
  - name: media
    backend: 127.0.0.1:8096
    listen: 443
    dns: media.example.com
    mode: splice
    edge: true
```

Replace the documentation address with the VPS's actual public IPv4 address. Edge-enabled services must use splice in v1. A native IPv6 client and the edge share the same home listener; native traffic is passed through, while a correctly authenticated edge header is consumed.

## Docker labels

Supported labels are `bifrost.enable`, `bifrost.name`, `bifrost.port`, `bifrost.listen`, `bifrost.dns`, `bifrost.mode`, `bifrost.network`, `bifrost.backend`, `bifrost.proxy_protocol`, and `bifrost.edge`.

Container discovery is level-triggered: Bifrost lists running containers at startup and every 30 seconds in addition to consuming events. An explicit network is required when a container has more than one. An explicit backend overrides discovered addresses.

## Selection and lifecycle

Without `prefix_override`, Bifrost selects deterministically from eligible on-link global /64 candidates and logs the selected and ignored prefixes. Temporary and deprecated addresses are excluded. Local addresses are observed; Bifrost does not reuse the suffix of an RFC 7217 address after a prefix change.

For splice mode, Bifrost derives its own stable IID, adds the new address, waits for Duplicate Address Detection, binds the listener, then publishes DNS. During overlap it keeps old and new AAAA records until the drain deadline. If an ISP removes the old prefix immediately, continuity cannot be guaranteed.
