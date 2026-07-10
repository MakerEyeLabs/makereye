#!/usr/bin/env bash
#
# MakerEye installer for Raspberry Pi OS Lite 64-bit.
#
# Safe to run more than once: it preserves any existing
# /etc/makereye/config.yaml, only installs the example config when none
# exists, and does not remove data on repeat runs.
#
# Usage:
#   sudo ./scripts/install.sh [path-to-makereye-binary]
#
# If no binary path is given, this script looks for ./bin/makereye
# (built via `make build-arm64` or `make build`).

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MAKEREYE_BIN="${1:-$REPO_ROOT/bin/makereye}"

GO2RTC_VERSION="v1.9.14"
GO2RTC_INSTALL_PATH="/usr/local/bin/go2rtc"

log()  { echo "==> $*"; }
warn() { echo "WARNING: $*" >&2; }
die()  { echo "ERROR: $*" >&2; exit 1; }

require_root() {
	if [ "$(id -u)" -ne 0 ]; then
		die "this installer must be run as root (try: sudo $0)"
	fi
}

check_platform() {
	if [ ! -r /etc/os-release ]; then
		warn "cannot read /etc/os-release; skipping OS check"
		return
	fi
	# shellcheck disable=SC1091
	. /etc/os-release
	if [ "${ID:-}" != "debian" ] && [ "${ID_LIKE:-}" != "debian" ]; then
		warn "this does not look like Debian/Raspberry Pi OS (ID=${ID:-unknown}); continuing anyway"
	fi
}

detect_arch() {
	ARCH="$(uname -m)"
	case "$ARCH" in
	aarch64 | arm64)
		GO2RTC_ARCH="arm64"
		;;
	armv7l | armhf)
		GO2RTC_ARCH="arm"
		;;
	x86_64)
		GO2RTC_ARCH="amd64"
		;;
	*)
		die "unsupported architecture: $ARCH"
		;;
	esac
	log "detected architecture: $ARCH (go2rtc: $GO2RTC_ARCH)"
}

install_packages() {
	log "installing required apt packages"
	export DEBIAN_FRONTEND=noninteractive
	apt-get update -qq
	# rpicam-apps provides rpicam-vid/rpicam-hello (current camera
	# tooling; this installer does not use the legacy raspivid stack).
	# ca-certificates/curl are needed to fetch the go2rtc release.
	apt-get install -y --no-install-recommends \
		rpicam-apps \
		libcamera0.3 \
		ca-certificates \
		curl
}

check_camera_tools() {
	if ! command -v rpicam-vid >/dev/null 2>&1; then
		warn "rpicam-vid not found on PATH after package install; camera streaming will not work until this is resolved"
	fi
}

install_go2rtc() {
	if [ -x "$GO2RTC_INSTALL_PATH" ]; then
		log "go2rtc already present at $GO2RTC_INSTALL_PATH, skipping download"
		return
	fi
	log "downloading go2rtc $GO2RTC_VERSION for linux/$GO2RTC_ARCH"
	local url="https://github.com/AlexxIT/go2rtc/releases/download/${GO2RTC_VERSION}/go2rtc_linux_${GO2RTC_ARCH}"
	local tmp
	tmp="$(mktemp)"
	if ! curl -fsSL "$url" -o "$tmp"; then
		rm -f "$tmp"
		die "failed to download go2rtc from $url"
	fi
	install -m 0755 "$tmp" "$GO2RTC_INSTALL_PATH"
	rm -f "$tmp"
}

install_binary() {
	[ -x "$MAKEREYE_BIN" ] || die "makereye binary not found at $MAKEREYE_BIN (build it first: make build-arm64)"
	log "installing makereye binary to /usr/local/bin/makereye"
	install -m 0755 "$MAKEREYE_BIN" /usr/local/bin/makereye
}

create_user() {
	if ! getent group makereye >/dev/null; then
		log "creating group makereye"
		groupadd --system makereye
	fi
	if ! getent passwd makereye >/dev/null; then
		log "creating system user makereye"
		useradd --system --gid makereye --shell /usr/sbin/nologin \
			--home-dir /var/lib/makereye --no-create-home makereye
	fi
	if getent group video >/dev/null; then
		usermod -a -G video makereye
	fi
}

create_directories() {
	log "creating MakerEye directories"
	install -d -o makereye -g makereye -m 0750 /etc/makereye
	install -d -o makereye -g makereye -m 0750 /var/lib/makereye
	install -d -o makereye -g makereye -m 0750 /run/makereye
}

install_config() {
	local dest=/etc/makereye/config.yaml
	if [ -f "$dest" ]; then
		log "existing config found at $dest, leaving it untouched"
		return
	fi
	log "installing example config to $dest"
	install -o makereye -g makereye -m 0640 "$REPO_ROOT/config/config.example.yaml" "$dest"
}

install_systemd_unit() {
	log "installing systemd unit"
	install -m 0644 "$REPO_ROOT/systemd/makereye.service" /etc/systemd/system/makereye.service
	systemctl daemon-reload
}

enable_service() {
	log "enabling and starting makereye.service"
	systemctl enable --now makereye.service || {
		warn "makereye.service did not start cleanly; check 'systemctl status makereye' and 'journalctl -u makereye'"
	}
}

print_next_steps() {
	cat <<EOF

MakerEye installation complete.

Config file:     /etc/makereye/config.yaml
Service:         makereye.service
Logs:             journalctl -u makereye -f

Next steps:
  1. Edit /etc/makereye/config.yaml (camera resolution, framerate, stream name, etc.)
  2. Restart to apply changes: sudo systemctl restart makereye
  3. Check status:              makereye status
  4. Validate config:           makereye validate-config

See README.md for stream URLs and hardware validation steps.
EOF
}

main() {
	require_root
	check_platform
	detect_arch
	install_packages
	check_camera_tools
	install_go2rtc
	install_binary
	create_user
	create_directories
	install_config
	install_systemd_unit
	enable_service
	print_next_steps
}

main "$@"
