---
title: Immich
description: Publish an Immich reverse proxy over IPv6.
---

Immich often runs behind an HTTP reverse proxy that provides TLS. Bifrost can publish that proxy over IPv6. The existing proxy continues to route requests and manage certificates.

```yaml
static_services:
  - name: immich
    backend: 127.0.0.1:8443
    listen: 443
    dns: photos.example.com
    mode: splice
    proxy_protocol: true
    edge: false
```

Set `proxy_protocol: true` only when the immediate listener on port 8443 accepts PROXY protocol v2. Immich must continue to receive normal HTTP forwarding headers from the reverse proxy.

If the immediate listener does not accept PROXY protocol v2, set this field to `false`. Otherwise, the connection fails before TLS starts.

Use direct mode when the reverse proxy owns a public IPv6 address and listens on the public port. In direct mode, Bifrost publishes the observed address and does not handle traffic.

Bifrost does not perform these tasks:

- Add the domain to Immich.
- Change trusted proxy settings.
- Create or renew certificates.

Configure these items in Immich and the reverse proxy.

If a CDN proxy already meets the requirements for an HTTP-only deployment, Bifrost can add little value. Use Bifrost when you need a direct IPv6 path or want to avoid a full-time traffic relay.
