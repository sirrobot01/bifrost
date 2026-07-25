# Authenticated edge metadata protocol

The edge sends one authenticated PROXY protocol v2 header immediately after opening the home connection and before replaying client bytes. The home listener accepts an ordinary direct client when the PROXY v2 signature is absent. When the signature is present, a malformed or unauthenticated header is rejected rather than passed to the backend.

The fixed 87-byte frame is:

| Offset | Size | Value |
|---:|---:|---|
| 0 | 12 | PROXY v2 signature |
| 12 | 1 | version/command `0x21` |
| 13 | 1 | family/protocol `0x11` (IPv4/TCP) |
| 14 | 2 | big-endian payload length `71` |
| 16 | 4 | original IPv4 source |
| 20 | 4 | edge listener IPv4 destination |
| 24 | 2 | original source port |
| 26 | 2 | edge listener port |
| 28 | 1 | private TLV type `0xe0` |
| 29 | 2 | TLV length `56` |
| 31 | 8 | Unix timestamp, big-endian seconds |
| 39 | 16 | cryptographic random nonce |
| 55 | 32 | HMAC-SHA-256 |

The MAC input is the context string `bifrost-edge-proxy-v1` plus a zero byte, the four-byte service-identity length, the service identity, and frame bytes 0–54. The service identity is the normalized DNS name configured at both ends. This prevents a valid header for one service from being replayed to another.

The home accepts timestamps only within `max_clock_skew`, verifies the MAC in constant time, and retains each nonce through the header's complete acceptance window. Keep home and edge clocks synchronized. Keys must contain at least 32 bytes and use file mode `0600`.

This protocol authenticates the Bifrost edge to the home listener and protects metadata integrity/freshness. It does not encrypt traffic; application TLS remains responsible for confidentiality and server authentication.
