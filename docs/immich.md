# Immich

Immich commonly sits behind an HTTP reverse proxy that already provides TLS. Bifrost can publish that proxy over native IPv6 while leaving application routing and certificates where they are.

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

Set `proxy_protocol: true` only when the immediate reverse-proxy listener on port 8443 is configured for PROXY v2. Immich itself should continue receiving ordinary HTTP forwarding headers from that proxy. If the proxy does not consume PROXY v2, leave the setting false or connections will fail before TLS.

Direct mode is possible when the reverse proxy owns a public IPv6 address and listens on the same public port. Bifrost then publishes the observed address and does not handle traffic.

Bifrost does not add Immich's domain, rewrite trusted proxy settings, or create certificates. Configure those in the proxy and application using their normal mechanisms. For an HTTP-only deployment already served adequately by a CDN proxy, Bifrost may add little value; its advantage is retaining the direct IPv6 path and avoiding a full-time traffic relay.
