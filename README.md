# MakerEye

MakerEye is a lightweight Raspberry Pi camera appliance for 3D printers
and other maker equipment. It turns a Raspberry Pi Zero 2 W + Camera
Module 3 running **stock** Raspberry Pi OS Lite 64-bit into a
purpose-built monitoring camera, no custom OS image, so you keep normal
Raspberry Pi functionality (Raspberry Pi Connect, apt updates, SSH,
standard camera tooling).

MakerEye is Prusa-first (Prusa Connect uploads, PrusaLink-triggered
timelapses) but its core camera/streaming functionality is not tied to
Prusa specifically.

**Current status: Milestone 0 (repository foundation), Milestone 1
(camera streaming), and Milestone 2 (Prusa Connect uploads) are
implemented. Later milestones (MQTT/Home Assistant, lighting,
timelapses, web UI, motion detection, AI monitoring) are documented in
`ROADMAP.md` but not yet implemented.** See `docs/NEXT_SESSION.md` for
exactly what has and hasn't been validated.

## What's implemented

- A `makereye` daemon that manages a Raspberry Pi Camera Module 3
  pipeline via `rpicam-vid` and exposes it through
  [go2rtc](https://github.com/AlexxIT/go2rtc): RTSP, WebRTC, MJPEG, and
  JPEG snapshots. Optional username/password auth on those endpoints.
- Periodic snapshot uploads to Prusa Connect, pulled from the same
  go2rtc pipeline.
- A small CLI (`run`, `version`, `validate-config`, `status`, `stream
  start/stop/restart`, `prusa start/stop/restart`).
- systemd integration (`makereye.service`) and an install script for
  fresh Raspberry Pi OS Lite installs.

## Target hardware and OS

- Raspberry Pi Zero 2 W
- Raspberry Pi Camera Module 3
- Raspberry Pi OS Lite 64-bit (Debian-based, `rpicam-apps` available via
  apt)

## Install (Raspberry Pi OS Lite, arm64)

Run directly on the Pi:

```sh
curl -fsSL https://raw.githubusercontent.com/MakerEyeLabs/makereye/main/scripts/quickstart.sh | sudo bash
```

This clones (or updates) MakerEye into `/opt/makereye-src`, installs
build prerequisites, builds the binary, and installs it as a systemd
service. It's safe to re-run (pulls the latest commit instead of
re-cloning, and never touches an existing config).

Stock Raspberry Pi OS Lite doesn't include git, make, or a Go toolchain
new enough to build MakerEye (`go.mod` requires Go 1.24.7+; Debian's
packaged `golang-go` is normally well behind that), `quickstart.sh`
installs all three along the way. Under the hood it's just:

```sh
sudo apt-get update && sudo apt-get install -y git   # only needed to clone
git clone https://github.com/MakerEyeLabs/makereye.git
cd makereye
sudo ./scripts/bootstrap.sh    # installs make + a Go toolchain matching go.mod
                                # (also installs git; redundant with the
                                # line above, which only exists to clone)
export PATH="$PATH:/usr/local/go/bin"   # or: source /etc/profile.d/makereye-go.sh
make build-arm64          # cross-compiles ./bin/makereye-linux-arm64,
                           # or build directly on the Pi with `make build`
sudo ./scripts/install.sh ./bin/makereye-linux-arm64
```

If you'd rather not pipe a script from the network straight into `sudo
bash`, run the individual steps above instead, they're what
`quickstart.sh` runs. `scripts/bootstrap.sh` and `scripts/install.sh`
are each independently safe to re-run.

`scripts/install.sh` (called by both paths above):

- installs `rpicam-apps` and other required apt packages,
- downloads a matching go2rtc release binary,
- installs the `makereye` binary,
- creates the `makereye` system user/group and `/etc/makereye`,
  `/var/lib/makereye`, `/run/makereye`,
- installs the example config to `/etc/makereye/config.yaml` **only if no
  config already exists there** (safe to re-run),
- installs and enables `makereye.service`.

It is safe to run more than once and never deletes an existing config.

**Future**: installing from a published GitHub release binary instead of
building from source. Not built out this session, see `ROADMAP.md`.

### Uninstall

```sh
sudo ./scripts/uninstall.sh            # keeps /etc/makereye and /var/lib/makereye
sudo ./scripts/uninstall.sh --purge    # also removes config, state, and the makereye user
```

## Configuration

Default path: `/etc/makereye/config.yaml` (override with `-config` on any
`makereye` subcommand). See `config/config.example.yaml` for a fully
commented example covering `device`, `camera`, `stream`, `go2rtc`,
`prusa_connect`, and `system`. Sections for future milestones (`mqtt`,
`timelapse`, `prusalink`, `motion`, `ai`) are accepted but have no runtime
effect yet.

```sh
makereye validate-config -config /etc/makereye/config.yaml
```

reports actionable errors (missing fields, out-of-range values, unknown
top-level keys) without starting anything.

## CLI

```
makereye run [-config path]              Run the MakerEye daemon in the foreground
makereye version                         Print version information
makereye validate-config [-config path]  Load and validate a config file
makereye status [-config path]           Show daemon and stream status
makereye stream start [-config path]     Start the camera stream
makereye stream stop [-config path]      Stop the camera stream
makereye stream restart [-config path]   Restart the camera stream
makereye prusa start [-config path]      Start Prusa Connect snapshot uploads
makereye prusa stop [-config path]       Stop Prusa Connect snapshot uploads
makereye prusa restart [-config path]    Restart Prusa Connect snapshot uploads
```

`prusa start`/`restart` fail if `prusa_connect.enabled` is `false` in
config; flip that to `true` (with `token`/`fingerprint` set) first.

`stream start/stop/restart` and `status` talk to the *running*
`makereye.service` daemon over a local control socket
(`/run/makereye/control.sock`), the daemon must already be running
(`systemctl status makereye`).

## Stream URLs

With the example config's stream name `camera` and default (loopback)
listen addresses, MakerEye's go2rtc backend exposes:

| Protocol | URL |
|----------|-----|
| RTSP     | `rtsp://127.0.0.1:8554/camera` |
| WebRTC   | `http://127.0.0.1:1984/api/webrtc?src=camera` |
| MJPEG    | `http://127.0.0.1:1984/api/stream.mjpeg?src=camera` |
| Snapshot | `http://127.0.0.1:1984/api/frame.jpeg?src=camera` |

`makereye status` prints these for your actual configured stream name and
listen addresses. go2rtc also serves a small built-in web UI at
`http://<webrtc_listen>/` useful for manually checking a stream while
testing on a Pi with a display, or over SSH port-forwarding.

### Network exposure and security

**The RTSP/WebRTC/MJPEG/snapshot endpoints have no authentication by
default.** The example config binds all of them to `127.0.0.1` only —
they are not reachable from your LAN by default. If you want to view the
stream from another device, either:

- SSH-tunnel to the Pi (`ssh -L 8554:localhost:8554 -L 1984:localhost:1984 pi@<host>`), or
- deliberately change `go2rtc.rtsp_listen` / `webrtc_listen` /
  `http_listen` in your config to a LAN-reachable address **and** set
  `go2rtc.auth.username` / `go2rtc.auth.password` (see below), or
- change the listen addresses without setting `auth`, understanding that
  anyone on that network segment can then view (and, for WebRTC/API,
  potentially reconfigure) the stream unauthenticated. Treat this the
  same as any other unauthenticated camera on your network, keep it on a
  trusted LAN/VLAN.

#### Authentication

Setting `go2rtc.auth.username` and `go2rtc.auth.password` in
`config.yaml` protects the RTSP endpoint and the HTTP API (WebRTC
signalling, MJPEG, snapshot) with a username/password, passed straight
through to go2rtc's own auth. Both fields must be set together (or both
left empty to disable auth, the default). `makereye validate-config`
shows whether auth is enabled (without printing the password);
`makereye status` prints the actual stream URLs with the credentials
embedded, so treat that command's output as sensitive.

The password is stored **as plaintext**, not hashed, in both
`config.yaml` and the go2rtc config MakerEye generates from it
(`/var/lib/makereye/go2rtc.yaml`). This isn't an oversight: go2rtc
authenticates clients by comparing the credential they send directly
against this value, and has no support for checking against a password
hash, so hashing it here would silently make every login fail. Both
files are already restricted to `0640 makereye:makereye` by the
installer; treat them like any other credential file (e.g. don't commit
`config.yaml` with a real password to a public repo, don't back it up
somewhere less trusted than the Pi itself).

If you want authentication without trusting this plaintext-storage
model, put a reverse proxy with its own auth in front instead (works for
the HTTP-based endpoints; RTSP is a different protocol and needs an
RTSP-aware proxy, not a plain HTTP one), or rely purely on network-level
restrictions (firewall rules, VLAN isolation, a WireGuard/Tailscale
tunnel instead of exposing the ports directly).

## Prusa Connect uploads

MakerEye can periodically capture a JPEG snapshot from its own go2rtc
pipeline (`GET /api/frame.jpeg`) and upload it to Prusa Connect's webcam
ingestion endpoint, so your printer's Prusa Connect dashboard shows a
live-ish camera feed without a separate uploader process.

### Getting a token and fingerprint

MakerEye doesn't perform camera registration/pairing itself. In Prusa
Connect's web UI: **Cameras -> Add camera -> "Other camera"**. That
issues a **token**, paste it into `prusa_connect.token`. For
**fingerprint**, pick any stable, unique string at least 16 characters
(e.g. `makereye-<device.name>-01`) and put the same value in
`prusa_connect.fingerprint` — it's not a secret, just an identifier;
Prusa Connect treats a fingerprint change as a different camera.

### Configuration

```yaml
prusa_connect:
  enabled: true
  token: "<token from Prusa Connect>"
  fingerprint: "makereye-mk4-01"
  interval_seconds: 10
```

Then `sudo systemctl restart makereye` (or `makereye prusa restart` if
the daemon is already running with `enabled: true`). `makereye status`
shows upload counts/failures:

```
prusa_connect: phase=running uploads=42 failures=0
```

Upload failures (bad token, network blip, Prusa Connect unreachable) are
logged and retried on the next interval, they never affect camera
streaming, this is an advisory feature independent of it.

### Token storage

Like `go2rtc.auth.password`, `prusa_connect.token` is stored **as
plaintext** in `config.yaml`, not hashed: Prusa Connect's API takes it as
a literal bearer credential on every upload, so MakerEye must hold the
real value to use it. `config.yaml` is `0640 makereye:makereye`; treat it
like any other credential file.

## Hardware validation

**This has not been run against real hardware by the session that wrote
this version of MakerEye.** Before trusting an install, walk through this
checklist on an actual Raspberry Pi Zero 2 W + Camera Module 3 running
Raspberry Pi OS Lite 64-bit:

1. **Camera detection**: `rpicam-hello --list-cameras` shows the Camera
   Module 3.
2. **Direct rpicam test**: `rpicam-vid -t 5000 -o test.h264` produces a
   valid, non-empty H.264 file.
3. **MakerEye service startup**: after `scripts/install.sh`, `systemctl
   status makereye` shows `active (running)` and `journalctl -u makereye
   -n 50` shows go2rtc starting without repeated restart-loop warnings.
4. **RTSP playback**: `ffplay rtsp://<pi-host>:8554/camera` (or VLC "Open
   Network Stream") shows live video (requires an SSH tunnel or a
   LAN-bound config per "Network exposure" above).
5. **WebRTC access**: open `http://<pi-host>:1984/` in a browser (via
   tunnel or LAN config) and confirm the stream loads over WebRTC.
6. **MJPEG access**: `curl -o test.mjpeg http://<pi-host>:1984/api/stream.mjpeg?src=camera`
   and confirm the file contains valid JPEG frame boundaries (or open the
   URL directly in a browser).
7. **Snapshot retrieval**: `curl -o snap.jpg http://<pi-host>:1984/api/frame.jpeg?src=camera`
   produces a valid JPEG.
8. **Stream start/stop/restart**: `makereye stream stop`, confirm RTSP
   playback stops; `makereye stream start`, confirm it resumes;
   `makereye stream restart`, confirm a brief interruption then resume.
9. **Reboot persistence**: `sudo reboot`, confirm `makereye.service` is
   `active (running)` after boot with no manual intervention
   (`systemctl is-enabled makereye` should say `enabled`).
10. **Failure and restart behavior**: temporarily rename
    `/usr/local/bin/go2rtc` (or corrupt the generated
    `/var/lib/makereye/go2rtc.yaml`), restart the service, confirm
    MakerEye logs a clear error rather than crash-looping the whole
    service; restore the binary/config and confirm `makereye stream
    restart` recovers it.

None of these claims should be repeated as "tested" until an operator
(or a future session with real hardware access) has actually run them —
see `docs/NEXT_SESSION.md`.

## Optional accessory: Wyze Spotlight Kit

`scripts/spotlight_ctl.sh` drives a Wyze Cam v3 Spotlight Kit's LEDs
(0-255 brightness) directly from the Pi over USB OTG, unrelated to the
Wyze camera it's normally sold with, MakerEye's Pi Zero 2 W just talks to
the spotlight's own USB-serial cable:

```sh
./scripts/spotlight_ctl.sh 200          # 0 (off) - 255 (max)
DEVICE=/dev/ttyUSB1 ./scripts/spotlight_ctl.sh 0
```

This is a standalone experiment, not wired into `config.yaml` or the
daemon. See `ROADMAP.md`'s Milestone 4 (Lighting) for the planned
integration: a native lighting subsystem with CLI control and a
dimmable Home Assistant light entity over MQTT.

## License

GPLv3, see `LICENSE`. Third-party dependency licenses are noted in
`docs/DEVELOPMENT.md`.

## More documentation

- `DESIGN.md`, architecture, process ownership, and the reasoning behind
  it.
- `ROADMAP.md`, milestone status, including what's deliberately not
  built yet.
- `docs/DEVELOPMENT.md`, local dev workflow, build/test/cross-compile,
  hardware validation, and known limitations of developing without a Pi.
- `docs/NEXT_SESSION.md`, handoff notes for continuing this project.
- `CHANGELOG.md`, notable changes by version.
