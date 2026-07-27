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
- `CAP_NET_ADMIN` permits network changes in the current namespace. Use the hardened systemd unit or a restricted container.
- An external probe receives each tested public address and port. Configure a probe only when you accept its privacy policy.

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

Calls to DNS providers, Docker, and a configured external probe are required product traffic. Logs remain on the host unless the operator forwards them.

## Deploy safely

- Keep management endpoints and metrics on loopback or a private network.
- Open only the required service addresses and ports.
- Permit essential ICMPv6 errors, especially Packet Too Big.
- Run the home and edge roles as separate system users.
- Apply security updates to the host and applications.
- Review `serve --dry-run` before the first reconciliation and after configuration changes.
- Monitor `/healthz`, reconciliation errors, DNS drift, and rejected connections.
