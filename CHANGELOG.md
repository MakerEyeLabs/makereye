# Changelog

All notable changes to MakerEye are recorded here. Format loosely follows
[Keep a Changelog](https://keepachangelog.com/); versioning will follow
SemVer once tagged releases begin.

## [Unreleased]

### Added

- Milestone 4: lighting framework (`internal/lighting`) with the Wyze
  Cam v3 Spotlight Kit as the first backend. Configured under
  `lighting:` as a list of typed lights; controlled via
  `makereye light <name> <0-255|on|off>` and, with MQTT enabled, as
  dimmable Home Assistant light entities. Advisory subsystem: unplugged
  lights are logged and recover on the next command. The Go backend
  disables tty output post-processing, fixing frame corruption for
  brightness levels containing byte 0x0A that the reference shell
  script couldn't send.
- `scripts/update.sh`: single-command update for a device installed
  from a git checkout — pulls the current branch, rebuilds, reinstalls
  via `install.sh` (so new dependencies are picked up), restarts the
  service, and prints status. Never touches an existing config.
- Milestone 3: optional MQTT + Home Assistant integration
  (`internal/mqtt`). With `mqtt.enabled` and a broker configured,
  MakerEye publishes availability (LWT), state/telemetry, and appears
  in Home Assistant automatically via MQTT discovery: stream and Prusa
  Connect switches, restart buttons, and status sensors, all driving
  the same daemon internals as the CLI. Advisory subsystem: a down
  broker is retried in the background and never affects streaming.
  First dependency beyond yaml.v3: `eclipse/paho.mqtt.golang`.
- `scripts/spotlight_ctl.sh`: standalone brightness control (0-255) for a
  Wyze Cam v3 Spotlight Kit accessory driven directly over the Pi's USB
  OTG port. Not wired into `config.yaml`/the daemon, an experiment kept
  as a utility script; see `ROADMAP.md`'s Milestone 4 (Lighting) for
  the planned native integration.
- Milestone 2: Prusa Connect snapshot uploads. `internal/prusaconnect`
  captures a JPEG from go2rtc's own snapshot endpoint and PUTs it to
  Prusa Connect on an interval (`prusa_connect.token`/`fingerprint`/
  `interval_seconds`). CLI: `prusa start/stop/restart`; `makereye status`
  reports upload/failure counts; `makereye validate-config` reports
  enabled/disabled without printing the token. Token stored as plaintext
  in `config.yaml` for the same reason as `go2rtc.auth.password` (Prusa
  Connect needs the literal credential). Validated against a real Prusa
  Connect account/camera, see `ROADMAP.md`.
- Milestone 1: optional `go2rtc.auth.username`/`password` config, passed
  through to go2rtc's own RTSP and HTTP API (WebRTC/MJPEG/snapshot) auth
  for LAN-exposed setups. Stored as plaintext by design (go2rtc needs the
  literal credential to authenticate clients, not a hash of it); both
  `config.yaml` and the generated go2rtc config remain
  `0640 makereye:makereye`. `makereye status` prints URLs with
  credentials embedded (noted as sensitive output);
  `makereye validate-config` reports auth on/off without the password.
- Milestone 0: repository foundation, Go module
  (`github.com/MakerEyeLabs/makereye`), GPLv3 license, `.gitignore`,
  README/DESIGN/ROADMAP docs, example config, systemd unit, install/
  uninstall scripts, Makefile, build-time version support.
- Milestone 1: camera streaming, `makereye` daemon manages a Raspberry
  Pi Camera Module 3 pipeline (`rpicam-vid`) exposed via go2rtc (RTSP,
  WebRTC, MJPEG, snapshot). CLI: `run`, `version`, `validate-config`,
  `status`, `stream start/stop/restart`. Config validation with
  actionable errors. Bounded restart behavior for the go2rtc supervisor.
- `scripts/bootstrap.sh`, installs build-time dependencies (git, make, a
  Go toolchain matching `go.mod`) missing from stock Raspberry Pi OS
  Lite, so a fresh Pi can go from `apt-get install git` to a built
  binary without manually chasing down each prerequisite.
- `scripts/quickstart.sh`, chains a clone/pull of this repo with
  `bootstrap.sh`, `make build`, and `install.sh` so the whole install is
  a single `curl | sudo bash` command; re-running it pulls the latest
  commit instead of re-cloning.

### Changed

- Restructured `ROADMAP.md`: added explicit design priorities (everyday
  use without a terminal — one-time SSH setup is fine, Home Assistant
  as the automation surface, lighting as part of image quality with a
  pluggable-backend framework), rewrote the contributor guidelines
  around keeping `main` releasable with feature work in branches,
  reordered the remaining milestones (MQTT/Home Assistant → lighting →
  manual timelapse → PrusaLink timelapse → web UI → motion → AI), and
  moved the "future research questions" into the milestones they belong
  to. Placeholder config sections were renumbered to match.

### Fixed

- `scripts/install.sh` was missing `ffmpeg` from the installed apt
  packages. go2rtc shells out to it for several internal code paths
  (transcoding, some snapshot/recording sources) even though MakerEye's
  own `exec:rpicam-vid` source doesn't call it directly; without it,
  those go2rtc code paths failed. Found during real hardware validation.

### Known limitations

- Not yet fully validated against real Raspberry Pi hardware, see
  `docs/NEXT_SESSION.md` and the "Hardware validation" section of
  `README.md`.
- go2rtc endpoints (RTSP/WebRTC/MJPEG/snapshot) have no authentication by
  default; default config binds them to loopback only. Optional
  username/password auth is available (see `README.md` "Network exposure
  and security") but is off unless explicitly configured.
- Milestones 3-9 (MQTT/Home Assistant, lighting, timelapses, PrusaLink,
  web UI, motion detection, AI monitoring) are not implemented; see
  `ROADMAP.md`.
