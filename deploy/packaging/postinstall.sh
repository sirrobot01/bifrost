#!/bin/sh
# Prepare the configuration directory and tell the operator what to do next.
# This deliberately does not enable or start the service: Bifrost changes DNS
# records and host addresses, so the first run must be an explicit decision made
# after reviewing a dry run.
set -e

# The directory is traversable so the bifrost-edge account can reach its own
# configuration on an edge host. Secrets are protected by their own 0600 modes,
# which Bifrost verifies before reading them.
install -d -o bifrost -g bifrost -m 0755 /etc/bifrost

if [ -d /run/systemd/system ]; then
	systemctl daemon-reload >/dev/null 2>&1 || true
fi

# An existing configuration means this is an upgrade, where setup guidance would
# only be noise.
if [ ! -e /etc/bifrost/config.yaml ]; then
	cat <<'GUIDE'

Bifrost is installed but not configured or started.

  sudo bifrost doctor                 # confirm this host has usable IPv6
  sudo bifrost init --interactive     # create the config, secret, and credential
  sudo bifrost serve --config /etc/bifrost/config.yaml --dry-run
  sudo systemctl enable --now bifrost

Documentation: https://bifrost.biodun.dev/getting-started/quickstart/
GUIDE
fi
