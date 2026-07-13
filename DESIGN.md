# MakerEye Design

This document records the architecture and design decisions for MakerEye,
for humans and for future Claude Code sessions picking up this project
without prior conversation history. It is a living document, update it
when architecture decisions change.

See `ROADMAP.md` for milestone status and `docs/DEVELOPMENT.md` for the
developer workflow.

## Product summary

MakerEye is a lightweight Go daemon that turns a Raspberry Pi Zero 2 W +
Camera Module 3 running stock Raspberry Pi OS Lite 64-bit into a
purpose-built monitoring appliance for a 3D printer (Prusa-first, not
Prusa-only). It deliberately stays small enough for one maintainer: no
plugin framework, no custom OS image, no NVR-scale feature set.

## Process ownership

**MakerEye launches and supervises go2rtc directly as a child process.**
This was chosen over the alternative (go2rtc as its own systemd unit,
configured by MakerEye) for Milestone 1:

- One systemd unit (`makereye.service`) is the only thing that needs to
  start correctly, be enabled, and be monitored by an operator or by
  `systemctl`. There is no second unit to keep in sync, no ordering
  dependency between two units to get wrong, and no window where go2rtc
  is running with a stale config MakerEye already moved past.
- MakerEye can react immediately to camera/stream configuration changes by
  restarting go2rtc itself, rather than shelling out to `systemctl
  restart go2rtc` (which would require sudo/policykit wiring for a
  non-root MakerEye process, or running MakerEye as root against systemd
  unnecessarily).
- `stream start/stop/restart` in the CLI has one clear owner to talk to:
  the running `makereye run` process, over a local control socket (see
  below), rather than needing to reconcile MakerEye's idea of state with
  systemd's.
- The go2rtc process is not exposed as an independently
  restart-on-boot-able systemd unit, which matches the actual usage
  pattern: go2rtc without MakerEye's generated config and supervision is
  not useful on its own in this appliance.

The tradeoff is that MakerEye's own crash takes go2rtc down with it (no
independent systemd `Restart=` for go2rtc). This is accepted: MakerEye's
process itself is a thin supervisor with a small state machine and no
long-running blocking work in its main loop, so it is not expected to
crash independently of go2rtc. `makereye.service` has
`Restart=on-failure`, so if the whole thing does go down, systemd brings
the pair back up together.

**Do not introduce a second service-management mechanism** (e.g. don't
also let go2rtc run as `go2rtc.service` in parallel with MakerEye trying
to launch it). If a future milestone needs go2rtc to survive a MakerEye
crash independently, that requires revisiting this decision explicitly
here, not adding an alternate path alongside it.

## Camera pipeline

```
rpicam-vid (child of go2rtc, via "exec:" source)
    │  raw H.264 Annex-B over stdout pipe
    ▼
go2rtc (child of makereye)
    │  RTSP / WebRTC / MJPEG / snapshot
    ▼
clients (VLC, browser, Prusa Connect uploader in Milestone 2, etc.)
```

- MakerEye renders a go2rtc YAML config (`internal/go2rtc.Render`) whose
  single stream source is `exec:rpicam-vid <args>`. go2rtc launches and
  owns that subprocess itself; MakerEye never talks to rpicam-vid
  directly. This keeps camera access exclusive to a single process chain
  and avoids two tools fighting over `/dev/video*`.
- `rpicam-vid` is used, not the legacy `raspivid`. Raspberry Pi OS Lite
  64-bit ships `rpicam-apps`; `libcamera-vid` etc. are compatibility
  symlinks to the same tool on current images.
- The command line is built by `internal/camera.BuildArgs`, a pure
  function from `config.CameraConfig` to an argument list, this is what
  is unit tested (no hardware required); only the resulting subprocess
  execution is untested without a real Pi + camera.
- On a Raspberry Pi 5, go2rtc's wiki notes `rpicam-vid`'s default output
  format detection breaks and requires `--codec libav --libav-format
  mpegts` instead of raw `--codec h264 --inline`. MakerEye's initial
  target is the Pi Zero 2 W, where the simpler raw H.264 Annex-B pipe
  (what MakerEye generates) is the documented, working approach. If a Pi
  5 target is added later, this will need to become pipeline-model-aware.

## Relationship between MakerEye, rpicam-vid, and go2rtc

- MakerEye: process supervisor + config authority. Generates go2rtc's
  config from MakerEye's own YAML config, starts/stops/restarts the
  go2rtc process, reports health, serves the local control socket for the
  CLI.
- go2rtc: the actual streaming server. Owns the rpicam-vid subprocess,
  demuxes/repackages the H.264 stream for RTSP/WebRTC/MJPEG/snapshot
  consumption, handles client connections. MakerEye does not reimplement
  any of this.
- rpicam-vid: camera capture only. Never launched or managed by MakerEye
  directly, always a go2rtc-owned child process.

## Prusa Connect uploader

Unlike go2rtc, `internal/prusaconnect.Uploader` is **not a subprocess**,
it's a goroutine inside the MakerEye process itself (`Start`/`Stop`
manage a context-cancelled loop, not an `exec.Cmd`). There's no external
binary to supervise, restart-loop, or health-check, it's just a periodic
HTTP client, so the go2rtc `Supervisor`'s process-lifecycle machinery
(backoff schedule, crash-loop detection, `PhaseFailed`) doesn't apply and
wasn't reused: a failed upload just gets retried on the next
`interval_seconds` tick, logged via `State.LastError`/`FailureCount`,
never a "give up" state. This is deliberate: uploads are advisory, not
something camera streaming depends on, so there is nothing to protect by
escalating retry backoff the way a crash-looping subprocess would need.

Each tick does two HTTP calls:

1. `GET http://<go2rtc.http_listen>/api/frame.jpeg?src=<stream.name>` —
   go2rtc's own snapshot endpoint, the same one `makereye status` prints
   for external clients. This is always a same-host call to MakerEye's
   own configured `go2rtc.http_listen`, sent with HTTP Basic Auth
   whenever `go2rtc.auth` is set, exactly like an external client would
   need, rather than relying on go2rtc's separate "requests from
   localhost skip auth" behavior, which is keyed on the request's source
   address, not on whether go2rtc's listener happens to be bound to
   loopback, and so isn't reliable to depend on if `go2rtc.http_listen`
   is ever a LAN address rather than `127.0.0.1`.
2. `PUT https://webcam.connect.prusa3d.com/c/snapshot` — Prusa Connect's
   webcam ingestion endpoint (a fixed constant, not user-configurable;
   there's no self-hosted Prusa Connect to point at instead), with
   `token`/`fingerprint` headers and the JPEG body. Header names/values
   and the endpoint itself came from a working reference script the
   project owner had used previously against the real API, not from
   Prusa's primary documentation (see `ROADMAP.md`'s former "Future
   research questions" entry for this, now resolved).

## Configuration model

- Format: YAML, single file, default path `/etc/makereye/config.yaml`,
  overridable with `-config` on every subcommand.
- `internal/config.Config` is the schema. `config.Default()` returns
  built-in defaults; `config.Load(path)` reads the file, decodes onto a
  copy of the defaults (so unset fields keep sensible values), and
  validates.
- Unknown top-level YAML keys are a hard error (`yaml.Decoder.KnownFields
  (true)`), so a typo in a section name fails loudly instead of being
  silently ignored.
- Validation (`Config.Validate`) collects *all* problems it finds and
  returns them together in one actionable error, rather than stopping at
  the first one.
- MakerEye never rewrites the user's config file. There is no "migrate
  config on startup" behavior and none should be added without deliberate
  design (it's a common source of surprising diffs and lost comments in
  appliance-style tools).
- **Resolved (Milestone 1/2)**: `go2rtc.auth.password` and
  `prusa_connect.token` live directly in `config.yaml` as plaintext, not
  in a separate tighter-permissioned file as this note originally
  floated. Reasoning: both go2rtc and Prusa Connect need the literal
  credential value to authenticate (see "Security considerations"), so
  hashing was never on the table; the remaining question was file
  permissions, not encryption. `config.yaml` is `0640 makereye:makereye`,
  and in the standard install (`scripts/install.sh`'s `create_user`) the
  `makereye` group has exactly one member, the `makereye` user itself, so
  a 0640 group-readable file and a hypothetical 0600 owner-only file
  protect the same set of principals today. A separate secrets file would
  only pay for itself if something later adds other members to the
  `makereye` group, at which point this should be revisited explicitly,
  not before. Splitting config now, for a threat that doesn't exist in
  the current install path, is exactly the kind of premature complexity
  this project avoids elsewhere (see `config.yaml`'s "never rewrites the
  user's config file" rule above, one file is easier to reason about).
  Future subsystems with their own credentials (MQTT, PrusaLink) should
  make this same call explicitly rather than assuming it, if the
  `makereye` group's membership assumption changes, revisit here first.

## Service model

- `makereye.service` runs `makereye run -config /etc/makereye/config.yaml`
  in the foreground (`Type=simple`).
- `makereye run`:
  1. Loads and validates config (exits non-zero with an actionable
     message on failure, this is treated as a configuration error, not a
     transient one, and systemd's `Restart=on-failure` loop would just
     repeat the same failure, so operators should notice via
     `systemctl status` / `journalctl` rather than relying on the retry
     loop alone).
  2. Creates its runtime/state directories if missing.
  3. Starts the go2rtc supervisor (`internal/go2rtc.Supervisor`), which
     writes the generated go2rtc config and launches the process.
  4. Serves the control socket (`internal/ipc`) at
     `<run_dir>/control.sock` for CLI commands.
  5. Blocks until SIGTERM/SIGINT, then stops go2rtc (SIGTERM, then
     SIGKILL after a grace period) and exits.
- Bounded restart behavior: if the go2rtc process itself exits
  unexpectedly, the supervisor retries with a fixed backoff schedule (1s,
  2s, 5s, 10s, 30s) and gives up after 5 consecutive rapid restarts,
  entering a `failed` phase that requires an explicit `makereye stream
  restart`. This avoids a tight restart loop hammering the SD card or CPU
  on a Pi Zero 2 W. A restart interval longer than 2 minutes resets the
  counter, so a long-lived process that eventually crashes once is not
  treated as part of a crash loop.
- `makereye stream start/stop/restart` and `makereye status` are CLI
  commands that talk to the *running* daemon over a Unix domain socket
  control protocol (`internal/ipc`), not separate process managers. This
  is why they require the daemon to already be running (`systemctl status
  makereye`), see `docs/DEVELOPMENT.md` for the local dev-loop
  implications.

## State and filesystem layout

| Path                              | Purpose                                             | Owner            |
|------------------------------------|------------------------------------------------------|------------------|
| `/etc/makereye/config.yaml`        | User configuration                                   | `makereye:makereye`, 0640 |
| `/var/lib/makereye/go2rtc.yaml`    | Generated go2rtc config (rewritten on every start)   | `makereye:makereye` |
| `/var/lib/makereye/`               | Persistent state (future: timelapse output, etc.)    | `makereye:makereye`, 0750 |
| `/run/makereye/control.sock`       | CLI↔daemon control socket, recreated on every start  | `makereye:makereye`, 0660 |
| `/usr/local/bin/makereye`          | MakerEye binary                                       | root |
| `/usr/local/bin/go2rtc`            | go2rtc binary                                         | root |
| `/etc/systemd/system/makereye.service` | systemd unit                                     | root |

## Security considerations

- go2rtc's RTSP/WebRTC/MJPEG/snapshot endpoints have **no authentication
  by default**. The example config binds all of them to `127.0.0.1`
  only, which is the conservative default described in the install/
  README docs. Exposing them on the LAN (binding to `0.0.0.0` or a LAN
  address) is a deliberate user choice documented in `README.md`, not
  something MakerEye does by default.
- Optional auth (`go2rtc.auth.username`/`password` in `config.yaml`) is
  passed straight through to go2rtc's own HTTP Basic Auth (API) and RTSP
  auth, which MakerEye does not implement itself. The password is stored
  **as plaintext**, deliberately not hashed: go2rtc authenticates clients
  by comparing the submitted credential directly against this value and
  has no support for verifying a password hash, so hashing it in
  `config.yaml` would silently break authentication for every client
  rather than adding security. This is consistent with go2rtc's own
  upstream config, which also expects plaintext. Both `config.yaml` and
  the generated `/var/lib/makereye/go2rtc.yaml` are `0640
  makereye:makereye`; that filesystem permission, not a hash, is the
  actual protection on this credential at rest. See README.md
  "Network exposure and security" for user-facing guidance.
- `makereye.service` runs as a dedicated unprivileged `makereye` system
  user (in the `video` group for camera device access), not root.
  `NoNewPrivileges=yes` and `ProtectHome=yes` are set.
  `ProtectSystem=strict` plus an explicit `DeviceAllow=` list were
  considered but **not** used: the exact camera device nodes touched by
  libcamera/rpicam-vid vary across kernel/firmware versions, and an
  incomplete allow-list fails silently (camera just doesn't work, with a
  non-obvious cause). This should be revisited once validated against
  real hardware across a couple of Raspberry Pi OS releases.
- The control socket (`/run/makereye/control.sock`) is filesystem-permission
  protected (0660, `makereye:makereye`), not further authenticated. Any
  process running as the `makereye` user or root can issue stream or
  prusa start/stop/restart. This is acceptable for a single-tenant
  appliance.
- MakerEye does not shell out to arbitrary user-supplied commands.
  `internal/camera.BuildArgs` builds an argument list from typed,
  validated config fields (ints, bools, a closed set of enum strings) —
  there is no string concatenation of user input into a shell command.
  `exec.Command` is used with an explicit argv, not `sh -c`.
- `prusa_connect.token` (in `config.yaml`) is stored as plaintext, the
  same reasoning and file permissions as `go2rtc.auth.password` above
  (Prusa Connect's API needs the literal bearer token, hashing it would
  break every upload). See "Configuration model" above for why this
  lives in `config.yaml` rather than a separate secrets file.
- Logs go to stdout/stderr only (journald captures them). Config values
  are logged sparingly and never include anything secret-shaped:
  `cmd_validate.go` prints whether `go2rtc.auth`/`prusa_connect` are
  enabled but never the password/token, and `cmd_status.go` only embeds
  credentials in the go2rtc stream URLs it prints (flagged as sensitive
  output there), never in log output.

## Known hardware assumptions

- Target: Raspberry Pi Zero 2 W, Raspberry Pi Camera Module 3, Raspberry
  Pi OS Lite 64-bit (arm64), a single camera per device (`camera: 0`).
- `rpicam-vid` and `libcamera` are assumed to be installed via
  `rpicam-apps` from the Raspberry Pi OS apt repositories (the installer
  does this). No custom driver or DKMS work is assumed.
- The Pi Zero 2 W's quad-core Cortex-A53 is capable of hardware H.264
  encode via the camera ISP pipeline for 1080p30 at the example config's
  bitrate; MakerEye does not do its own encoding or transcoding in this
  milestone (that stays in rpicam-vid/go2rtc).
- No hardware testing has been performed by the Claude Code session that
  wrote this version of MakerEye, see `docs/NEXT_SESSION.md` for exactly
  what still needs to be validated on real hardware, and README.md for
  the manual validation checklist an operator should run.
