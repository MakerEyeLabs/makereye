# MakerEye Roadmap

This tracks milestone status, ordering, and what each milestone must
deliver to count as done. See `DESIGN.md` for architecture and
`docs/NEXT_SESSION.md` for the most recent session handoff.

## Design priorities

These apply across every milestone:

1. **Everyday use must not need a terminal.** One-time setup and
   configuration over SSH is fine (install, editing `config.yaml`);
   day-to-day operation is not allowed to require a shell. Milestones 1
   and 2 already meet this bar: once configured, streaming and Prusa
   Connect uploads just run, and you watch them in VLC, a browser, or
   the Prusa Connect dashboard. The rule for new milestones: any
   control an operator is expected to touch routinely (start a
   timelapse, turn a light on) ships with a no-shell interface
   (MQTT/Home Assistant, and later the local web UI) in the same
   milestone — "CLI now, MQTT later" deferrals are not allowed.
2. **Home Assistant integration is a focus — and strictly optional.**
   Core features (streaming, Prusa Connect uploads, lighting,
   timelapses) work out of the box with no Home Assistant and no MQTT
   broker; HA integration is a first-class way to operate and automate
   MakerEye, never a requirement for it. Within that boundary, HA is
   the preferred automation surface: behaviors that *compose* MakerEye's
   features (lighting schedules, notifications, custom triggers) belong
   in HA automations over the entities MakerEye exposes, not
   reimplemented inside MakerEye — but when something is core rather
   than compositional, it gets built in and the HA recipe becomes an
   alternative, not the only path (see Milestone 6 for an example of
   this split).
3. **Lighting is part of image quality.** Snapshots, streams, and
   timelapses are only as good as the scene lighting, so lighting
   control is a first-class (optional) subsystem with pluggable
   backends (see Milestone 4), not an afterthought. The first supported
   light is the Wyze Cam v3 Spotlight Kit — an off-the-shelf, cheap
   (~$8 on sale as of mid-2026), plug-and-play USB accessory whose
   protocol is already reverse-engineered
   (`scripts/spotlight_ctl.sh`); GPIO-driven lights and addressable LED
   strips are planned backend types.

## Contributor guidelines (human or agent)

- **`main` stays releasable.** Every commit on `main` builds, passes
  `make check`, and keeps the docs consistent (README / DESIGN /
  CHANGELOG / example config updated together with the code). Partial
  and exploratory work lives in branches, not on `main`.
- **One milestone per branch/PR.** Don't mix milestones in one change,
  and don't land runtime code for a later milestone while working on an
  earlier one. Config placeholders for future milestones (a section that
  parses and validates but does nothing) are fine and encouraged, so the
  schema doesn't churn.
- **A milestone is complete when**: its features are implemented and
  tested; docs are updated; behavior is validated on real hardware (or
  the unvalidated parts are explicitly flagged as such); and it
  satisfies the everyday-use usability rule in "Design priorities".

## Milestone 0 — Repository foundation — ✅ DONE

- Go module (`github.com/MakerEyeLabs/makereye`), GPLv3 license,
  `.gitignore`, README/DESIGN/ROADMAP/CHANGELOG, example config, systemd
  unit, install/uninstall scripts, Makefile, build-time version support,
  focused tests.
- Added later: `scripts/bootstrap.sh` (build deps for stock Raspberry Pi
  OS Lite) and `scripts/quickstart.sh` (single `curl | sudo bash`
  clone/build/install).

## Milestone 1 — Camera streaming — ✅ DONE

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
- Optional `go2rtc.auth.username`/`password` in `config.yaml`, passed
  through to go2rtc's own RTSP/HTTP API auth, for LAN-exposed setups.
  Password is stored as plaintext deliberately (see `DESIGN.md`
  "Security considerations" for why hashing it would break auth
  entirely).
- **Hardware validation status**: core path confirmed on a real Pi +
  Camera Module 3 (install, service startup, JPEG snapshots, SSH-tunnel
  viewing, LAN RTSP with auth challenge). The full checklist in
  `README.md` "Hardware validation" (reboot persistence, failure/restart
  drills) has not been executed end to end; treat those specific items
  as unverified.

## Milestone 2 — Prusa Connect uploads — ✅ DONE

- `internal/prusaconnect`: `Uploader` captures a JPEG from go2rtc's own
  `/api/frame.jpeg` snapshot endpoint and PUTs it to Prusa Connect's
  webcam ingestion endpoint on an interval (`prusa_connect.interval_seconds`,
  default 10s). Not a subprocess like go2rtc, an in-process goroutine
  loop (see `DESIGN.md` "Prusa Connect uploader").
- Credential flow is manual: no in-app registration/pairing. You create
  the camera in Prusa Connect's web UI (Cameras -> Add camera -> "Other
  camera"), which issues a token, and paste it into
  `prusa_connect.token`. `prusa_connect.fingerprint` is a self-chosen
  stable identifier, not issued by Prusa.
- Endpoint/headers (`PUT https://webcam.connect.prusa3d.com/c/snapshot`,
  `token`/`fingerprint` headers, `image/jpg` content-type) came from a
  working reference script the project owner had used previously against
  the real API, not primary Prusa documentation — the details weren't
  independently verified against Prusa's docs, only against a script
  known to work in practice (and now against MakerEye's own
  implementation working in practice).
- Upload state tracked and inspectable via `makereye status`
  (`prusa_connect: phase=... uploads=... failures=...`) and
  `makereye validate-config` (enabled/disabled + fingerprint, never the
  token).
- Independent start/stop/restart via `makereye prusa start/stop/restart`,
  following the same control-socket pattern as `stream start/stop/restart`
  (new `ipc.CmdPrusaStart/Stop/Restart`).
- Upload failures are logged and retried next interval; they never affect
  camera streaming or crash-loop the daemon, this is an advisory feature.
- `prusa_connect.token` stored as plaintext in `config.yaml` (like
  `go2rtc.auth.password`), see `DESIGN.md` "Security considerations" for
  why, and "Configuration model" for why it isn't split into a separate
  secrets file.
- **Validated against a real Prusa Connect account/camera** (MK4,
  fingerprint/token from a real "Add camera -> Other camera" pairing):
  uploads succeed and the camera registers correctly. One gotcha worth
  recording since it looked like a bug at first: Prusa Connect's web
  dashboard doesn't display webcam footage while the paired printer is
  offline, even if uploads are succeeding, so "camera shows as
  registered but no image" during setup is expected if the printer
  itself isn't on, not a MakerEye or upload problem.

## Milestone 3 — MQTT + Home Assistant integration — ✅ DONE

The usability foundation: everything that was CLI-only (stream
start/stop/restart, prusa start/stop/restart, status) is now operable
from Home Assistant, and later milestones plug new features into the
same plumbing instead of building their own.

- `internal/mqtt.Bridge`: optional advisory in-process subsystem (see
  `DESIGN.md` "MQTT + Home Assistant bridge"). NOT required for core
  operation — daemon starts and streams normally with the broker down;
  paho retries in the background indefinitely.
- Availability topic (retained + LWT), JSON status telemetry (stream
  phase/restarts, Prusa upload/failure counts), republished on every
  command (ack) and every 30s.
- Control mirroring the control-socket commands through the same daemon
  internals: stream and prusa switches (ON/OFF) + restart buttons.
  Prusa entities only published when `prusa_connect.enabled`.
- Home Assistant MQTT discovery (retained, re-published on every
  reconnect): one "MakerEye" device with switches, buttons, and
  sensors, availability wired so entities show unavailable when the
  daemon dies. HA camera entity needs no code — documented in README
  (point Generic Camera at the go2rtc URLs).
- Broker credentials in `config.yaml` under `mqtt:`, same plaintext
  policy and reasoning as the existing credentials.
- Unit-tested against a fake paho client (discovery payloads, command
  dispatch, ack republish, offline LWT behavior); daemon smoke-tested
  against an unreachable broker.
- **Validated against a real Home Assistant + Mosquitto setup**: device
  and all entities appeared via discovery with no HA-side steps,
  switches/buttons control the daemon, sensors track state. Setup
  finding worth recording: a "disconnected (retrying)" state against
  HA's Mosquitto add-on was missing credentials, the add-on rejects
  anonymous connections by default (per README's MQTT section).

## Milestone 4 — Lighting framework + Wyze Spotlight Kit — IMPLEMENTED, awaiting hardware validation

Lighting is a framework with pluggable backends, not a one-off Wyze
integration. The deliverable is the framework plus one working backend
(the Wyze Spotlight Kit), with the config shaped so more backend types
can be added later without breaking existing setups.

- `internal/lighting`: `Manager` (named lights, last-commanded-state
  tracking, "on" restores last non-zero brightness) + `Backend`
  interface (`SetBrightness(0-255)`), kept minimal until a real second
  backend needs more. See `DESIGN.md` "Lighting".
- `lighting:` config section: list of typed lights
  (`type: wyze_spotlight`, per-light `device` defaulting to
  `/dev/ttyUSB0`, `startup_brightness` applied at daemon start since
  the hardware is write-only).
- First backend: **Wyze Cam v3 Spotlight Kit** over USB serial —
  off-the-shelf, cheap (~$8 on sale as of mid-2026), plug-and-play, its
  power passthrough cable even powers the Pi Zero, protocol fully
  reverse-engineered (frame + 16-bit additive checksum, validated on
  real hardware). The Go backend also fixes a latent bug in the
  reference shell script: tty output post-processing is disabled so
  frames containing 0x0A aren't corrupted.
- Future backend types (later contributions, not this milestone): plain
  GPIO on/off, PWM-dimmed GPIO, addressable LED strips (WS2812 etc.).
- Advisory subsystem: unplugged/missing lights are logged and recover
  on the next command (device opened per write); startup-brightness
  failures tolerated; never affects streaming. Daemon shutdown leaves
  lights as-is so restarts/updates don't flap them.
- CLI: `makereye light <name> <0-255|on|off>` via the control socket
  (`ipc.CmdLightSet`).
- MQTT/HA (per the usability rule, same milestone): each configured
  light is a dimmable HA light entity via discovery (state + brightness
  topics, ON restores last brightness).
- `scripts/spotlight_ctl.sh` remains as the standalone/manual tool and
  protocol documentation.
- **Awaiting hardware validation**: real spotlight driven through the
  daemon (CLI + HA dimmer), including brightness levels whose frames
  contain 0x0A (e.g. 10 and 166), which the shell script couldn't send
  correctly. Flip to DONE once confirmed.

## Milestone 5 — Manual timelapse — NOT STARTED

- Start/stop jobs via CLI **and** MQTT/HA in the same milestone (per the
  usability rule — the old "CLI now, MQTT later" phrasing of this
  milestone is exactly what the rule exists to prevent).
- Periodic snapshots from the shared go2rtc pipeline (same
  `/api/frame.jpeg` pattern as `internal/prusaconnect` — not a second
  camera claim).
- Render with ffmpeg (shell out, don't reimplement encoding; ffmpeg is
  already installed by `scripts/install.sh`).
- Preserve source frames if rendering fails, so nothing is silently
  lost.
- Progress/completion reporting via `makereye status` and MQTT
  (HA sensor: current job, frame count, last render result).
- Storage design questions to answer at implementation time: output
  location under the state dir, SD-card wear and free-space guardrails,
  retention policy for frames and rendered files, how finished
  timelapses get off the device (the web UI milestone adds
  browse/download; until then, network file access or `scp`).
- Optional lighting hook (if Milestone 4's `lighting:` is configured):
  hold a configured brightness while a timelapse job is active, restore
  after.
- Config placeholder already present: `timelapse.enabled`.

## Milestone 6 — PrusaLink automatic timelapse — NOT STARTED

- Detect printer job state via PrusaLink's local API; auto start/stop
  Milestone 5 timelapse jobs around print jobs.
- Associate job metadata (filename, etc.) with output files.
- Scope note (per design priority #2): Home Assistant users can already
  get most of this once Milestones 3+5 exist, by driving MakerEye's
  timelapse MQTT commands from HA's own PrusaLink integration — document
  that recipe as part of this milestone. The on-device PrusaLink polling
  this milestone adds is for setups without Home Assistant, and must not
  fight with HA-driven control (last command wins, no flapping).
- Config placeholder already present: `prusalink.enabled`.
- **Research needed at implementation time**: PrusaLink's local API
  surface for job state (endpoints, auth, REST vs websocket, how job
  start/end is best detected).

## Milestone 7 — Local web interface — NOT STARTED

Moved ahead of motion/AI: it's the no-Home-Assistant answer to the
usability rule, and the last remaining reason to SSH in day-to-day is
editing `config.yaml`.

- Small appliance-style UI served by the daemon: status (stream, Prusa
  Connect, lighting, timelapse), live view (embed/link go2rtc's stream
  endpoints), config viewing and editing with the existing validation
  (reject-on-invalid, never silently rewrite), timelapse
  browsing/download.
- No heavy frontend framework without a clearly compelling reason —
  default assumption is server-rendered Go templates and a tiny amount
  of vanilla JS.
- Security design question to answer at implementation time: auth story
  for the UI (reuse `go2rtc.auth`? separate credential?) and safe
  defaults for its listen address (loopback-only by default, same as
  go2rtc, with the same documented LAN-exposure tradeoffs).

## Milestone 8 — Motion-triggered operation — NOT STARTED

- Scope note (per design priority #2): once MQTT exists, motion-driven
  behavior can often be composed in Home Assistant (or by pointing a
  motion-capable NVR like Frigate at MakerEye's RTSP stream). This
  milestone is for on-device detection where none of that infrastructure
  exists.
- Sustained motion as a generic "start a job" trigger (for non-Prusa
  maker equipment), publishing MQTT events HA can react to.
- Configurable idle timeout to stop.
- Stay focused on maker-equipment use cases, not general surveillance
  features (no face detection, no zones UI, etc.).
- Config placeholder already present: `motion.enabled`.
- **Research needed at implementation time**: detection approach —
  frame-diff on go2rtc's stream vs. a dedicated capture path, evaluated
  against whatever go2rtc/rpicam capabilities look like then, and
  against the Pi Zero 2 W's CPU budget while encoding.

## Milestone 9 — AI monitoring — NOT STARTED

- Local print-failure detection (spaghetti detection, etc.).
- Publish advisory events first (MQTT event → HA notification); local
  printer pause control is a later, explicitly-opt-in step, not part of
  the initial AI milestone.
- Config placeholder already present: `ai.enabled`.
- **Research needed at implementation time**: what actually runs
  acceptably on a Pi Zero 2 W (if anything) vs. requiring a more capable
  Pi or an off-device inference option — may change the hardware story
  for this milestone specifically.

## Candidate work, not yet scheduled

- **Update over MQTT/Home Assistant** (single-command update exists
  today: `scripts/update.sh` pulls/rebuilds/reinstalls/restarts). The
  full no-SSH version has a clean design but real moving parts, so it's
  recorded here rather than bolted onto Milestone 3:
  - HA's MQTT discovery supports an `update` entity: MakerEye would
    publish its installed version plus the latest available (upstream
    `main` commit, or a release tag once releases exist), and HA shows
    an "Update" card with an install button like any other device.
  - Privilege separation is the crux: the daemon runs as the
    unprivileged `makereye` user and must not gain root. Sketch: the
    daemon writes a trigger file under `/var/lib/makereye`; a
    root-owned `makereye-update.path` systemd unit watches it and
    starts a oneshot `makereye-update.service` that runs
    `scripts/update.sh`. No sudo rules, no polkit, auditable via
    journald.
  - "Latest available" needs a source of truth: polling the GitHub API
    or `git fetch` on a timer. Fits naturally with the roadmap's
    existing "install from published release binaries" future item,
    at which point this becomes "compare tags, download binary" with
    no on-device rebuild.
- **Scheduled auto-update** (systemd timer running `scripts/update.sh`
  nightly): trivially enabled once wanted, but deliberately not the
  default — during active development an unattended pull can break the
  camera while nobody is watching; user-triggered updates (button in
  HA per the above) are the intended model.
