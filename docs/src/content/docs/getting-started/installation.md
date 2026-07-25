---
title: Install Bifrost
description: Install the binary, create secrets, and start the home service.
---

## Select a package

Bifrost provides these Linux packages:

| Package suffix | CPU |
|---|---|
| `linux_x86_64` | AMD64 or Intel 64 |
| `linux_aarch64` | ARM64 |
| `linux_armv7` | 32-bit ARM v7 |

You can also use the container image at `ghcr.io/sirrobot01/bifrost`. Use a fixed release tag in production.

## Verify a release

Download the archive, `checksums.txt`, and `checksums.txt.sigstore.json` from the same release.

Run these commands:

```sh
cosign verify-blob \
  --certificate-identity-regexp 'https://github.com/sirrobot01/bifrost/.github/workflows/release.yml@refs/tags/v.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --bundle checksums.txt.sigstore.json checksums.txt
sha256sum --ignore-missing --check checksums.txt
```

Do not install the archive if either command fails.

## Install the binary

Extract the archive. Then install the binary:

```sh
sudo install -m 0755 bifrost /usr/local/bin/bifrost
```

Confirm the version:

```sh
bifrost version
```

## Create the service account

Create one account for the home service:

```sh
sudo useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin bifrost
sudo install -d -o bifrost -g bifrost -m 0750 /etc/bifrost
```

## Create the configuration

Replace `eth0` with the interface that has the global IPv6 address.

```sh
sudo -u bifrost /usr/local/bin/bifrost init \
  --interface eth0 \
  --output /etc/bifrost/config.yaml
```

The command does not create secrets. Create the address secret:

```sh
sudo -u bifrost install -m 0600 /dev/null /etc/bifrost/address-secret
openssl rand -hex 32 | sudo -u bifrost tee /etc/bifrost/address-secret >/dev/null
```

Create the DNS token file:

```sh
sudo -u bifrost install -m 0600 /dev/null /etc/bifrost/cloudflare-token
sudoedit /etc/bifrost/cloudflare-token
```

Edit the configuration:

```sh
sudoedit /etc/bifrost/config.yaml
```

Set the correct owner and mode after the edit:

```sh
sudo chown bifrost:bifrost /etc/bifrost/*.yaml /etc/bifrost/*-secret /etc/bifrost/*-token
sudo chmod 0600 /etc/bifrost/*.yaml /etc/bifrost/*-secret /etc/bifrost/*-token
```

See the [configuration guide](../../guides/configuration/) for all fields.

## Review the plan

First, show the selected prefix and service addresses:

```sh
sudo -u bifrost /usr/local/bin/bifrost status --offline --config /etc/bifrost/config.yaml
```

Then show the changes that Bifrost will make:

```sh
sudo /usr/local/bin/bifrost serve --config /etc/bifrost/config.yaml --dry-run
```

Do not start the service until the output is correct.

## Start the systemd service

Install the supplied unit:

```sh
sudo install -m 0644 deploy/bifrost.service /etc/systemd/system/bifrost.service
sudo systemctl daemon-reload
sudo systemctl enable --now bifrost
```

Check the service:

```sh
systemctl status bifrost
sudo /usr/local/bin/bifrost check --config /etc/bifrost/config.yaml
curl --fail http://127.0.0.1:9098/healthz
```

The unit grants `CAP_NET_ADMIN` and `CAP_NET_BIND_SERVICE`. Splice mode requires `CAP_NET_ADMIN`. Ports below 1024 require `CAP_NET_BIND_SERVICE`.

The unit gives Bifrost three minutes to stop. Keep `TimeoutStopSec` longer than `drain_grace`.

## Use the container

The image contains one static binary and CA certificates. The image uses UID and GID 65532 by default.

The home role needs the host network namespace. It also needs the `NET_ADMIN` capability. The supplied Compose file has these settings:

```sh
docker compose -f examples/compose.yaml up -d
```

The example mounts the Docker socket. Access to that socket gives root-level control of the Docker host. Use a socket proxy when possible.

Use the binary and systemd unit if you do not want a network-management container.

## Upgrade Bifrost

1. Download the new release.
2. Verify the new release.
3. Run the new binary with `serve --dry-run`.
4. Stop the service.
5. Replace the binary.
6. Start the service.
7. Run `bifrost check`.

A clean stop removes owned DNS records and managed addresses. A restart can cause a short interruption. Resolver caches can extend this interruption past the configured TTL.

Keep the old binary until all checks pass. Restore that binary if you must roll back.
