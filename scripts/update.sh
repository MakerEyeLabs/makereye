#!/usr/bin/env bash
#
# Single-command MakerEye updater for a device installed from a git
# checkout: pull the latest commit on the current branch, rebuild,
# reinstall, and restart the service.
#
#   ./scripts/update.sh          # sudo is used for install/restart only
#
# Reuses scripts/install.sh for the install step, so dependency changes
# (new apt packages, go2rtc updates) are picked up automatically, and
# the existing /etc/makereye/config.yaml is never touched.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

log() { echo "==> $*"; }

# The Go toolchain installed by bootstrap.sh lives outside the default
# PATH of non-login shells.
export PATH="$PATH:/usr/local/go/bin"

before="$(git rev-parse --short HEAD)"
branch="$(git rev-parse --abbrev-ref HEAD)"

log "updating branch $branch (currently $before)"
git pull --ff-only

after="$(git rev-parse --short HEAD)"
if [ "$before" = "$after" ]; then
	log "already up to date ($after); rebuilding and reinstalling anyway"
else
	log "updated $before -> $after"
fi

log "building"
make build

log "installing (needs sudo)"
sudo ./scripts/install.sh ./bin/makereye

log "restarting makereye.service"
sudo systemctl restart makereye

sleep 2
makereye status || true

log "update complete: running $(git rev-parse --short HEAD)"
