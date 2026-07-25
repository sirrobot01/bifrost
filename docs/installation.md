# Installation

## Binary

Download the archive for `linux_x86_64`, `linux_aarch64`, or `linux_armv7`. Verify the signed checksum before installing:

```sh
cosign verify-blob \
  --certificate-identity-regexp 'https://github.com/sirrobot01/bifrost/.github/workflows/release.yml@refs/tags/v.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --bundle checksums.txt.sigstore.json checksums.txt
sha256sum --ignore-missing --check checksums.txt
sudo install -m 0755 bifrost /usr/local/bin/bifrost
```

Release tags publish the GitHub release and signed container image automatically. Maintainers complete the release checklist before pushing a tag.

## Home service

Create an unprivileged service account and a protected configuration directory:

```sh
sudo useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin bifrost
sudo install -d -o bifrost -g bifrost -m 0750 /etc/bifrost
sudo -u bifrost install -m 0600 /dev/null /etc/bifrost/address-secret
openssl rand -hex 32 | sudo -u bifrost tee /etc/bifrost/address-secret >/dev/null
sudo -u bifrost install -m 0600 /dev/null /etc/bifrost/cloudflare-token
sudoedit /etc/bifrost/cloudflare-token
sudo install -o bifrost -g bifrost -m 0600 configs/bifrost.example.yaml /etc/bifrost/config.yaml
sudoedit /etc/bifrost/config.yaml
```

The bundled systemd unit grants only `CAP_NET_ADMIN` and `CAP_NET_BIND_SERVICE`, then applies filesystem, namespace, device, and kernel hardening. If all public ports are above 1023, removing `CAP_NET_BIND_SERVICE` is safe. Do not remove `CAP_NET_ADMIN`; splice mode needs it to manage service addresses. Keep `TimeoutStopSec` longer than `drain_grace` when changing the default.

```sh
sudo install -m 0644 deploy/bifrost.service /etc/systemd/system/bifrost.service
sudo systemctl daemon-reload
sudo systemctl enable --now bifrost
journalctl -u bifrost -f
```

When Docker discovery is enabled, the service account must be able to access the configured Unix socket. Membership in the Docker group is root-equivalent. A read-only socket mount does not make the Docker API read-only, so a socket proxy restricted to container list and event endpoints is preferable.

## Container

The image is a static binary plus CA certificates and defaults to UID/GID 65532. Published images are available as `ghcr.io/sirrobot01/bifrost:<version>` and the moving major tag `:1`. The home role needs the host network namespace and `NET_ADMIN`; the provided Compose example runs it as root with all capabilities dropped except `NET_ADMIN` and `NET_BIND_SERVICE`.

```sh
docker compose -f examples/compose.yaml up -d
```

Use the binary/systemd deployment if you do not want a privileged network-management container. The edge can run as UID 65532 with only `NET_BIND_SERVICE` when its key is readable by that UID.

## Upgrade and rollback

Run `bifrost serve --dry-run` with the new binary and current config before replacing the service binary. Keep the previous binary until `/healthz` reports ready and `bifrost check` passes. To roll back, stop Bifrost, restore the prior binary, and restart. Do not manually remove the ownership TXT markers; they are what lets either version distinguish its records from operator-owned DNS.

A clean stop withdraws owned DNS, drains listeners, and removes managed addresses. A restart can therefore have a short interruption bounded by provider and resolver behavior even with a low TTL. A crash cannot run cleanup; the next process recovers ownership from DNS and reconciles desired state without relying on a local state file.
