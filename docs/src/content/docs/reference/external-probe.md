---
title: External probe contract
description: How reachability is confirmed from outside the host, and what a probe sees.
---

A host cannot prove external reachability from inside its own network. Every local check is equally consistent with a router that drops all inbound traffic, so `check` states plainly when nothing outside has confirmed the path, and `--require-external` turns that into a non-zero exit.

There are two ways to get outside evidence.

## The edge as prober

With `edge.enabled` set, `check` reaches every edge-enabled service through every configured edge automatically. The edges already sit outside the network and do not terminate TLS, so each opens a fresh inbound connection to the published address: a handshake that completes through it has crossed the router, which is the hop no local check can see. Every published edge must answer; one success does not mask another failed A record. Services without `edge: true` remain unverified unless an HTTPS probe is configured.

This reports reachability only. It sends no large frames, so it produces no path MTU verdict.

## An HTTPS probe endpoint

Set `probe.endpoint` to use a service you trust instead; it takes precedence over the edge. `bifrost doctor --probe <url>` uses the same contract against a temporary listener, before any configuration exists.

Request, one per service:

```json
{"address":"2001:db8:1234:1::10","port":443,"server_name":"media.example.com"}
```

`server_name` carries the published DNS name for probers that dispatch on TLS names. One that dials the address directly ignores it. `doctor --probe` omits it, since its listener has no name.

Response:

```json
{"reachable":true,"path_mtu":1492,"path_mtu_measured":true,"packet_too_big_works":true}
```

`path_mtu_measured` separates "found no blackhole" from "never looked". Omit it and Bifrost produces no `pmtu` finding rather than a false clean bill.

The client reads at most 64 KiB and times out after 20 seconds. A network error, invalid response, or non-success status is a warning, not proof the service is down.

## Privacy

An endpoint receives the public service address, port, published DNS name, your source address, and normal HTTPS metadata. It never receives the owner ID, DNS credentials, backend address, or application data.

Choose an endpoint whose retention policy you accept. Bifrost configures no public probe by default; an edge you run yourself avoids the question entirely.
