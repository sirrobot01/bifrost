#!/bin/sh
# Bifrost installer.
#
#   curl -fsSL https://bifrost.biodun.dev/install.sh | sh
#
# Downloads the latest release for this machine, verifies it against the
# release checksum file, and installs it: the deb or rpm package where a
# package manager is available, otherwise the archive binary into
# /usr/local/bin. The script never enables or starts anything.
#
# The checksum file itself can be verified with cosign; this script does not,
# because cosign is rarely installed before Bifrost is. For a production host,
# follow https://bifrost.biodun.dev/getting-started/installation/ instead.
set -eu

REPO="sirrobot01/bifrost"
# BIFROST_DOWNLOAD overrides the release download base. CI uses it to run this
# script against locally built artifacts before they are published.
DOWNLOAD="${BIFROST_DOWNLOAD:-https://github.com/$REPO/releases/latest/download}"

fail() {
	echo "install.sh: $*" >&2
	exit 1
}

case "$(uname -s)" in
Linux) release_os=linux ;;
Darwin) release_os=darwin ;;
FreeBSD) release_os=freebsd ;;
OpenBSD) release_os=openbsd ;;
*) fail "unsupported operating system $(uname -s); releases cover Linux, macOS, FreeBSD, and OpenBSD" ;;
esac

case "$(uname -m)" in
amd64 | x86_64) package_arch=amd64 archive_arch=x86_64 ;;
aarch64 | arm64) package_arch=arm64 archive_arch=aarch64 ;;
armv7l) package_arch=armv7 archive_arch=armv7 ;;
*) fail "unsupported CPU $(uname -m); releases cover x86_64, aarch64, and armv7" ;;
esac

if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -q -O "$2" "$1"; }
else
	fail "curl or wget is required"
fi

if command -v sha256sum >/dev/null 2>&1; then
	verify_checksum() { grep " $1\$" "$workdir/checksums.txt" | (cd "$workdir" && sha256sum -c -); }
elif command -v shasum >/dev/null 2>&1; then
	verify_checksum() { grep " $1\$" "$workdir/checksums.txt" | (cd "$workdir" && shasum -a 256 -c -); }
elif command -v sha256 >/dev/null 2>&1; then
	verify_checksum() {
		expected="$(awk -v artifact="$1" '$2 == artifact { print $1; exit }' "$workdir/checksums.txt")"
		[ -n "$expected" ] && [ "$(sha256 -q "$workdir/$1")" = "$expected" ]
	}
else
	fail "sha256sum, shasum, or sha256 is required to verify the download"
fi

SUDO=""
if [ "$(id -u)" -ne 0 ]; then
	if command -v sudo >/dev/null 2>&1; then
		SUDO="sudo"
	elif command -v doas >/dev/null 2>&1; then
		SUDO="doas"
	else
		fail "run as root or install sudo/doas"
	fi
fi

if [ "$release_os" = linux ] && command -v apt-get >/dev/null 2>&1; then
	flavor=deb
elif [ "$release_os" = linux ] && { command -v dnf >/dev/null 2>&1 || command -v yum >/dev/null 2>&1; }; then
	flavor=rpm
else
	flavor=archive
fi

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

fetch "$DOWNLOAD/checksums.txt" "$workdir/checksums.txt"

case "$flavor" in
deb | rpm)
	artifact="bifrost_linux_$package_arch.$flavor"
	;;
archive)
	# Archive names carry the release version; read it from the checksum file
	# rather than asking the GitHub API.
	artifact="$(awk -v pattern="^bifrost_.*_${release_os}_$archive_arch\\\\.tar\\\\.gz$" \
		'$2 ~ pattern { print $2; exit }' "$workdir/checksums.txt")"
	[ -n "$artifact" ] || fail "no archive for $archive_arch in the latest release"
	;;
esac

echo "downloading $artifact"
fetch "$DOWNLOAD/$artifact" "$workdir/$artifact"
verify_checksum "$artifact" ||
	fail "checksum verification failed for $artifact"

case "$flavor" in
deb)
	$SUDO apt-get install -y "$workdir/$artifact"
	;;
rpm)
	if command -v dnf >/dev/null 2>&1; then
		$SUDO dnf install -y "$workdir/$artifact"
	else
		$SUDO yum install -y "$workdir/$artifact"
	fi
	;;
archive)
	if [ "$release_os" = darwin ]; then
		tar -xzf "$workdir/$artifact" -C "$workdir" bifrost deploy/dev.biodun.bifrost.plist deploy/dev.biodun.bifrost-edge.plist
	elif [ "$release_os" = freebsd ]; then
		tar -xzf "$workdir/$artifact" -C "$workdir" bifrost deploy/freebsd/bifrost deploy/freebsd/bifrost-edge
	elif [ "$release_os" = openbsd ]; then
		tar -xzf "$workdir/$artifact" -C "$workdir" bifrost deploy/openbsd/bifrost deploy/openbsd/bifrost_edge
	else
		tar -xzf "$workdir/$artifact" -C "$workdir" bifrost
	fi
	$SUDO install -m 0755 "$workdir/bifrost" /usr/local/bin/bifrost
	echo "installed /usr/local/bin/bifrost"
	if [ "$release_os" = darwin ]; then
		$SUDO install -m 0644 "$workdir/deploy/dev.biodun.bifrost.plist" /Library/LaunchDaemons/dev.biodun.bifrost.plist
		$SUDO install -m 0644 "$workdir/deploy/dev.biodun.bifrost-edge.plist" /Library/LaunchDaemons/dev.biodun.bifrost-edge.plist
		echo "installed launchd definitions without loading them"
	elif [ "$release_os" = freebsd ]; then
		$SUDO install -m 0755 "$workdir/deploy/freebsd/bifrost" /usr/local/etc/rc.d/bifrost
		$SUDO install -m 0755 "$workdir/deploy/freebsd/bifrost-edge" /usr/local/etc/rc.d/bifrost-edge
		echo "installed FreeBSD rc.d scripts without enabling them"
	elif [ "$release_os" = openbsd ]; then
		$SUDO install -m 0555 "$workdir/deploy/openbsd/bifrost" /etc/rc.d/bifrost
		$SUDO install -m 0555 "$workdir/deploy/openbsd/bifrost_edge" /etc/rc.d/bifrost_edge
		echo "installed OpenBSD rc.d scripts without enabling them"
	fi
	echo "the archive does not enable or start an operating-system service;"
	echo "follow https://bifrost.biodun.dev/getting-started/installation/#install-from-an-archive"
	;;
esac

echo
echo "Next: sudo bifrost doctor"
