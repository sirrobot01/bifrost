# Decision guide

Use Bifrost when all of these are true:

- the home connection has globally routed IPv6;
- IPv4 is behind CGNAT or otherwise unsuitable for inbound access;
- clients can use an ordinary DNS name with no companion software;
- direct bandwidth, latency, or non-HTTP TCP support matters;
- you are willing to expose and secure the application itself.

Use a tunnel or overlay instead when there is no usable inbound IPv6, only enrolled users need access, or identity-aware private access matters more than transparent public reachability. A self-hosted VPS tunnel is also a better fit when the ISP router cannot open an IPv6 pinhole.

Use an HTTP CDN/reverse proxy when every service is compatible HTTP, its terms and upload/streaming limits fit, and hiding the origin or adding edge security is more valuable than a direct path. Bifrost deliberately does not recreate TLS termination, certificates, authentication, or WAF features.

Use ordinary DDNS when one host address and one record are enough and you do not need per-service addresses, prefix-overlap drains, ownership-safe reconciliation, Docker discovery, IPv4-backend splicing, or PMTU/firewall diagnostics.

The optional Bifrost edge is most useful for non-HTTP TCP or deployments where IPv4 clients need compatibility but IPv6 clients should avoid a VPS hairpin. It is not mandatory for an IPv6-only audience and can be added later without changing the native AAAA path.
