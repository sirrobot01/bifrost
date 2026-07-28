---
title: Install Bifrost
description: The install script, the package and archive alternatives, release verification, and upgrades.
---

One command installs the latest release:

```sh
curl -fsSL https://bifrost.biodun.dev/install.sh | sh
```

The script detects the CPU and package manager, verifies the download against the release checksum file, and installs the deb or rpm package. Where neither package manager exists, it installs the archive binary to `/usr/local/bin` and prints the remaining setup steps.

The package installs the binary at `/usr/bin/bifrost`, creates the `bifrost` and `bifrost-edge` system accounts, creates `/etc/bifrost` with mode `0755`, and installs both systemd units. It does not enable or start anything: Bifrost changes DNS records and host addresses, so the first run stays an explicit decision.

Continue with the [quickstart](../quickstart/) to check the host, create the configuration, and start the service.

The rest of this page covers the alternatives to the script, release verification, and upgrades.

## Install without the script

Every method ends in the same place. Pick the one that fits how you manage the host.

Builds cover three CPUs. Packages use the first suffix, archives the second:

| CPU | Package suffix | Archive suffix |
|---|---|---|
| AMD64 or Intel 64 | `amd64` | `x86_64` |
| ARM64 | `arm64` | `aarch64` |
| 32-bit ARM v7 | `armv7` | `armv7` |

### Download the package directly

Debian and Ubuntu:

```sh
curl -fsSLO https://github.com/sirrobot01/bifrost/releases/latest/download/bifrost_linux_amd64.deb
sudo apt-get install -y ./bifrost_linux_amd64.deb
```

Fedora, RHEL, and openSUSE:

```sh
curl -fsSLO https://github.com/sirrobot01/bifrost/releases/latest/download/bifrost_linux_amd64.rpm
sudo dnf install -y ./bifrost_linux_amd64.rpm
```

This is exactly what the script does, without the script. Removing the package leaves `/etc/bifrost` and the accounts in place: those files hold the address secret that derives your service addresses, and deleting them would change every address on reinstall.

### Install from an archive

Use this when no package fits your distribution.

Archive names carry the release version, so download the one matching your CPU from the [releases page](https://github.com/sirrobot01/bifrost/releases/latest), then:

```sh
tar xzf bifrost_*_linux_x86_64.tar.gz
sudo install -m 0755 bifrost /usr/local/bin/bifrost
bifrost version
```

The archive does none of the setup a package does, so create the accounts, the directory, and the units yourself:

```sh
sudo useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin bifrost
sudo useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin bifrost-edge
sudo install -d -o bifrost -g bifrost -m 0755 /etc/bifrost
sudo install -m 0644 deploy/bifrost.service /etc/systemd/system/bifrost.service
sudo install -m 0644 deploy/bifrost-edge.service /etc/systemd/system/bifrost-edge.service
sudo systemctl daemon-reload
```

`/etc/bifrost` is deliberately traversable: on a host running both roles, `bifrost-edge` has to reach its own config there. The secrets inside stay `0600`, and Bifrost refuses to read one that is not.

The shipped unit runs `/usr/local/bin/bifrost`; the packages install to `/usr/bin` and repoint the unit with a drop-in. The unit grants `CAP_NET_ADMIN` for splice mode and `CAP_NET_BIND_SERVICE` for ports below 1024, and allows three minutes to stop; keep `TimeoutStopSec` longer than `drain_grace`.

### Use the container

The image at `ghcr.io/sirrobot01/bifrost` holds one static binary and CA certificates, and runs as UID and GID 65532. Use a fixed release tag in production.

The home role needs the host network namespace and `NET_ADMIN`. The supplied Compose file sets both:

```sh
docker compose -f examples/compose.yaml up -d
```

That example mounts the Docker socket, which grants root-level control of the Docker host. Use a socket proxy when possible.

Prefer the package or archive if you would rather not run a network-management container.

## Verify a release

Do this before installing on anything you care about. Every release is signed with cosign through GitHub Actions, so a verified checksum proves the artifact came from this repository's release workflow. The install script checks the sha256 but not the signature, because cosign is rarely installed before Bifrost is.

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

## Upgrade Bifrost

With a package, installing the new deb or rpm replaces the binary and leaves `/etc/bifrost` alone. Restart afterwards:

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
