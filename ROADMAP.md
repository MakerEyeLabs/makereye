# MakerEye Roadmap

This tracks milestone status. See `DESIGN.md` for architecture and
`docs/NEXT_SESSION.md` for the most recent session handoff.

**Rule for contributors (human or agent): work on one milestone at a
time. Do not partially implement a later milestone while "just adding a
placeholder", config placeholders are fine (see below), runtime code for
future milestones is not.**

## Milestone 0, Repository foundation, ✅ DONE (this session)

- Go module (`github.com/MakerEyeLabs/makereye`), GPLv3 license,
  `.gitignore`, README/DESIGN/ROADMAP/CHANGELOG, example config, systemd
  unit, install/uninstall scripts, Makefile, build-time version support,
  focused tests.

## Milestone 1, Camera streaming, ✅ DONE (this session)

- `internal/config`: schema, defaults, validation, YAML load.
- `internal/camera`: rpicam-vid argument construction.
- `internal/go2rtc`: go2rtc config generation + process supervisor
  (bounded restarts, health check via HTTP).
- `internal/ipc`: Unix socket control protocol (CLI ↔ daemon).
- `internal/daemon`: ties config + supervisor + control socket + signal
  handling together.
- CLI: `run`, `version`, `validate-config`, `status`, `stream
  start/stop/restart`.
- systemd unit + install/uninstall scripts.
- **Not hardware-validated**, see `docs/NEXT_SESSION.md` for the exact
  checklist to run on a real Pi Zero 2 W + Camera Module 3.
- **Added after initial hardware validation**: optional
  `go2rtc.auth.username`/`password` in `config.yaml`, passed through to
  go2rtc's own RTSP/HTTP API auth, for LAN-exposed setups. Password is
  stored as plaintext deliberately (see `DESIGN.md` "Security
  considerations" for why hashing it would break auth entirely).

## Milestone 2, Prusa Connect uploads, NOT STARTED

- Capture snapshots from the shared go2rtc pipeline (likely via go2rtc's
  own snapshot HTTP endpoint, no need to reimplement JPEG capture).
- Upload to Prusa Connect on an interval.
- Support registration/manual credential flow per Prusa's documented API
  (not yet researched in depth, see "Future research questions" below).
- Track upload state/failures somewhere inspectable (`makereye status`?).
- Independent start/stop/restart of the uploader, likely following the
  same control-socket pattern as `stream start/stop/restart`.
- Config placeholder already present: `prusa_connect.enabled` (no runtime
  effect yet).

## Milestone 3, MQTT telemetry and control, NOT STARTED

- Optional MQTT client; MUST NOT be required for core operation.
- Availability + system telemetry topics.
- Stream/uploader control via MQTT commands, with acknowledgements.
- Optional Home Assistant MQTT discovery.
- Config placeholder already present: `mqtt.enabled`.

## Milestone 4, Manual timelapse, NOT STARTED

- Start/stop jobs via CLI (and later MQTT).
- Periodic snapshots from the shared camera pipeline (not a second camera
  claim).
- Render with ffmpeg (shell out, don't reimplement encoding).
- Preserve source frames if rendering fails, so nothing is silently lost.
- Progress/completion reporting.
- Config placeholder already present: `timelapse.enabled`.

## Milestone 5, PrusaLink automatic timelapse, NOT STARTED

- Detect printer job state via PrusaLink's local API.
- Auto start/stop timelapses around print jobs.
- Associate job metadata (filename, etc.) with output files.
- Config placeholder already present: `prusalink.enabled`.

## Milestone 6, Motion-triggered operation, NOT STARTED

- Sustained motion as a generic "start a job" trigger (for non-Prusa
  maker equipment).
- Configurable idle timeout to stop.
- Stay focused on maker-equipment use cases, not general surveillance
  features (no face detection, no zones UI, etc.).
- Config placeholder already present: `motion.enabled`.

## Milestone 7, AI monitoring, NOT STARTED

- Local print-failure detection (spaghetti detection, etc.).
- Publish advisory events first; local printer pause control is a later,
  explicitly-opt-in step, not part of the initial AI milestone.
- Config placeholder already present: `ai.enabled`.

## Milestone 8, Local web interface, NOT STARTED

- Small appliance-style status/config UI.
- No heavy frontend framework without a clearly compelling reason —
  default assumption is server-rendered Go templates or a tiny amount of
  vanilla JS.

## Future research questions (deliberately not investigated yet)

Recorded here per the Milestone 0/1 session's research boundaries, so the
next session doesn't have to rediscover that these are open:

- **Prusa Connect**: exact snapshot upload API (endpoint, auth headers,
  camera "fingerprint"/registration flow, rate limits). Needs primary-doc
  research at Milestone 2 time.
- **PrusaLink**: local API surface for job state (endpoints, auth,
  whether it's REST/websocket, how job start/end is best detected).
  Needed at Milestone 5.
- **Home Assistant MQTT discovery**: exact topic/payload conventions
  MakerEye should emit so entities show up correctly. Needed at
  Milestone 3.
- **AI model choice**: what actually runs acceptably on a Pi Zero 2 W (if
  anything) vs. requiring a more capable Pi for this feature, or an
  off-device inference option. Needed at Milestone 7, may change the
  hardware story for that milestone specifically.
- **Motion detection approach**: frame-diff on go2rtc's stream vs. a
  dedicated capture, needs revisiting against whatever go2rtc/rpicam
  capabilities look like by Milestone 6.
