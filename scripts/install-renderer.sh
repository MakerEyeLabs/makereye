#!/usr/bin/env bash
#
# Installer for the MakerEye remote timelapse renderer on a Debian-based
# machine (NOT the camera Pi -- this is for the box that mounts the
# shared timelapse directory and does the ffmpeg work).
#
#   sudo ./scripts/install-renderer.sh [watch-directory]
#
# Installs ffmpeg + jq, puts makereye-render on PATH, and installs the
# makereye-render systemd service (watch mode). With a watch-directory
# argument the service is configured and started; without one, the
# config file is left for you to edit before enabling.
#
# Safe to re-run; never overwrites an edited /etc/makereye-render.conf.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WATCH_DIR="${1:-}"
CONF=/etc/makereye-render.conf

log() { echo "==> $*"; }
die() { echo "ERROR: $*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "run as root: sudo $0 [watch-directory]"

log "installing dependencies (ffmpeg, jq)"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y --no-install-recommends ffmpeg jq

log "installing makereye-render to /usr/local/bin"
install -m 0755 "$REPO_ROOT/scripts/makereye-render.sh" /usr/local/bin/makereye-render

if [ ! -f "$CONF" ]; then
	log "writing $CONF"
	cat >"$CONF" <<EOF
# MakerEye remote renderer configuration.
# WATCH_DIR: the timelapse tree to watch -- the same directory the
# camera's timelapse.output_dir points at, mounted on this machine.
WATCH_DIR=${WATCH_DIR:-/mnt/makereye/timelapses}
# Seconds between scans (polling; inotify doesn't work across hosts on
# network shares).
POLL_INTERVAL=60
EOF
else
	log "keeping existing $CONF"
	if [ -n "$WATCH_DIR" ]; then
		log "NOTE: watch-directory argument ignored because $CONF exists; edit it instead"
	fi
fi

log "installing systemd unit"
install -m 0644 "$REPO_ROOT/systemd/makereye-render.service" /etc/systemd/system/makereye-render.service
systemctl daemon-reload

if [ -n "$WATCH_DIR" ]; then
	log "enabling and starting makereye-render.service (watching $WATCH_DIR)"
	systemctl enable --now makereye-render.service
	sleep 1
	systemctl --no-pager status makereye-render.service || true
else
	cat <<EOF

Renderer installed. Next steps:
  1. Edit $CONF (set WATCH_DIR to your mounted timelapse tree)
  2. sudo systemctl enable --now makereye-render.service

One-shot rendering works immediately, no service needed:
  makereye-render /path/to/timelapses/<job-id>-<name>

On the camera, set timelapse.auto_render: false and point
timelapse.output_dir at the shared directory.
EOF
fi
