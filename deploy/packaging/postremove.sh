#!/bin/sh
# The service accounts and /etc/bifrost are left in place. They hold the address
# secret and DNS credentials, and removing them would silently destroy the
# derived addresses a reinstall would otherwise reproduce.
set -e

if [ -d /run/systemd/system ]; then
	systemctl daemon-reload >/dev/null 2>&1 || true
fi
