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

**Current status: Milestones 0-5 (repository foundation, camera
streaming, Prusa Connect uploads, MQTT/Home Assistant, lighting,
manual timelapse) are implemented. Later milestones (PrusaLink
auto-timelapse, web UI, motion detection, AI monitoring) are documented
in `ROADMAP.md` but not yet implemented.** See `docs/NEXT_SESSION.md`
for exactly what has and hasn't been validated.

## What's implemented

- A `makereye` daemon that manages a Raspberry Pi Camera Module 3
  pipeline via `rpicam-vid` and exposes it through
  [go2rtc](https://github.com/AlexxIT/go2rtc): RTSP, WebRTC, MJPEG, and
  JPEG snapshots. Optional username/password auth on those endpoints.
- Periodic snapshot uploads to Prusa Connect, pulled from the same
  go2rtc pipeline.
- Optional MQTT + Home Assistant integration: the device and its
  controls appear in HA automatically via MQTT discovery.
- Optional lighting control (first backend: Wyze Cam v3 Spotlight Kit
  over USB serial), from the CLI and as dimmable HA light entities.
- Manual timelapses: one capture job at a time from the shared camera
  pipeline, durable across restarts, rendered to MP4 with ffmpeg,
  controlled from the CLI or Home Assistant.
- A small CLI (`run`, `version`, `validate-config`, `status`, `stream
  start/stop/restart`, `prusa start/stop/restart`, `light`,
  `timelapse`).
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

### Updating

From the checkout on the device:

```sh
./scripts/update.sh
```

pulls the latest commit on the current branch, rebuilds, reinstalls
(picking up any new dependencies via `install.sh`), restarts the
service, and prints `makereye status`. Your `/etc/makereye/config.yaml`
is never touched. A no-SSH update path (an update entity in Home
Assistant with an install button) is sketched in `ROADMAP.md`
"Candidate work".

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
makereye light <name> <0-255|on|off>     Set a configured light's brightness
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

## MQTT and Home Assistant

MakerEye can connect to an MQTT broker and appear in Home Assistant
automatically via MQTT discovery — no HA-side configuration needed. This
is entirely optional: streaming and Prusa Connect uploads never require
MQTT.

You need a broker HA is connected to; the standard setup is HA's
Mosquitto add-on. Then:

```yaml
mqtt:
  enabled: true
  broker_url: tcp://homeassistant.local:1883
  username: makereye
  password: "<broker password>"
```

Credentials are optional in MakerEye — leave both empty to connect
anonymously — but the broker must allow it, and HA's Mosquitto add-on
disables anonymous access by default, so in the standard setup you'll
create a broker user (a dedicated HA user, or a user under the add-on's
`logins:` option) and put its credentials here.

and `sudo systemctl restart makereye`. A "MakerEye" device appears in
HA (Settings → Devices & Services → MQTT) **automatically** — do not
use HA's manual "add MQTT device" dialog, that flow is for devices
that can't announce themselves and would just create a hand-managed
duplicate. The discovered device has:

- a **Stream** switch and a **Restart stream** button,
- a **Prusa Connect uploads** switch and restart button (only when
  `prusa_connect.enabled` is true),
- a dimmable **light entity** per configured light (when
  `lighting.enabled` is true),
- timelapse **start/stop/render buttons**, **interval/fps number
  entities**, and job sensors (when `timelapse.enabled` is true),
- **diagnostic sensors**: OS/kernel version, CPU usage (averaged over
  the publish interval), memory usage and available MB, CPU
  temperature, root and capture-directory disk usage/free space, Wi-Fi
  signal (link quality and interface as attributes), and uptime (with a
  human-readable attribute); the MakerEye version is the device's
  firmware field,
- **diagnostic binary sensors**: capture storage low (the configured
  free-space threshold), undervoltage and CPU throttled (Pi firmware
  flags, with has-occurred attributes), and system clock synchronized,
- sensors: stream phase, Prusa upload/failure counts,
- availability wiring, so everything shows "unavailable" if the daemon
  or the Pi goes down.

Commands go through the same internals as the CLI; state is republished
immediately after each command (acknowledgement) and every 30 seconds.
A broker that is down or unreachable is retried in the background
forever and never affects streaming; `makereye status` shows
`mqtt: connected` / `disconnected (retrying)`.

**Live video in HA** needs no MakerEye code at all: point HA's
[Generic Camera](https://www.home-assistant.io/integrations/generic/)
integration at the go2rtc snapshot and stream URLs from `makereye
status` (requires LAN-reachable listen addresses and `go2rtc.auth`, per
"Network exposure and security" above).

Broker credentials are stored as plaintext in `config.yaml`, same
policy and reasoning as the other credentials (see "Token storage"
above).

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

## Lighting

Good lighting is half of good camera output, so MakerEye can control
named lights natively. Configure them under `lighting:` in
`config.yaml`:

```yaml
lighting:
  enabled: true
  lights:
    - name: spotlight
      type: wyze_spotlight     # first supported backend
      device: /dev/ttyUSB0     # default
      startup_brightness: 0    # applied at daemon start (0 = off)
```

Control from the CLI:

```sh
makereye light spotlight on      # restores last-used brightness
makereye light spotlight 128     # 0-255
makereye light spotlight off
```

With `mqtt.enabled`, every configured light also appears in Home
Assistant as a **dimmable light entity** (brightness slider) on the
MakerEye device — lighting schedules and print-triggered lighting are
then plain HA automations.

The first supported backend is the **Wyze Cam v3 Spotlight Kit**: an
off-the-shelf, cheap USB accessory whose power-passthrough cable also
powers the Pi Zero, driven over its USB serial interface with a
reverse-engineered protocol (full 0-255 brightness, not just the
high/low/off the Wyze firmware exposes). GPIO-driven lights and
addressable LED strips are planned backend types — see `ROADMAP.md`.
Lighting is advisory: an unplugged light is logged and retried on the
next command, never affecting streaming. Note the hardware is
write-only, so MakerEye tracks the last brightness it commanded; the
`startup_brightness` write at daemon start puts the light in a known
state.

`scripts/spotlight_ctl.sh` remains as a standalone tool and as the
protocol documentation:

```sh
./scripts/spotlight_ctl.sh 200          # 0 (off) - 255 (max)
DEVICE=/dev/ttyUSB1 ./scripts/spotlight_ctl.sh 0
```

## Timelapse

One capture job at a time, pulling JPEG frames from the same go2rtc
pipeline as everything else (no second camera claim), stored durably
under per-job directories and rendered to MP4 with ffmpeg. Enable it in
`config.yaml` (`timelapse.enabled: true`; see
`config/config.example.yaml` for every knob), then:

```sh
makereye timelapse start -name "benchy" -interval 15 -fps 30
makereye timelapse status
makereye timelapse stop            # auto-renders by default
makereye timelapse list
makereye timelapse render <job-id> # retry/re-render any job
```

With MQTT enabled the same controls appear in Home Assistant:
start/stop/render buttons, number entities for the capture interval and
playback fps (consumed by the next start), and sensors for phase, job
name, frame count, capture failures, last result, and last output path.

Behavior worth knowing:

- **Restarts don't kill captures.** A job that was capturing when the
  daemon stopped (update, reboot, power loss) resumes automatically
  (`resume_interrupted: true`, the default) or is marked `interrupted`
  and can still be rendered from the frames it got.
- **Frames are never deleted because something failed.** Failed renders
  keep every frame and can be retried; `retain_frames: false` only
  removes frames after a *validated* successful render.
- **Storage guardrails, not retention.** Capture stops cleanly (frames
  preserved, actionable error) if free space drops below
  `minimum_free_space_mb`. MakerEye never deletes old jobs on its own —
  clean up explicitly, or point `output_dir` at bigger storage.
- **`output_dir` cannot live under `/home`.** The service runs with
  systemd's `ProtectHome` hardening and cannot see home directories;
  use the default state dir, or a mount under `/mnt`/`/media`.
- **Failures are visible in HA**, not just journald: the "Last command
  result" sensor shows the outcome of every button/switch command (e.g.
  a start refused for low disk space), and the "Last error" sensor
  shows the newest runtime error from any subsystem (stream, Prusa
  Connect, timelapse), prefixed with where it came from.
- **Storage estimate**: a 1080p JPEG frame from the stream is roughly
  300-600 KB, so a 30-second interval running 24 hours is ~2,880 frames
  ≈ 1-2 GB plus the rendered MP4. For long-running timelapses, prefer
  durable storage: point `output_dir` at a mounted NAS share or USB
  drive to spare the SD card the write wear.
- **Getting videos off the device** is `scp`/network-share territory for
  now; the web UI milestone adds browse/download, and upload automation
  is sketched in `ROADMAP.md` "Candidate work".

### Remote rendering

Rendering on a Pi Zero works but monopolizes it for minutes. The better
setup for regular use: put the whole timelapse tree on a network share
and render elsewhere.

On the camera:

```yaml
timelapse:
  output_dir: /mnt/share/timelapses   # a mounted NAS/SMB/NFS path
  auto_render: false                  # capture only; render remotely
```

On any Debian-based machine that mounts the same share:

```sh
sudo ./scripts/install-renderer.sh /mnt/share/timelapses
```

installs ffmpeg + jq, a `makereye-render` command, and a systemd
service that polls the tree (polling is deliberate: inotify doesn't see
changes made by other hosts on network shares) and renders each job as
its capture finishes — reading the same `job.json` manifests, updating
them the same atomic way, and never deleting frames. Failed renders are
marked `render_failed` and only retried explicitly, so a bad job can't
loop the watcher. One-shot rendering needs no service at all:

```sh
makereye-render /mnt/share/timelapses/<job-id>-<name>
```
- **Lighting during capture**: set `timelapse.light` (and
  `light_brightness`) to pin a configured light while capturing and
  restore it after — or compose the light entities with the timelapse
  buttons in an HA automation, e.g. a script that turns the spotlight
  on, waits a second, and presses "Start timelapse", with the reverse
  on stop.

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
