---
title: Choose a deployment
description: Decide if Bifrost matches your network and service.
---

## Use Bifrost

Use Bifrost when all these statements are true:

- Your home connection has global IPv6.
- IPv4 inbound access does not work or uses CGNAT.
- Clients must connect without companion software.
- You control the DNS name.
- You can secure the published application.
- You want to avoid a relay for most traffic.

## Use a tunnel or VPN

Use a tunnel or VPN in these cases:

- Your ISP does not provide usable IPv6.
- Your router cannot permit inbound IPv6.
- Only approved users need access.
- You need identity checks before traffic reaches the application.
- You do not want to publish the application to the internet.

A VPS tunnel sends traffic through the VPS. Bifrost does not send native IPv6 traffic through a VPS.

## Use an HTTP proxy

Use an HTTP CDN or reverse proxy when all services use HTTP and the proxy limits meet your needs.

An HTTP proxy can provide TLS, certificates, authentication, and web filtering. Bifrost does not provide these functions.

Bifrost is useful for non-HTTP TCP services. It is also useful when direct bandwidth or latency is important.

## Use a basic DDNS client

Use a basic DDNS client when one host address and one DNS record are sufficient.

Use Bifrost when you also need one or more of these functions:

- A separate IPv6 address for each service
- DNS overlap during a prefix change
- DNS ownership checks
- Docker service discovery
- IPv6-to-IPv4 TCP splice mode
- Firewall and PMTU checks

## Decide if you need the edge

Do not use the edge when all clients have IPv6.

Use the edge when IPv4-only clients need access. The edge is most useful for non-HTTP TCP services. The edge only carries IPv4-client traffic.

You can add the edge later. The home AAAA path does not depend on it.
