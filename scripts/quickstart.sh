#!/usr/bin/env bash
#
# One-shot MakerEye installer for Raspberry Pi OS Lite 64-bit.
#
# Clones (or updates) MakerEye, builds it, and installs it as a systemd
# service in one step, handling prerequisites (git, make, Go) along the
# way. Meant to be run directly on the Pi:
#
#   curl -fsSL https://raw.githubusercontent.com/MakerEyeLabs/makereye/main/scripts/quickstart.sh | sudo bash
#
# Equivalent to a manual clone followed by bootstrap.sh, make build, and
# install.sh -- see README.md for those individual steps if you'd rather
# review the scripts before running them as root.
#
# Safe to re-run: pulls the latest commit instead of re-cloning, and
# each step this delegates to (bootstrap.sh, install.sh) is itself safe
# to re-run.

set -euo pipefail

REPO_URL="https://github.com/MakerEyeLabs/makereye.git"
SRC_DIR="/opt/makereye-src"

log() { echo "==> $*"; }
die() { echo "ERROR: $*" >&2; exit 1; }

require_root() {
	if [ "$(id -u)" -ne 0 ]; then
		die "this script must be run as root (try: curl ... | sudo bash)"
	fi
}

fetch_source() {
	if [ -d "$SRC_DIR/.git" ]; then
		log "updating existing checkout at $SRC_DIR"
		git -C "$SRC_DIR" pull --ff-only
		return
	fi

	if ! command -v git >/dev/null 2>&1; then
		log "git not found, installing it"
		export DEBIAN_FRONTEND=noninteractive
		apt-get update -qq
		apt-get install -y --no-install-recommends git
	fi
	log "cloning $REPO_URL to $SRC_DIR"
	git clone "$REPO_URL" "$SRC_DIR"
}

main() {
	require_root
	fetch_source
	cd "$SRC_DIR"

	log "running scripts/bootstrap.sh (git/make/Go)"
	./scripts/bootstrap.sh
	export PATH="$PATH:/usr/local/go/bin"

	log "building makereye"
	make build

	log "running scripts/install.sh"
	./scripts/install.sh ./bin/makereye
}

main "$@"
