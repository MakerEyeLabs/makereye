#!/usr/bin/env bash
#
# MakerEye uninstaller.
#
# By default this removes the makereye binary, systemd unit, and go2rtc
# binary, but LEAVES /etc/makereye and /var/lib/makereye in place so
# configuration and any generated state survive a reinstall.
#
# Usage:
#   sudo ./scripts/uninstall.sh            # keep config and state
#   sudo ./scripts/uninstall.sh --purge    # also remove config, state, and the makereye user

set -euo pipefail

PURGE=0
if [ "${1:-}" = "--purge" ]; then
	PURGE=1
fi

log()  { echo "==> $*"; }

require_root() {
	if [ "$(id -u)" -ne 0 ]; then
		echo "ERROR: this uninstaller must be run as root (try: sudo $0)" >&2
		exit 1
	fi
}

stop_service() {
	if systemctl list-unit-files makereye.service >/dev/null 2>&1; then
		log "stopping and disabling makereye.service"
		systemctl disable --now makereye.service 2>/dev/null || true
	fi
}

remove_unit() {
	if [ -f /etc/systemd/system/makereye.service ]; then
		log "removing systemd unit"
		rm -f /etc/systemd/system/makereye.service
		systemctl daemon-reload
	fi
}

remove_binaries() {
	log "removing makereye and go2rtc binaries"
	rm -f /usr/local/bin/makereye
	rm -f /usr/local/bin/go2rtc
}

purge_data() {
	if [ "$PURGE" -ne 1 ]; then
		log "keeping /etc/makereye and /var/lib/makereye (pass --purge to remove them)"
		return
	fi
	log "purging /etc/makereye, /var/lib/makereye, /run/makereye"
	rm -rf /etc/makereye /var/lib/makereye /run/makereye
	if getent passwd makereye >/dev/null; then
		log "removing makereye user and group"
		userdel makereye 2>/dev/null || true
		groupdel makereye 2>/dev/null || true
	fi
}

main() {
	require_root
	stop_service
	remove_unit
	remove_binaries
	purge_data
	log "makereye uninstall complete"
}

main "$@"
