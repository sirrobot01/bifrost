#!/bin/sh
# Create the unprivileged accounts the units run as. Both dpkg and rpm run this
# before any file is unpacked, so the accounts exist by the time postinstall
# takes ownership of /etc/bifrost.
set -e

create_account() {
	account="$1"
	if getent passwd "$account" >/dev/null 2>&1; then
		return 0
	fi
	if command -v useradd >/dev/null 2>&1; then
		useradd --system --no-create-home --home-dir /nonexistent \
			--shell /usr/sbin/nologin "$account"
	elif command -v adduser >/dev/null 2>&1; then
		# BusyBox and Alpine style, where useradd is absent.
		adduser -S -H -h /nonexistent -s /sbin/nologin "$account"
	else
		echo "bifrost: no useradd or adduser found; create the $account account manually" >&2
		return 0
	fi
}

create_account bifrost
create_account bifrost-edge
