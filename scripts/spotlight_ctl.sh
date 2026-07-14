#!/usr/bin/env bash
#
# Brightness control for a Wyze Cam v3 Spotlight Kit accessory, driven
# directly by a Raspberry Pi over its USB OTG port -- not a Wyze Cam.
#
# The Spotlight Kit ships with a power passthrough cable that can plug
# into the Pi's OTG port, powering the Pi Zero itself as well as the
# spotlight -- no separate power supply needed. This essentially
# replaces the Wyze Cam v3 with a Pi Zero: the spotlight itself shows up
# as a normal USB serial device (typically /dev/ttyUSB0) that Raspberry
# Pi OS's stock kernel drivers already handle, same as it would on the
# Wyze camera's own SoC.
#
# Protocol (reverse-engineered against real hardware; the on/off/low
# presets came from gtxaspec/wz_mini_hacks's spotlight_ctl.sh and
# documentation/notes.md, the full 0-255 brightness range and the
# checksum formula were worked out from there by testing):
#
#   aa 55 43 05 16 <brightness> 07 <checksum_hi> <checksum_lo>
#
#   brightness:    0x00 (off) - 0xff (max), sent as a single byte.
#   checksum:      16-bit big-endian sum of every preceding byte
#                   (0xaa + 0x55 + 0x43 + 0x05 + 0x16 + brightness + 0x07),
#                   confirmed the device actually validates this (a wrong
#                   checksum is silently ignored, not just decorative).
#
# This is standalone and not wired into MakerEye's daemon/config yet --
# see ROADMAP.md's Milestone 4 (Lighting) for the planned integration:
# a native lighting subsystem with CLI control and a dimmable Home
# Assistant light entity over MQTT.
#
# Usage:
#   ./scripts/spotlight_ctl.sh <brightness 0-255>
#   DEVICE=/dev/ttyUSB1 ./scripts/spotlight_ctl.sh 128

set -euo pipefail

DEVICE="${DEVICE:-/dev/ttyUSB0}"

if [[ $# -ne 1 ]]; then
	echo "Usage: $0 <brightness 0-255>" >&2
	exit 1
fi

brightness="$1"

if ! [[ "$brightness" =~ ^[0-9]+$ ]] ||
	((brightness < 0 || brightness > 255)); then
	echo "Brightness must be an integer from 0 to 255." >&2
	exit 1
fi

checksum=$((0x164 + brightness))
checksum_high=$(((checksum >> 8) & 0xFF))
checksum_low=$((checksum & 0xFF))

printf -v packet \
	'\\xAA\\x55\\x43\\x05\\x16\\x%02X\\x07\\x%02X\\x%02X' \
	"$brightness" \
	"$checksum_high" \
	"$checksum_low"

printf '%b' "$packet" >"$DEVICE"
