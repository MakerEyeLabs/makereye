# Development

## Local development workflow

MakerEye is a standard Go module; most work (config, CLI wiring, go2rtc
config generation, process-supervisor logic) can be developed and tested
on any machine with Go installed, a Raspberry Pi is only required to
exercise the actual camera and go2rtc streaming pipeline end to end.

```sh
git clone https://github.com/MakerEyeLabs/makereye.git
cd makereye
go build ./...
go test ./...
```

## Build commands

```sh
make build          # ./bin/makereye for the host OS/arch
make build-arm64     # ./bin/makereye-linux-arm64, cross-compiled for
                      # Raspberry Pi OS Lite 64-bit
make check           # gofmt -l check + go vet + go test
make clean
```

Version info (`makereye version`) is set via `-ldflags` in the Makefile
from `git describe`/`git rev-parse`; a plain `go build` without the
Makefile produces a binary that reports `dev`.

## Test commands

```sh
go test ./...              # all packages
go test ./... -run TestX   # a single test
gofmt -l .                 # list files needing formatting (should be empty)
go vet ./...
```

Tests are hardware-independent: `internal/camera` tests check the
argument list built for `rpicam-vid` as plain string assertions,
`internal/go2rtc` supervisor tests replace the go2rtc binary with a
throwaway shell script (`sleep 30`, `exit 1`, etc.) to exercise
start/stop/restart/crash-loop behavior without needing the real go2rtc
binary or a camera.

## Cross-compilation / Pi build strategy

`make build-arm64` cross-compiles from any Go toolchain
(`GOOS=linux GOARCH=arm64`), this is the expected way to produce the
binary you `scp` to a Pi Zero 2 W, since compiling on-device is slow.
Building directly on the Pi with `make build` also works if preferred.

There is no CGO dependency in MakerEye itself, so cross-compilation needs
no special toolchain setup beyond a standard Go install.

## How to validate on actual Raspberry Pi hardware

See the "Hardware validation" checklist in `README.md`, it covers camera
detection, direct `rpicam-vid` testing, service startup, RTSP/WebRTC/
MJPEG/snapshot access, stream start/stop/restart, reboot persistence, and
failure/restart behavior. That checklist has **not** been executed by the
Claude Code session that built this version of MakerEye; treat all
camera/streaming claims as implemented-but-unverified until it has been.

## Known limitations of development without camera hardware

- `internal/camera.BuildArgs` is tested as a pure function (config in,
  argument list out) but the actual `rpicam-vid` invocation, its exit
  behavior, and the resulting H.264 stream have not been exercised.
- `internal/go2rtc.Supervisor` tests use a fake binary standing in for
  go2rtc, so process lifecycle (start/stop/restart/crash-loop/backoff) is
  covered, but real go2rtc behavior (actually demuxing rpicam-vid's H.264
  output, serving RTSP/WebRTC/MJPEG/snapshot) is not.
- `go2rtc.Render`'s output is validated as well-formed YAML with the
  expected structure, but has not been fed to a real go2rtc process.
- There is no CI runner with camera hardware; hardware validation is
  necessarily manual (see above) until/unless a self-hosted runner with a
  Pi + camera is set up, which is out of scope for this milestone.

## Third-party dependencies and licenses

| Dependency | License | Role |
|---|---|---|
| [gopkg.in/yaml.v3](https://github.com/go-yaml/yaml) | MIT (with some Apache-2.0-licensed files) | YAML config parsing/generation |
| [go2rtc](https://github.com/AlexxIT/go2rtc) | MIT | External binary, not vendored, streaming backend MakerEye launches and supervises |
| Raspberry Pi `rpicam-apps` / `libcamera` | BSD-2-Clause (rpicam-apps) / mixed BSD/LGPL (libcamera) | External tools, not vendored, camera capture, installed via apt |

MakerEye's own code is GPLv3 (see `LICENSE`). No code has been copied
from another project into this repository; go2rtc and rpicam-apps/
libcamera are used as external processes (subprocess/binary
dependencies), not linked or vendored, so their licenses do not impose
requirements on MakerEye's own source beyond normal attribution, which is
recorded here.
