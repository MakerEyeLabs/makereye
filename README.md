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

**Current status: Milestone 0 (repository foundation) and Milestone 1
(camera streaming) are implemented. Later milestones (Prusa Connect
uploads, MQTT, timelapses, motion detection, AI monitoring, web UI) are
documented in `ROADMAP.md` but not yet implemented.** See
`docs/NEXT_SESSION.md` for exactly what has and hasn't been validated.

## What Milestone 1 gives you

- A `makereye` daemon that manages a Raspberry Pi Camera Module 3
  pipeline via `rpicam-vid` and exposes it through
  [go2rtc](https://github.com/AlexxIT/go2rtc): RTSP, WebRTC, MJPEG, and
  JPEG snapshots.
- A small CLI (`run`, `version`, `validate-config`, `status`, `stream
  start/stop/restart`).
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
commented example covering `device`, `camera`, `stream`, `go2rtc`, and
`system`. Sections for future milestones (`prusa_connect`, `mqtt`,
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
```

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

**The RTSP/WebRTC/MJPEG/snapshot endpoints have no authentication in this
milestone.** The example config binds all of them to `127.0.0.1` only —
they are not reachable from your LAN by default. If you want to view the
stream from another device, either:

- SSH-tunnel to the Pi (`ssh -L 8554:localhost:8554 -L 1984:localhost:1984 pi@<host>`), or
- deliberately change `go2rtc.rtsp_listen` / `webrtc_listen` /
  `http_listen` in your config to a LAN-reachable address, understanding
  that anyone on that network segment can then view (and, for WebRTC/API,
  potentially reconfigure) the stream. Treat this the same as any other
  unauthenticated camera on your network, keep it on a trusted LAN/VLAN.

Adding authentication to these endpoints is out of scope for Milestone 1;
if you need it now, put a reverse proxy with auth in front, or rely on
network-level restrictions (firewall rules, VLAN isolation).

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
