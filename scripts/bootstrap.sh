#!/usr/bin/env bash
#
# MakerEye build-dependency bootstrapper.
#
# Stock Raspberry Pi OS Lite doesn't include git, make, or a Go toolchain
# new enough to build MakerEye (go.mod requires Go 1.24.7+; Debian's
# packaged `golang-go` is normally well behind that). This script installs
# what's needed to `git clone` and `make build` / `make build-arm64`.
#
# This does NOT install MakerEye's runtime dependencies (rpicam-apps,
# go2rtc, etc.), that's scripts/install.sh, run after building.
#
# Usage:
#   sudo ./scripts/bootstrap.sh
#
# Safe to re-run: apt-get install is idempotent, and the Go install step
# is skipped if an adequate toolchain is already on PATH.

set -euo pipefail

MIN_GO_VERSION="1.24.7"
GO_INSTALL_DIR="/usr/local/go"
PROFILE_SNIPPET="/etc/profile.d/makereye-go.sh"

log()  { echo "==> $*"; }
warn() { echo "WARNING: $*" >&2; }
die()  { echo "ERROR: $*" >&2; exit 1; }

require_root() {
	if [ "$(id -u)" -ne 0 ]; then
		die "this script must be run as root (try: sudo $0)"
	fi
}

install_packages() {
	log "installing git, make, and download tooling via apt"
	export DEBIAN_FRONTEND=noninteractive
	apt-get update -qq
	apt-get install -y --no-install-recommends \
		git \
		make \
		ca-certificates \
		curl \
		tar
}

detect_go_arch() {
	case "$(uname -m)" in
	aarch64 | arm64)
		GO_ARCH="arm64"
		;;
	armv7l | armhf)
		GO_ARCH="armv6l"
		;;
	x86_64)
		GO_ARCH="amd64"
		;;
	*)
		die "unsupported architecture for Go toolchain install: $(uname -m)"
		;;
	esac
}

# True (exit 0) if version $1 is strictly less than version $2.
version_lt() {
	[ "$1" = "$2" ] && return 1
	[ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | head -n1)" = "$1" ]
}

go_already_adequate() {
	command -v go >/dev/null 2>&1 || return 1
	local have
	have="$(go version | sed -n 's/^go version go\([0-9.]*\).*/\1/p')"
	[ -n "$have" ] || return 1
	if version_lt "$have" "$MIN_GO_VERSION"; then
		return 1
	fi
	log "existing Go toolchain (go$have) already satisfies the minimum (go$MIN_GO_VERSION), skipping Go install"
	return 0
}

install_go() {
	if go_already_adequate; then
		return
	fi

	log "resolving latest stable Go release"
	local version
	version="$(curl -fsSL 'https://go.dev/VERSION?m=text' | head -n1)"
	if [ -z "$version" ]; then
		warn "could not resolve the latest Go version from go.dev, falling back to go$MIN_GO_VERSION"
		version="go$MIN_GO_VERSION"
	fi

	local url="https://go.dev/dl/${version}.linux-${GO_ARCH}.tar.gz"
	log "installing $version (linux/$GO_ARCH) to $GO_INSTALL_DIR"
	local tmp
	tmp="$(mktemp)"
	if ! curl -fsSL "$url" -o "$tmp"; then
		rm -f "$tmp"
		die "failed to download Go from $url"
	fi
	rm -rf "$GO_INSTALL_DIR"
	tar -C "$(dirname "$GO_INSTALL_DIR")" -xzf "$tmp"
	rm -f "$tmp"

	log "adding $GO_INSTALL_DIR/bin to PATH via $PROFILE_SNIPPET"
	echo "export PATH=\$PATH:$GO_INSTALL_DIR/bin" >"$PROFILE_SNIPPET"
	chmod 0644 "$PROFILE_SNIPPET"
}

print_next_steps() {
	cat <<EOF

Build dependencies ready: git, make, Go ($("$GO_INSTALL_DIR/bin/go" version 2>/dev/null || echo "already on PATH")).

If 'go' is not yet on PATH in this shell, start a new login shell or run:
  export PATH=\$PATH:$GO_INSTALL_DIR/bin

Next steps:
  make build          # or: make build-arm64
  sudo ./scripts/install.sh ./bin/makereye
EOF
}

main() {
	require_root
	install_packages
	detect_go_arch
	install_go
	print_next_steps
}

main "$@"
