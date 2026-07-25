# Jellyfin

Jellyfin is a good Bifrost fit when remote media traffic would otherwise traverse a VPS or third-party tunnel.

For an IPv4-only local Jellyfin listener:

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

Bifrost does not terminate TLS. Put a TLS-capable server on the backend port or publish Jellyfin's HTTPS listener. If the public port differs from the backend, account for it in Jellyfin's published-server configuration.

In splice mode Jellyfin sees the Bifrost host as the peer. Do not mark the Bifrost host or an overly broad subnet as trusted merely to make remote clients appear local; that can weaken authentication and distort remote bitrate policy. Jellyfin does not normally consume PROXY v2 directly, so leave `proxy_protocol` false unless a compatible proxy is the immediate backend.

Direct mode is preferable when Jellyfin owns a public IPv6 address, listens on the public port, and both host and router permit it. That preserves client addresses and removes Bifrost from the data path.

Before testing playback, run `bifrost check`. A TLS login that works while high-bitrate video stalls is a strong PMTU warning; verify ICMPv6 Packet Too Big rather than blindly lowering every interface MTU.
