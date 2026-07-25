---
title: Edge metadata protocol
description: Authenticated client metadata between the edge and the home listener.
---

The edge sends one authenticated PROXY protocol v2 header after it opens the home connection. It sends the header before it sends client data.

The home listener accepts a direct client when the PROXY v2 signature is absent. It rejects a malformed or unauthenticated header when the signature is present.

## Frame format

The frame has a fixed size of 87 bytes.

| Offset | Size | Value |
| ---: | ---: | --- |
| 0 | 12 | PROXY v2 signature |
| 12 | 1 | Version and command `0x21` |
| 13 | 1 | IPv4 and TCP family `0x11` |
| 14 | 2 | Big-endian payload length `71` |
| 16 | 4 | Original IPv4 source |
| 20 | 4 | Edge listener IPv4 destination |
| 24 | 2 | Original source port |
| 26 | 2 | Edge listener port |
| 28 | 1 | Private TLV type `0xe0` |
| 29 | 2 | TLV length `56` |
| 31 | 8 | Unix timestamp in big-endian seconds |
| 39 | 16 | Cryptographic random nonce |
| 55 | 32 | HMAC-SHA-256 |

## MAC input

The MAC covers these values in order:

1. The context string `bifrost-edge-proxy-v1`.
2. One zero byte.
3. The four-byte service identity length.
4. The service identity.
5. Frame bytes 0 through 54.

The service identity is the normalized DNS name that is configured on both hosts. This binding prevents the use of a valid header for a different service.

## Validation

The home listener:

- Accepts timestamps only within `max_clock_skew`.
- Compares the MAC in constant time.
- Stores each nonce through the full acceptance window.
- Rejects a nonce that it has already seen.

Keep the clocks on both hosts synchronized. Use a key with at least 32 bytes. Store the key in a regular file with mode `0600`.

This protocol authenticates the edge and protects the metadata from replay or modification. It does not encrypt application traffic. Use application TLS for confidentiality and server authentication.
