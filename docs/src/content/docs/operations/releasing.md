---
title: Release process
description: Validate, tag, publish, and verify a Bifrost release.
---

## Check the release

Complete these checks before you create a tag:

- Confirm that the worktree is clean.
- Confirm that the release commit is on `main`.
- Confirm that `go.mod` specifies Go 1.26.
- Run `make verify` on a supported development host, and confirm native CI or smoke tests passed for every released OS.
- Run `make docs`.
- Run `docker build --build-arg VERSION=dev .`.
- Run `goreleaser check`.
- Run `make snapshot`.
- Test the configuration examples after you replace their placeholders.
- Confirm that the security, firewall, edge, and application limits match the code.

Run `goreleaser check` from a checkout that has its Git remote.

## Publish a tag

Create and push an annotated semantic-version tag:

```sh
git tag -a v1.0.0 -m 'Bifrost v1.0.0'
git push origin v1.0.0
```

The release workflow builds static Linux archives for amd64, arm64, and arm/v7, plus amd64 and arm64 archives for macOS, FreeBSD, OpenBSD, and Windows. It creates `checksums.txt` and signs it with keyless Cosign through GitHub OIDC. It also publishes and signs a multi-architecture Linux image in GHCR.

The tag starts publication. Complete all checks before you push it.

## Verify an artifact

Test one artifact as a user would:

```sh
cosign verify-blob \
  --certificate-identity https://github.com/sirrobot01/bifrost/.github/workflows/release.yml@refs/tags/v1.0.0 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --bundle checksums.txt.sigstore.json checksums.txt
sha256sum --ignore-missing --check checksums.txt
```

## Check v1 acceptance

- A clean install reaches ready state with the example and documentation.
- Prefix rotation publishes the new address before it removes the old address.
- Service removal deletes owned DNS records and preserves foreign records.
- `bifrost check` reports local listeners, TLS certificates, provider ownership, public DNS, host firewall, external reachability, and path MTU findings separately.
- Direct mode has no Bifrost listener.
- Splice mode reports its client-address behavior.
- A certificate is issued on first start and renewed before expiry, and `tls: off` never touches ACME.
- Managed firewall mode drops unpublished inbound IPv6, keeps `allow_ports` reachable, and removes its table on stop.
- `bifrost edge invite` and `bifrost edge join` enrol an edge without hand-written keys.
- Edge TLS routing and one static port map work from IPv4 through home IPv6.
- `check` reports reachability as unverified unless something outside the network confirmed it.
- Unix termination signals and Windows SCM stop requests drain connections within `drain_grace`.
- The native systemd, launchd, rc.d, or Windows service stops cleanly.
- Metrics stay on loopback and contain no secrets.
