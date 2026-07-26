---
title: Install Bifrost
description: Install from a package, an archive, or a container, and verify the release.
---

For a first install, follow the [quickstart](../quickstart/) instead. This page covers every install method, release verification, and upgrades.

Bifrost publishes Linux builds for these CPUs:

| Suffix | CPU |
|---|---|
| `amd64`, `x86_64` | AMD64 or Intel 64 |
| `arm64`, `aarch64` | ARM64 |
| `armv7` | 32-bit ARM v7 |

## Install a package

Debian and Ubuntu:

```sh
curl -fsSLO https://github.com/sirrobot01/bifrost/releases/latest/download/bifrost_linux_amd64.deb
sudo apt-get install -y ./bifrost_linux_amd64.deb
```

Fedora, RHEL, and openSUSE:

```sh
curl -fsSLO https://github.com/sirrobot01/bifrost/releases/latest/download/bifrost_linux_amd64.rpm
sudo rpm -i ./bifrost_linux_amd64.rpm
```

The package installs the binary at `/usr/bin/bifrost`, creates the `bifrost` and `bifrost-edge` system accounts, creates `/etc/bifrost` with mode `0750`, and installs both systemd units.

It does not enable or start anything. Bifrost changes DNS records and host addresses, so the first run stays an explicit decision.

Removing the package leaves `/etc/bifrost` and the accounts in place. Those files hold the address secret that derives your service addresses, and deleting them would change every address on reinstall.

## Install from an archive

Use this when no package fits your distribution.

Archive names carry the release version, so download the one matching your CPU from the [releases page](https://github.com/sirrobot01/bifrost/releases/latest), then:

```sh
tar xzf bifrost_*_linux_x86_64.tar.gz
sudo install -m 0755 bifrost /usr/local/bin/bifrost
bifrost version
```

The archive does none of the setup a package does, so create the account, the directory, and the unit yourself:

```sh
sudo useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin bifrost
sudo install -d -o bifrost -g bifrost -m 0750 /etc/bifrost
sudo install -m 0644 deploy/bifrost.service /etc/systemd/system/bifrost.service
sudo systemctl daemon-reload
```

The shipped unit runs `/usr/local/bin/bifrost`. The packages install to `/usr/bin` and repoint the unit with a drop-in.

Then continue with `bifrost doctor` and `bifrost init --interactive` as in the [quickstart](../quickstart/).

## Verify a release

Do this before installing on anything you care about. Every release is signed with cosign through GitHub Actions, so a verified checksum proves the artifact came from this repository's release workflow.

Download the artifact, `checksums.txt`, and `checksums.txt.sigstore.json` from the same release.

```sh
cosign verify-blob \
  --certificate-identity-regexp 'https://github.com/sirrobot01/bifrost/.github/workflows/release.yml@refs/tags/v.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --bundle checksums.txt.sigstore.json checksums.txt
sha256sum --ignore-missing --check checksums.txt
```

The first command proves who produced `checksums.txt`. The second proves your download matches it. Do not install if either fails.

Packages, archives, and container images are all covered by the same checksum file.

## Use the container

The image at `ghcr.io/sirrobot01/bifrost` holds one static binary and CA certificates, and runs as UID and GID 65532. Use a fixed release tag in production.

The home role needs the host network namespace and `NET_ADMIN`. The supplied Compose file sets both:

```sh
docker compose -f examples/compose.yaml up -d
```

That example mounts the Docker socket, which grants root-level control of the Docker host. Use a socket proxy when possible.

Prefer the package or archive if you would rather not run a network-management container.

## Create the configuration by hand

`bifrost init --interactive` creates the configuration, the address secret, and the DNS credential with correct permissions. Prefer it.

To write the files yourself, generate a template and fill it in:

```sh
sudo -u bifrost bifrost init --output /etc/bifrost/config.yaml
```

That template is not runnable. It has no service, no credential, no address secret, and a placeholder zone ID. You must also:

```sh
openssl rand -hex 32 | sudo -u bifrost tee /etc/bifrost/address-secret >/dev/null
sudo -u bifrost install -m 0600 /dev/null /etc/bifrost/cloudflare-token
sudoedit /etc/bifrost/cloudflare-token
sudoedit /etc/bifrost/config.yaml
sudo chmod 0600 /etc/bifrost/config.yaml /etc/bifrost/address-secret /etc/bifrost/cloudflare-token
```

Bifrost refuses any secret file readable by group or other. The [configuration guide](../../guides/configuration/) describes every field.

## Review before the first start

```sh
sudo -u bifrost bifrost status --offline --config /etc/bifrost/config.yaml
sudo bifrost serve --config /etc/bifrost/config.yaml --dry-run
```

The first shows the selected prefix and the derived service addresses. The second shows the DNS records and host addresses Bifrost would change. Neither changes anything.

Start the service only when the output is correct:

```sh
sudo systemctl enable --now bifrost
systemctl status bifrost
sudo bifrost check --config /etc/bifrost/config.yaml
curl --fail http://127.0.0.1:9098/healthz
```

The unit grants `CAP_NET_ADMIN` for splice mode and `CAP_NET_BIND_SERVICE` for ports below 1024. It allows three minutes to stop; keep `TimeoutStopSec` longer than `drain_grace`.

## Upgrade Bifrost

With a package, `apt-get install ./bifrost_*.deb` or `rpm -U` replaces the binary and leaves `/etc/bifrost` alone. Restart afterwards:

```sh
sudo systemctl restart bifrost
sudo bifrost check --config /etc/bifrost/config.yaml
```

With an archive:

1. Download and verify the new release.
2. Run the new binary with `serve --dry-run`.
3. Stop the service.
4. Replace the binary.
5. Start the service.
6. Run `bifrost check`.

A clean stop removes owned DNS records and managed addresses, so a restart can interrupt service briefly. Resolver caches can extend that past the configured TTL.

Keep the old binary until every check passes, and restore it if you must roll back.
