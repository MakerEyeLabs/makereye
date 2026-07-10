# Next Session Handoff

Written at the end of the Milestone 0 + Milestone 1 implementation
session. This should be enough context to continue without the original
prompt or conversation history, see also `DESIGN.md` (architecture) and
`ROADMAP.md` (milestone status/next research questions).

## What was implemented

**Milestone 0, Repository foundation**: Go module
(`github.com/MakerEyeLabs/makereye`, chosen because it's the repo's
actual `origin` remote, not a placeholder), GPLv3 `LICENSE` (already
present at session start), `.gitignore`, `README.md`, `DESIGN.md`,
`ROADMAP.md`, `CHANGELOG.md`, `config/config.example.yaml`,
`systemd/makereye.service`, `scripts/install.sh` /
`scripts/uninstall.sh`, `Makefile`, build-time version support
(`internal/version`, wired via `-ldflags` in the Makefile), tests.

**Milestone 1, Camera streaming**:

- `internal/config`, YAML schema/defaults/validation
  (`config.Default()`, `config.Load(path)`, `Config.Validate()`).
  Unknown top-level YAML keys are a hard error. All validation problems
  are collected and returned together.
- `internal/camera`, pure function `BuildArgs(config.CameraConfig)
  []string` building the `rpicam-vid` argument list (raw H.264 Annex-B to
  stdout, current tooling, not legacy `raspivid`).
- `internal/go2rtc`, `Render(*config.Config) ([]byte, error)` generates
  go2rtc's YAML config (single `exec:rpicam-vid ...` stream source, RTSP/
  WebRTC/API listen addresses); `StreamURLs(*config.Config)` computes the
  four client-facing URLs; `Supervisor` launches/supervises the go2rtc
  process with bounded restart backoff (1s/2s/5s/10s/30s, gives up after
  5 consecutive rapid restarts → `PhaseFailed`) and an HTTP-based
  `HealthCheck`.
- `internal/ipc`, tiny line-delimited-JSON-over-Unix-socket protocol so
  the CLI can talk to the running daemon (`status`,
  `stream-start/stop/restart`).
- `internal/daemon`, wires config + supervisor + control socket +
  SIGTERM/SIGINT handling into `Daemon.Run(ctx)`.
- `cmd/makereye`, CLI using only the stdlib `flag` package (no
  third-party CLI framework): `run`, `version`, `validate-config`,
  `status`, `stream start/stop/restart`.

## Important decisions

1. **Process model**: MakerEye launches and supervises go2rtc directly as
   a child process (not a second systemd unit). Full reasoning in
   `DESIGN.md` § Process ownership. Do not add a second
   service-management path for go2rtc without revisiting this decision
   there.
2. **Go module path**: `github.com/MakerEyeLabs/makereye`, this is the
   repo's real `git remote origin`, not an invented placeholder, so no
   later "replace the module path" step should be needed.
3. **CLI↔daemon communication**: a Unix domain socket at
   `<run_dir>/control.sock` with a minimal JSON request/response
   protocol (`internal/ipc`), rather than the CLI managing a second
   process or talking to systemd. This is the same pattern later
   milestones (Prusa uploader start/stop, etc.) should reuse rather than
   inventing a different mechanism per subsystem.
4. **go2rtc exec source**: raw H.264 Annex-B pipe (`--codec h264
   --inline -o -`), which is the documented/working approach for the
   Pi Zero 2 W target. Note in `DESIGN.md`: this is known to need
   `--codec libav --libav-format mpegts` instead on a Raspberry Pi 5 —
   irrelevant to the current target, but relevant if hardware scope ever
   expands.
5. **Config secrets**: no secrets exist in Milestone 1's config. Decision
   recorded in `DESIGN.md` for Milestone 2/3: prefer a separate
   tighter-permission file over broadening `config.yaml`'s exposure, when
   that's actually designed.
6. **Systemd hardening**: deliberately did NOT use `ProtectSystem=strict`
   + `DeviceAllow=` allow-listing for camera devices, because getting
   that list wrong silently breaks the camera and the exact device nodes
   vary by kernel/firmware. Used moderate hardening instead
   (`NoNewPrivileges`, `ProtectHome`, `ProtectSystem=full`, `video` group
   membership). Revisit once tested on real hardware.

## Files that deserve review

- `internal/go2rtc/supervisor.go`, the restart/backoff/health-check
  state machine is the most complex piece of new logic in this
  milestone; worth a careful read if anything about stream reliability
  is reported as buggy later.
- `internal/camera/args.go`, the exact `rpicam-vid` flags chosen
  (`--inline`, `--codec h264`, bitrate-in-bps conversion, autofocus
  flags). These were derived from go2rtc's documented Raspberry Pi exec
  patterns via web research this session, not from hands-on hardware
  testing, see "Hardware tests still required" below.
- `scripts/install.sh`, the go2rtc download URL/version
  (`v1.9.14`, asset name `go2rtc_linux_arm64`) was set from web research
  this session; the exact asset filename convention could not be
  double-confirmed via the GitHub API from this sandboxed environment
  (API access to that repo was blocked by the environment's network
  policy). If `install.sh` fails to download go2rtc, check the actual
  asset names at https://github.com/AlexxIT/go2rtc/releases and fix the
  URL construction in `install_go2rtc()`.
- `systemd/makereye.service`, hardening directives in particular; see
  decision #6 above.

## Commands that were run

```sh
go mod init github.com/MakerEyeLabs/makereye
go get gopkg.in/yaml.v3
go mod tidy
gofmt -l .           # (fixed one formatting issue, then clean)
go vet ./...         # clean
go build ./...        # clean
go test ./...         # all passing (see below)
make build-arm64      # cross-compile smoke test, succeeded
```

Plus a manual end-to-end smoke test: built the CLI, pointed it at a temp
config with a fake `go2rtc` binary (a shell script standing in for the
real one, since no camera hardware is available in this environment),
and exercised `validate-config`, `run` (daemon), `status`, `stream stop`,
`stream start`, `stream restart`, and `SIGTERM` shutdown, all behaved as
designed (see transcript in session history / rerun anytime with a
similar throwaway script).

## Test results

`go test ./...`, all packages pass:

- `internal/camera`, argument-building tests (resolution/framerate/
  bitrate flags, rotation/flip flags, all three autofocus modes,
  single-line command output).
- `internal/config`, default validity, 10 invalid-value cases, YAML
  load with overrides, unknown-field rejection, missing-file error.
- `internal/go2rtc`, YAML render structure/content, nil-config
  rejection, stream URL construction, and (using a fake binary in place
  of real go2rtc) supervisor start/stop, double-start rejection,
  crash-loop → `PhaseFailed` transition, and restart producing a new PID.
- `internal/ipc`, serve/call round trip for a known and an unknown
  command, and connecting to a nonexistent socket.

`cmd/makereye`, `internal/daemon`, and `internal/version` have no unit
tests (thin wiring code, covered instead by the manual end-to-end smoke
test above).

## Hardware tests still required

None of these have been run, they require a real Raspberry Pi Zero 2 W
+ Camera Module 3 running Raspberry Pi OS Lite 64-bit. Full checklist is
in `README.md` § Hardware validation; summary:

1. `rpicam-hello --list-cameras` detects the Camera Module 3.
2. `rpicam-vid -t 5000 -o test.h264` produces valid output directly.
3. `scripts/install.sh` completes and `makereye.service` reaches
   `active (running)`.
4. RTSP playback (`ffplay`/VLC) actually shows live video.
5. WebRTC playback via go2rtc's built-in web UI works.
6. MJPEG endpoint returns a valid multipart JPEG stream.
7. Snapshot endpoint returns a valid single JPEG.
8. `stream stop/start/restart` actually stop/resume the real video feed,
   not just the process-supervision state machine (which *is* tested,
   just not against the real rpicam-vid → go2rtc pipeline).
9. Reboot persistence.
10. Failure/restart behavior with the real go2rtc binary missing or
    misconfigured.

## Known problems

- go2rtc release version/asset naming in `scripts/install.sh` is
  best-effort from web research, not independently verified against the
  live GitHub API from this environment (network policy blocked direct
  API access to that repo). Verify before relying on the installer as-is.
- No hardware validation at all yet (see above), treat all camera/
  streaming behavior as "implemented per go2rtc/rpicam-vid documentation,
  not yet observed working."
- `systemd/makereye.service`'s hardening directives are a reasonable
  starting point but untested against real libcamera device access
  patterns; may need loosening (or could safely be tightened further)
  once observed on hardware.
- The `AutofocusMode`/`lens_position` mapping to `rpicam-vid` flags
  (`--autofocus-mode`, `--lens-position`) was not verified against a real
  Camera Module 3; flag names/semantics should be double-checked against
  `rpicam-vid --help` on-device.

## Recommended scope for the next session

Two reasonable options, in order of what's likely most valuable:

1. **Hardware validation pass** (no new features): work through the
   "Hardware tests still required" checklist above on real Pi Zero 2 W +
   Camera Module 3 hardware, fix whatever it surfaces (likely candidates:
   `rpicam-vid` flag mismatches, go2rtc asset naming, systemd hardening
   too strict for camera device access), and update `DESIGN.md`/
   `README.md`/`CHANGELOG.md` to reflect what's now actually verified
   rather than "implemented but unverified."
2. **Milestone 2, Prusa Connect uploads**, once Milestone 1 is confirmed
   working on hardware (or if hardware access genuinely isn't available
   to the next session either, and the user prefers to keep making
   software progress in parallel). Start with the "Future research
   questions" in `ROADMAP.md` (Prusa Connect's exact snapshot upload API,
   auth/registration flow), that research was explicitly deferred from
   this session. Reuse the `internal/ipc` control-socket pattern for the
   uploader's own start/stop/restart rather than inventing a new
   mechanism.

Do not start Milestone 3+ before Milestone 2 is at least functionally
complete, per `ROADMAP.md`'s one-milestone-at-a-time rule.
