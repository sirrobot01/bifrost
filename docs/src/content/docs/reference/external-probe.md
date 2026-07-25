---
title: External probe contract
description: Request format, response format, and privacy limits for reachability tests.
---

A host cannot prove external reachability from inside its own network. Bifrost can call an HTTPS probe that the operator selects. It does not call a probe when `probe.endpoint` is empty.

## Request

For each configured service, `bifrost check` sends this JSON body:

```json
{"address":"2001:db8:1234:1::10","port":443}
```

## Response

The endpoint returns this JSON body:

```json
{"reachable":true,"path_mtu":1492,"packet_too_big_works":true}
```

The client accepts no more than 64 KiB of response data. It uses a 20-second timeout.

A network error, invalid response, or non-success HTTP status produces a warning. It does not prove that the home service is down.

## Privacy

The probe operator receives the public service address and port. The operator can also see the request source address and normal HTTPS metadata.

Bifrost does not send these values:

- Owner ID
- DNS credential
- Service name
- Backend address
- Application data

Select an endpoint with a retention policy that you accept. Bifrost does not configure a public probe by default.
