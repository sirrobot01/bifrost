---
title: Plex
description: Understand the limits of Plex publication through Bifrost.
---

Bifrost can improve the network path to Plex. It does not bypass a Plex subscription and does not change Plex settings.

Plex clients get server connection addresses through the Plex control plane. DNS publication of `plex.example.com` does not make all Plex clients use that name.

If you configure a custom server access URL, keep that setting aligned with the current address or hostname. Bifrost does not update this Plex setting in v1.

## Prefer direct mode

Plex normally listens on IPv4 and IPv6. It does not normally accept PROXY protocol.

Use a direct IPv6 address when the network permits it. A direct connection preserves the client address.

In splice mode, Plex sees the Bifrost host as the peer. This can change local-network detection, remote bandwidth rules, audit data, and IP-based controls.

```yaml
static_services:
  - name: plex
    backend: 127.0.0.1:32400
    listen: 32400
    dns: plex.example.com
    mode: splice
    proxy_protocol: false
    edge: false
```

## Use the optional edge

The edge can carry TCP port 32400 from IPv4 through a static port map. Plex must still advertise an address that its clients will use.

Plex remote-playback rules apply independently of the network path.

Treat Plex as an advanced integration in v1. Services that use operator-controlled DNS, such as Jellyfin, Immich, SSH, and WebDAV, match the core Bifrost publication model more directly.
