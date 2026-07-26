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
DOWNLOAD="https://github.com/$REPO/releases/latest/download"

fail() {
	echo "install.sh: $*" >&2
	exit 1
}

[ "$(uname -s)" = "Linux" ] || fail "Bifrost runs on Linux; see https://bifrost.biodun.dev for the container image"

case "$(uname -m)" in
x86_64) package_arch=amd64 archive_arch=x86_64 ;;
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

command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is required to verify the download"

SUDO=""
if [ "$(id -u)" -ne 0 ]; then
	command -v sudo >/dev/null 2>&1 || fail "run as root or install sudo"
	SUDO="sudo"
fi

if command -v apt-get >/dev/null 2>&1; then
	flavor=deb
elif command -v dnf >/dev/null 2>&1 || command -v yum >/dev/null 2>&1; then
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
	artifact="$(awk -v pattern="^bifrost_.*_linux_$archive_arch\\\\.tar\\\\.gz$" \
		'$2 ~ pattern { print $2; exit }' "$workdir/checksums.txt")"
	[ -n "$artifact" ] || fail "no archive for $archive_arch in the latest release"
	;;
esac

echo "downloading $artifact"
fetch "$DOWNLOAD/$artifact" "$workdir/$artifact"
(cd "$workdir" && grep " $artifact\$" checksums.txt | sha256sum -c -) ||
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
	tar -xzf "$workdir/$artifact" -C "$workdir" bifrost
	$SUDO install -m 0755 "$workdir/bifrost" /usr/local/bin/bifrost
	echo "installed /usr/local/bin/bifrost"
	echo "the archive does not create the service account or systemd unit;"
	echo "follow https://bifrost.biodun.dev/getting-started/installation/#install-from-an-archive"
	;;
esac

echo
echo "Next: sudo bifrost doctor"
