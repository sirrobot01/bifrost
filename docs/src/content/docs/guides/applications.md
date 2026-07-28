---
title: Application notes
description: The per-application details that the generic configuration does not cover.
---

Most services need nothing beyond a name, a backend, and a port:

```yaml
static_services:
  - name: jellyfin
    backend: 127.0.0.1:8096
    listen: 443
    dns: media.example.com
```

The one decision that is not automatic: **does the backend already speak TLS?** If it serves plain HTTP, leave the default and Bifrost obtains a certificate for the published name. If it terminates TLS itself, set `tls: off` so the connection passes through as raw TCP. Getting this backwards fails every request — Bifrost would decrypt and then speak plaintext into a TLS listener.

Beyond that, these are the details worth knowing.

## Jellyfin, Emby, and similar

Plain HTTP backends, so the example above is the whole configuration.

Do not add the Bifrost host or a wide subnet to the application's trusted or local networks to make remote clients appear local. It weakens authentication and produces wrong bitrate decisions. Neither accepts PROXY protocol v2, so leave `proxy_protocol` unset.

If the public and backend ports differ, set the published server port in the application to match, or generated links point at the wrong port.

## Immich

Serves plain HTTP on 2283. Point Bifrost at it and the reverse proxy becomes optional. If you keep an existing proxy, publish that listener with `tls: off` instead, and configure Immich's trusted-proxy settings as usual.

## Plex

Two things behave differently.

Plex clients get connection addresses from the Plex control plane, so publishing `plex.example.com` does not by itself route clients through it. Set the custom server access URL in Plex, and keep it aligned with what Bifrost publishes.

Plex serves its own `plex.direct` certificate on 32400, so it needs `tls: off`:

```yaml
static_services:
  - name: plex
    backend: 127.0.0.1:32400
    listen: 32400
    dns: plex.example.com
    tls: off
```

The edge can carry 32400 through a static port map, but Plex's remote-playback rules apply regardless of the network path.

## Client addresses

Splice mode hides the client address: the backend sees the Bifrost host as its peer. This affects local-network detection, bandwidth rules, audit logs, and IP-based controls.

Direct mode preserves it, but requires the backend to own a public IPv6 address and listen on the public port. It also rules out TLS termination, since Bifrost never sees the connection.
