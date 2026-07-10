# Changelog

All notable changes to MakerEye are recorded here. Format loosely follows
[Keep a Changelog](https://keepachangelog.com/); versioning will follow
SemVer once tagged releases begin.

## [Unreleased]

### Added

- Milestone 0: repository foundation, Go module
  (`github.com/MakerEyeLabs/makereye`), GPLv3 license, `.gitignore`,
  README/DESIGN/ROADMAP docs, example config, systemd unit, install/
  uninstall scripts, Makefile, build-time version support.
- Milestone 1: camera streaming, `makereye` daemon manages a Raspberry
  Pi Camera Module 3 pipeline (`rpicam-vid`) exposed via go2rtc (RTSP,
  WebRTC, MJPEG, snapshot). CLI: `run`, `version`, `validate-config`,
  `status`, `stream start/stop/restart`. Config validation with
  actionable errors. Bounded restart behavior for the go2rtc supervisor.

### Known limitations

- Not yet validated against real Raspberry Pi hardware, see
  `docs/NEXT_SESSION.md` and the "Hardware validation" section of
  `README.md`.
- go2rtc endpoints (RTSP/WebRTC/MJPEG/snapshot) have no authentication;
  default config binds them to loopback only.
- Milestones 2-8 (Prusa Connect uploads, MQTT, timelapses, PrusaLink,
  motion detection, AI monitoring, web UI) are not implemented; see
  `ROADMAP.md`.
