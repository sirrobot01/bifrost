# Plex

Bifrost can improve the network path to Plex, but it is not a subscription bypass and it does not rewrite Plex application settings.

Plex clients discover server connection URIs through Plex's own control plane rather than ordinary DNS alone. Publishing `plex.example.com` does not automatically make every Plex client use it. If your deployment relies on a custom server access URL, you remain responsible for keeping that Plex setting aligned with the current address or hostname.

Plex generally binds dual-stack and does not consume PROXY protocol. Prefer a direct IPv6 address when the host topology permits it. In splice mode every client appears to Plex as the Bifrost host, which can affect local-network classification, remote bandwidth policy, auditing, and IP-based controls.

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

The optional edge can carry legacy IPv4 TCP/32400 using a static map, but Plex must still advertise a URI clients will attempt. Application entitlements for remote playback apply independently of whether traffic is direct or relayed.

Treat Plex as an advanced integration for v1. Jellyfin, Immich behind an existing reverse proxy, SSH, WebDAV, and other services that already honor operator-controlled DNS exercise Bifrost's core publication model more directly.
