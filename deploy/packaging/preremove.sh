#!/bin/sh
# Stop the units only on real removal. dpkg passes "upgrade" and rpm passes 1
# when a newer version is replacing this one, and stopping there would drop
# published services for the length of the upgrade.
set -e

case "$1" in
0 | remove | purge)
	if [ -d /run/systemd/system ]; then
		for unit in bifrost.service bifrost-edge.service; do
			systemctl stop "$unit" >/dev/null 2>&1 || true
			systemctl disable "$unit" >/dev/null 2>&1 || true
		done
	fi
	;;
esac
