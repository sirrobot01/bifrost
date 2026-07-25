# Releasing

## Preconditions

- The worktree is clean and the intended release commit is on `main`.
- `go.mod` declares Go 1.26.
- `make verify` passes on Linux or an equivalent CI run is green.
- `docker build --build-arg VERSION=dev .` succeeds.
- `goreleaser check` and `make snapshot` pass. Run `goreleaser check` from a checkout with its Git remote configured.
- Configuration examples decode after their documented placeholders are replaced.
- The security, firewall, edge, and application limitations still match implementation.

## Tag and artifacts

Create an annotated semantic-version tag and push it:

```sh
git tag -a v1.0.0 -m 'Bifrost v1.0.0'
git push origin v1.0.0
```

The release workflow builds static Linux archives for amd64, arm64, and arm/v7, produces `checksums.txt`, signs that checksum with keyless Cosign using GitHub OIDC, and publishes a GitHub release. It also publishes and signs a multi-architecture image in GHCR. Pushing the tag is the publication approval, so complete every precondition first.

Verify one artifact exactly as a user would:

```sh
cosign verify-blob \
  --certificate-identity https://github.com/sirrobot01/bifrost/.github/workflows/release.yml@refs/tags/v1.0.0 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --bundle checksums.txt.sigstore.json checksums.txt
sha256sum --ignore-missing --check checksums.txt
```

## v1 acceptance

- A clean install can reach ready state from the example and documentation.
- Prefix rotation adds and publishes the new address before retiring the old one.
- Removing a desired service withdraws owned DNS without touching foreign records.
- `bifrost check` distinguishes local listener, provider ownership, public DNS, host firewall, external reachability, and PMTU findings.
- Direct mode has no Bifrost listener; splice mode reports source-IP behavior honestly.
- Edge TLS SNI and one static port map work from IPv4 through home IPv6.
- SIGTERM drains within `drain_grace` and the systemd service stops cleanly.
- Metrics stay on loopback and contain no secrets.
