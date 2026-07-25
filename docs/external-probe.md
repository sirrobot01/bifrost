# External probe contract

External reachability cannot be proven from inside the home network. Bifrost therefore supports an explicit, operator-selected HTTPS endpoint and makes no request when `probe.endpoint` is absent.

For each configured service, `bifrost check` sends:

```json
{"address":"2001:db8:1234:1::10","port":443}
```

The endpoint returns:

```json
{"reachable":true,"path_mtu":1492,"packet_too_big_works":true}
```

The address and port are necessarily disclosed to that endpoint, along with the source IP and ordinary HTTPS metadata visible to its operator. Choose an endpoint whose retention policy you accept. Bifrost sends no owner ID, DNS credential, service name, backend address, or application payload.

The client limits responses to 64 KiB and uses a 20-second timeout. A non-success HTTP status, malformed response, or network failure is reported as a warning rather than proof that the home service is down.
