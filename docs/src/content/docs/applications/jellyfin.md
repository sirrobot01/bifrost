---
title: Jellyfin
description: Publish Jellyfin over direct IPv6 or a Bifrost splice.
---

Use Bifrost for Jellyfin when you want to keep media traffic off a VPS or a third-party tunnel.

## Use a splice

Use this example for an IPv4-only local listener:

```yaml
static_services:
  - name: jellyfin
    backend: 127.0.0.1:8096
    listen: 443
    dns: media.example.com
    mode: splice
    proxy_protocol: false
    edge: false
```

Bifrost does not end TLS. Put a TLS server on the backend port, or publish the Jellyfin HTTPS listener. If the public and backend ports differ, update the published server settings in Jellyfin.

In splice mode, Jellyfin sees the Bifrost host as the peer. Do not mark the Bifrost host or a large subnet as trusted to make remote clients appear local. This change can weaken authentication and produce incorrect bitrate rules.

Jellyfin does not normally accept PROXY protocol v2. Keep `proxy_protocol: false` unless a compatible proxy is the immediate backend.

## Prefer a direct path

Use direct mode when Jellyfin meets all these conditions:

- It owns a public IPv6 address.
- It listens on the public port.
- The host firewall permits the connection.
- The router firewall permits the connection.

Direct mode preserves the client address and keeps Bifrost out of the data path.

## Test media traffic

Run `bifrost check` before you test playback. If login works but high-bitrate video stops, test ICMPv6 Packet Too Big delivery. Do not lower all interface MTUs as the first response.
