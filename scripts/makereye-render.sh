#!/usr/bin/env bash
#
# MakerEye remote timelapse renderer: render MakerEye timelapse jobs on
# a machine with real CPU instead of the capture device. Point the Pi's
# timelapse.output_dir at a network share (and set auto_render: false),
# mount the same share here, and either render one job:
#
#   makereye-render /mnt/makereye/timelapses/<job-id>-<name>
#
# or watch the whole tree and render jobs automatically as their
# capture finishes (see install-renderer.sh for the systemd service):
#
#   makereye-render --watch /mnt/makereye/timelapses [--interval 60]
#
# Watch mode POLLS rather than using inotify, deliberately: inotify
# events are not delivered for changes made by other hosts on NFS/SMB
# mounts, and this tree's whole point is living on a network share.
#
# Reads each job's job.json manifest (jq), renders frames/%08d.jpg with
# ffmpeg at the job's playback_fps, validates the output, and updates
# the manifest (phase complete + output_file) the same atomic
# temp-file-then-rename way MakerEye itself does. Frames are never
# deleted. Eligible phases in watch mode: stopped, interrupted,
# capture_failed. A job that fails to render is marked render_failed
# and is NOT retried automatically (rerun it explicitly in one-shot
# mode) so a bad job can't hot-loop the watcher.
#
# Dependencies: jq, ffmpeg.

set -euo pipefail

INTERVAL=60
WATCH_DIR=""
JOB_DIR=""
ENCODER="${ENCODER:-libx264}"
PRESET="${PRESET:-veryfast}"

log() { echo "[makereye-render] $*"; }
die() { echo "[makereye-render] ERROR: $*" >&2; exit 1; }

usage() {
	sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'
	exit 2
}

while [ $# -gt 0 ]; do
	case "$1" in
	--watch)
		WATCH_DIR="${2:?--watch requires a directory}"
		shift 2
		;;
	--interval)
		INTERVAL="${2:?--interval requires seconds}"
		shift 2
		;;
	-h | --help)
		usage
		;;
	*)
		JOB_DIR="$1"
		shift
		;;
	esac
done

command -v jq >/dev/null || die "jq is required (apt-get install jq)"
command -v ffmpeg >/dev/null || die "ffmpeg is required (apt-get install ffmpeg)"
[ -n "$WATCH_DIR" ] || [ -n "$JOB_DIR" ] || usage

sanitize() {
	local s
	s=$(echo "$1" | tr '[:upper:]' '[:lower:]' | tr ' ' '-' | tr -cd 'a-z0-9_-' | cut -c1-40)
	echo "${s:-timelapse}"
}

# update_manifest DIR JQ_PROGRAM [--arg k v ...]
update_manifest() {
	local dir="$1" prog="$2"
	shift 2
	local tmp
	tmp=$(mktemp "$dir/.job.json.XXXX")
	if jq "$@" "$prog" "$dir/job.json" >"$tmp"; then
		mv "$tmp" "$dir/job.json"
	else
		rm -f "$tmp"
		log "WARNING: failed to update manifest in $dir"
	fi
}

# render_job DIR FORCE -> 0 rendered, 1 skipped, 2 failed
render_job() {
	local dir="$1" force="$2"
	local manifest="$dir/job.json"
	[ -f "$manifest" ] || { log "no job.json in $dir"; return 1; }

	local phase name fps outfile
	phase=$(jq -r '.phase // "unknown"' "$manifest")
	name=$(jq -r '.name // "timelapse"' "$manifest")
	fps=$(jq -r '.playback_fps // 30' "$manifest")
	outfile=$(jq -r '.output_file // ""' "$manifest")
	[ -n "$outfile" ] || outfile="$(sanitize "$name").mp4"

	case "$phase" in
	capturing | rendering)
		log "skip $dir: job is $phase"
		return 1
		;;
	complete)
		if [ "$force" != force ] && [ -s "$dir/$outfile" ]; then
			return 1 # already rendered; silent in watch mode
		fi
		;;
	render_failed)
		if [ "$force" != force ]; then
			log "skip $dir: render_failed (rerun explicitly: makereye-render $dir)"
			return 1
		fi
		;;
	esac

	local count
	count=$(find "$dir/frames" -maxdepth 1 -name '[0-9]*.jpg' 2>/dev/null | wc -l)
	if [ "$count" -eq 0 ]; then
		log "skip $dir: no frames"
		return 1
	fi

	# Best-effort lock so two renderers (or a watcher + a manual run)
	# don't collide; stale locks (>4h) are broken automatically.
	local lock="$dir/.render.lock"
	if ! mkdir "$lock" 2>/dev/null; then
		if [ -n "$(find "$lock" -maxdepth 0 -mmin +240 2>/dev/null)" ]; then
			log "breaking stale render lock in $dir"
			rmdir "$lock" 2>/dev/null || true
			mkdir "$lock" 2>/dev/null || { log "skip $dir: locked"; return 1; }
		else
			log "skip $dir: another render holds the lock"
			return 1
		fi
	fi
	trap 'rmdir "$lock" 2>/dev/null || true' RETURN

	log "rendering $dir ($count frames @ ${fps}fps -> $outfile)"
	local tmp_out="$dir/.render-tmp.mp4"
	if ! ffmpeg -hide_banner -loglevel error -y \
		-framerate "$fps" -i "$dir/frames/%08d.jpg" \
		-c:v "$ENCODER" -preset "$PRESET" -pix_fmt yuv420p \
		-movflags +faststart "$tmp_out"; then
		rm -f "$tmp_out"
		log "render FAILED for $dir (frames preserved)"
		update_manifest "$dir" '.phase = "render_failed" | .last_error = "remote render failed (see renderer logs)"'
		return 2
	fi
	if [ ! -s "$tmp_out" ]; then
		rm -f "$tmp_out"
		log "render FAILED for $dir: ffmpeg produced no output"
		update_manifest "$dir" '.phase = "render_failed" | .last_error = "remote render produced no output"'
		return 2
	fi
	mv "$tmp_out" "$dir/$outfile"
	update_manifest "$dir" '.phase = "complete" | .output_file = $of | del(.last_error)' --arg of "$outfile"
	log "complete: $dir/$outfile"
	return 0
}

if [ -n "$JOB_DIR" ]; then
	[ -d "$JOB_DIR" ] || die "no such directory: $JOB_DIR"
	render_job "$JOB_DIR" force
	exit $?
fi

[ -d "$WATCH_DIR" ] || die "no such directory: $WATCH_DIR"
log "watching $WATCH_DIR (poll every ${INTERVAL}s)"
while true; do
	for manifest in "$WATCH_DIR"/*/job.json; do
		[ -f "$manifest" ] || continue
		render_job "$(dirname "$manifest")" auto || true
	done
	sleep "$INTERVAL"
done
