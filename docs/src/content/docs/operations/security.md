---
title: Security model
description: Trust boundaries and safe deployment requirements.
---

Bifrost publishes applications to the internet. It terminates TLS for splice services and manages their certificates, but it is not an authentication service, web application firewall, or VPN. Certificate private keys and the ACME account key live in `acme.state_dir` (default `/var/lib/bifrost`), readable only by the service account.

Every published backend must provide its own authentication, updates, and rate controls.

## Protect credentials and privileges

- A DNS credential can change records in its scope. Limit it to the required zone. Store it in a file with mode `0600`.
- The address secret creates stable splice addresses. Its value does not grant DNS access. Rotation changes the generated addresses.
- The edge key authenticates edge metadata. Do not reuse the DNS credential or address secret as the edge key.
- Access to the Docker socket is equivalent to root access. A read-only filesystem mount does not limit Docker API methods.
- Linux `CAP_NET_ADMIN` and root services on macOS or BSD can change host networking. Use the supplied service definition and keep configuration ownership narrow.
- An HTTPS probe endpoint receives each tested public address, port, and published DNS name. Configure one only when you accept its privacy policy. An enabled edge acts as the prober instead, which keeps the question inside infrastructure you run.

## Keep DNS ownership separate

Bifrost writes `bifrost-owner=<owner_id>` to `_bifrost.<service-name>` TXT records. It refuses to create, replace, or delete address records without its matching marker.

During removal, Bifrost deletes owned A and AAAA records before it deletes the marker. Do not use one owner ID for separate installations that have different administrators.

`bifrost check` reads provider state. It reports a mismatch in the marker, address set, TTL, or proxy state. It does not change provider state.

## Select the data path

Direct mode keeps Bifrost out of the connection. The backend receives the client address.

Splice mode creates a new backend connection. The backend receives the Bifrost address unless it accepts PROXY protocol v2.

The optional edge authenticates client metadata. The final backend still receives the home Bifrost address unless final PROXY protocol output is enabled.

The edge allowlist limits server-side request forgery. The edge accepts only configured names and global IPv6 destinations. It limits DNS cache time, ClientHello size, connection count, request rate, and idle time. Static port maps contain explicit destinations that clients cannot select.

## Understand telemetry

Bifrost has no analytics, crash reporting, or update checks. It does not use an external service by default.

Calls to DNS providers, the ACME certificate authority, Docker, and any configured external probe are required product traffic. Logs remain on the host unless you forward them.

## Deploy safely

- Keep management endpoints and metrics on loopback or a private network.
- Open only the required service addresses and ports.
- Permit essential ICMPv6 errors, especially Packet Too Big.
- Run the home and edge roles as separate system users.
- Apply security updates to the host and applications.
- Review `serve --dry-run` before the first reconciliation and after configuration changes.
- Monitor the metrics listener, especially certificate expiry and rejected connections.

## Metrics

`metrics.listen` (default `127.0.0.1:9098`) serves `/healthz`, `/status`, and Prometheus metrics at `/metrics`. It must bind loopback.

| Metric | Meaning |
|---|---|
| `bifrost_ready` | 1 once reconciliation has succeeded. |
| `bifrost_services` | Published services. |
| `bifrost_certificate_expiry_seconds` | Per name, expiry as a Unix timestamp. |
| `bifrost_external_reachable` | Per service, 1 when it answered from outside at the last check. Absent when nothing can look from outside. |
| `bifrost_external_checked_seconds` | Per service, when that check last ran. |
| `bifrost_connections_active` | Connections currently spliced. |
| `bifrost_connections_accepted_total` | Accepted connections. |
| `bifrost_connections_rejected_total` | Rejected connections. |
| `bifrost_backend_dial_failures_total` | Failures dialling a backend. |

Alert on `bifrost_external_reachable` and `bifrost_certificate_expiry_seconds`, or configure `notify.webhook` and skip the metrics stack entirely.

Alerting on `bifrost_certificate_expiry_seconds`: Renewal starts 30 days out, so a value inside two weeks means renewals have been failing silently.
