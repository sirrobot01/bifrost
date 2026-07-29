---
title: Install Bifrost
description: The install script, the package and archive alternatives, release verification, and upgrades.
---

One command installs the latest release:

```sh
curl -fsSL https://bifrost.biodun.dev/install.sh | sh
```

The script detects the operating system, CPU, and package manager, verifies the download against the release checksum file, and installs the deb or rpm package on Linux. On macOS, or where no Linux package manager fits, it installs the archive binary to `/usr/local/bin` and prints the remaining service setup steps.

The package installs the binary at `/usr/bin/bifrost`, creates the `bifrost` and `bifrost-edge` system accounts, creates `/etc/bifrost` with mode `0755`, and installs both systemd units. It does not enable or start anything: Bifrost changes DNS records and host addresses, so the first run stays an explicit decision.

The deb and rpm are named `bifrost-ingress`, because Debian and Ubuntu already ship an unrelated package called `bifrost`. Only the package name differs; the binary, the units, and `/etc/bifrost` are unchanged, so every command is still `bifrost`.

Continue with the [quickstart](../quickstart/) to check the host, create the configuration, and start the service.

The rest of this page covers the alternatives to the script, release verification, and upgrades.

## Install without the script

Every method ends in the same place. Pick the one that fits how you manage the host.

Linux builds cover three CPUs; macOS builds cover Intel and Apple silicon. Packages use the first suffix, archives the second:

| CPU | Package suffix | Archive suffix |
|---|---|---|
| AMD64 or Intel 64 | `amd64` | `x86_64` |
| ARM64 | `arm64` | `aarch64` |
| 32-bit ARM v7 | `armv7` | `armv7` |

### macOS

The install script selects `darwin_x86_64` on Intel or `darwin_aarch64` on Apple silicon and installs both launchd definitions without loading them. After creating the configuration with `sudo bifrost init --interactive`, load the home daemon:

```sh
sudo install -d -m 0755 /etc/bifrost /var/lib/bifrost
sudo launchctl bootstrap system /Library/LaunchDaemons/dev.biodun.bifrost.plist
sudo launchctl enable system/dev.biodun.bifrost
```

The daemon runs as root because macOS requires administrative privilege to add service IPv6 aliases. It writes structured logs to `/var/log/bifrost.log`. To reload a changed configuration without dropping listeners:

```sh
sudo launchctl kill HUP system/dev.biodun.bifrost
```

macOS supports advisory firewall mode. Keep `firewall.mode: advisory` and add the required address-and-port rules to the host policy yourself. `firewall.pcp` remains available when the router supports PCP. Docker discovery works with a configured Unix socket; Docker Desktop commonly uses a per-user socket, so set `docker.socket` explicitly when enabling it.

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

Use this when no package fits your operating system or distribution.

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

```sh
sudo bifrost upgrade
```

This downloads the latest release archive, verifies it against the release checksum file, and replaces the running binary in place. `/etc/bifrost` is never touched. Add `--check` to compare versions without installing anything, or `--restart` to restart whichever units are running once the new binary is in place.

Without `--restart`, the command prints what to restart. The old binary keeps serving until you do:

```sh
sudo systemctl restart bifrost          # home
sudo systemctl restart bifrost-edge     # edge
sudo bifrost check --config /etc/bifrost/config.yaml
```

Configuration changes do not need a restart at all: `sudo systemctl reload bifrost` re-reads the file and reconciles the difference. See [configuration](../../guides/configuration/).

Restarting the edge only drops connections in flight. Restarting home removes owned DNS records and managed addresses first, so it can interrupt service briefly, and resolver caches can extend that past the configured TTL.

Upgrade home and edge hosts to the same version. The edge header carries a fixed version byte with no negotiation, so a mismatched pair has nothing that would warn you if the frame format ever changes.

### Upgrading with a package manager

Installing a newer deb or rpm also works and keeps the package database accurate:

```sh
curl -fsSL https://bifrost.biodun.dev/install.sh | sh
sudo systemctl restart bifrost
```

:::caution[Installs from 0.5.2 and earlier need one manual step]
Those releases were packaged as `bifrost`, which collides with an unrelated package of that name in Debian and Ubuntu. Because the distribution's version sorts higher, `apt upgrade` could treat it as a newer Bifrost and replace this one with it, removing the binary and both units.

Move to the renamed package once, on each host:

```sh
sudo apt-mark unhold bifrost 2>/dev/null || true
sudo apt-get remove -y bifrost
curl -fsSL https://bifrost.biodun.dev/install.sh | sh
```

`/etc/bifrost` survives, because the package does not own it. Check with `dpkg -l bifrost-ingress` that the new name is installed.
:::

### Rolling back

Keep the old binary until every check passes. `bifrost upgrade` writes the new binary by renaming it over the old one, so an interrupted upgrade leaves the previous binary in place rather than a partial file.
