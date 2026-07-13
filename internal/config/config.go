// Package config defines MakerEye's persistent YAML configuration: its
// schema, defaults, and validation. Loading never mutates or rewrites the
// user's file on disk.
package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultConfigPath is the default location MakerEye reads its
// configuration from when no override is given.
const DefaultConfigPath = "/etc/makereye/config.yaml"

// Config is the root MakerEye configuration document.
type Config struct {
	Device       DeviceConfig       `yaml:"device"`
	Camera       CameraConfig       `yaml:"camera"`
	Stream       StreamConfig       `yaml:"stream"`
	Go2rtc       Go2rtcConfig       `yaml:"go2rtc"`
	PrusaConnect PrusaConnectConfig `yaml:"prusa_connect"`
	System       SystemConfig       `yaml:"system"`

	// The subsystems below are not implemented yet. Their config
	// sections are accepted and validated only enough to catch obvious
	// mistakes; setting enabled: true has no runtime effect yet.
	MQTT      MQTTConfig      `yaml:"mqtt"`
	Timelapse TimelapseConfig `yaml:"timelapse"`
	PrusaLink PrusaLinkConfig `yaml:"prusalink"`
	Motion    MotionConfig    `yaml:"motion"`
	AI        AIConfig        `yaml:"ai"`
}

// DeviceConfig identifies this MakerEye instance.
type DeviceConfig struct {
	Name string `yaml:"name"`
}

// AutofocusMode is a Camera Module 3 autofocus mode.
type AutofocusMode string

const (
	AutofocusAuto       AutofocusMode = "auto"
	AutofocusContinuous AutofocusMode = "continuous"
	AutofocusManual     AutofocusMode = "manual"
)

// CameraConfig controls how rpicam-vid captures from the camera.
type CameraConfig struct {
	// Camera is the libcamera camera index or identifier (rpicam-vid
	// --camera). Leave at 0 for a single-camera Pi.
	Camera int `yaml:"camera"`

	Width     int `yaml:"width"`
	Height    int `yaml:"height"`
	Framerate int `yaml:"framerate"`
	// BitrateKbps is the H.264 target bitrate in kilobits per second.
	BitrateKbps int `yaml:"bitrate_kbps"`

	// Rotation is a rotation in degrees: 0 or 180.
	Rotation int  `yaml:"rotation"`
	HFlip    bool `yaml:"hflip"`
	VFlip    bool `yaml:"vflip"`

	// Autofocus is one of "auto", "continuous", or "manual".
	Autofocus AutofocusMode `yaml:"autofocus"`
	// LensPosition is used only when autofocus is "manual". It is the
	// rpicam-vid --lens-position dioptre value (0 = infinity).
	LensPosition float64 `yaml:"lens_position"`
}

// StreamConfig controls the go2rtc stream that exposes the camera.
type StreamConfig struct {
	// Name is the go2rtc stream name, used in RTSP/WebRTC/MJPEG/snapshot
	// URLs, e.g. rtsp://<host>:8554/<name>.
	Name string `yaml:"name"`
}

// Go2rtcConfig controls the go2rtc binary and the network addresses it
// listens on.
type Go2rtcConfig struct {
	// BinaryPath is the path to the go2rtc executable.
	BinaryPath string `yaml:"binary_path"`
	// ConfigPath is where MakerEye writes the generated go2rtc config.
	ConfigPath string `yaml:"config_path"`

	// RTSPListen is the RTSP listen address, e.g. "127.0.0.1:8554".
	RTSPListen string `yaml:"rtsp_listen"`
	// WebRTCListen is the WebRTC/API listen address, e.g. "127.0.0.1:1984".
	WebRTCListen string `yaml:"webrtc_listen"`
	// HTTPListen serves MJPEG and snapshots, e.g. "127.0.0.1:1984" (go2rtc
	// serves HTTP, WebRTC signalling, MJPEG, and snapshots on the same
	// port by default).
	HTTPListen string `yaml:"http_listen"`

	// Auth optionally protects go2rtc's HTTP API (WebRTC signalling,
	// MJPEG, snapshot) and RTSP endpoints with a username/password. Empty
	// Username disables auth, matching go2rtc's own default. See
	// README.md "Network exposure and security".
	Auth AuthConfig `yaml:"auth"`
}

// AuthConfig is a username/password pair passed straight through to
// go2rtc's own HTTP Basic Auth (API) and RTSP auth.
type AuthConfig struct {
	Username string `yaml:"username"`

	// Password is stored as configured, not hashed. go2rtc compares it
	// directly against the plaintext credential clients submit and has
	// no support for verifying against a password hash, so MakerEye
	// cannot pre-hash it here without silently breaking authentication
	// for every client. This file (and the go2rtc config MakerEye
	// generates from it) should be handled like any other credential
	// file; the installer already restricts both to 0640 makereye:makereye.
	Password string `yaml:"password"`
}

// SystemConfig controls general daemon behavior.
type SystemConfig struct {
	// LogLevel is one of "debug", "info", "warn", "error".
	LogLevel string `yaml:"log_level"`
	// StateDir holds MakerEye's persistent runtime state.
	StateDir string `yaml:"state_dir"`
	// RunDir holds transient runtime files (sockets, pid-adjacent data).
	RunDir string `yaml:"run_dir"`
}

// PrusaConnectConfig controls periodic snapshot uploads to Prusa Connect's
// webcam ingestion endpoint.
type PrusaConnectConfig struct {
	Enabled bool `yaml:"enabled"`

	// Token is this camera's Prusa Connect upload token. Obtained from
	// Prusa Connect's web UI (Cameras -> Add camera -> "Other camera"),
	// which issues a token for a manually-configured camera rather than
	// MakerEye performing any registration/pairing flow itself.
	//
	// Stored as plaintext, not hashed: Prusa Connect's API takes this as
	// a literal bearer credential in the "token" header on every upload,
	// so MakerEye must hold the real value to use it, the same
	// constraint as go2rtc.auth.password (see Go2rtcConfig.Password).
	Token string `yaml:"token"`

	// Fingerprint is a stable identifier for this camera that Prusa
	// Connect uses to recognize it across uploads. Any unique string at
	// least 16 characters works (e.g. "makereye-<device-name>"); changing
	// it later is treated by Prusa Connect as a different camera.
	Fingerprint string `yaml:"fingerprint"`

	// IntervalSeconds is how often a snapshot is captured and uploaded.
	IntervalSeconds int `yaml:"interval_seconds"`
}

// MQTTConfig is a placeholder for Milestone 3. No runtime effect.
type MQTTConfig struct {
	Enabled bool `yaml:"enabled"`
}

// TimelapseConfig is a placeholder for Milestone 4. No runtime effect.
type TimelapseConfig struct {
	Enabled bool `yaml:"enabled"`
}

// PrusaLinkConfig is a placeholder for Milestone 5. No runtime effect.
type PrusaLinkConfig struct {
	Enabled bool `yaml:"enabled"`
}

// MotionConfig is a placeholder for Milestone 6. No runtime effect.
type MotionConfig struct {
	Enabled bool `yaml:"enabled"`
}

// AIConfig is a placeholder for Milestone 7. No runtime effect.
type AIConfig struct {
	Enabled bool `yaml:"enabled"`
}

// Default returns a Config populated with MakerEye's built-in defaults.
func Default() *Config {
	return &Config{
		Device: DeviceConfig{
			Name: "makereye",
		},
		Camera: CameraConfig{
			Camera:      0,
			Width:       1920,
			Height:      1080,
			Framerate:   30,
			BitrateKbps: 4000,
			Rotation:    0,
			HFlip:       false,
			VFlip:       false,
			Autofocus:   AutofocusContinuous,
		},
		Stream: StreamConfig{
			Name: "camera",
		},
		Go2rtc: Go2rtcConfig{
			BinaryPath:   "/usr/local/bin/go2rtc",
			ConfigPath:   "/var/lib/makereye/go2rtc.yaml",
			RTSPListen:   "127.0.0.1:8554",
			WebRTCListen: "127.0.0.1:1984",
			HTTPListen:   "127.0.0.1:1984",
		},
		PrusaConnect: PrusaConnectConfig{
			Enabled:         false,
			IntervalSeconds: 10,
		},
		System: SystemConfig{
			LogLevel: "info",
			StateDir: "/var/lib/makereye",
			RunDir:   "/run/makereye",
		},
	}
}

// Load reads and parses the YAML configuration file at path, applies
// defaults for unset fields, and validates the result. It never modifies
// the file on disk.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %q: %w", path, err)
	}

	cfg := Default()
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parsing config file %q: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks the configuration for actionable errors. It reports all
// problems it finds rather than stopping at the first one.
func (c *Config) Validate() error {
	var errs []string

	check := func(cond bool, format string, args ...any) {
		if cond {
			errs = append(errs, fmt.Sprintf(format, args...))
		}
	}

	check(strings.TrimSpace(c.Device.Name) == "", "device.name must not be empty")

	check(c.Camera.Width <= 0, "camera.width must be positive, got %d", c.Camera.Width)
	check(c.Camera.Height <= 0, "camera.height must be positive, got %d", c.Camera.Height)
	check(c.Camera.Framerate <= 0 || c.Camera.Framerate > 120,
		"camera.framerate must be between 1 and 120, got %d", c.Camera.Framerate)
	check(c.Camera.BitrateKbps <= 0, "camera.bitrate_kbps must be positive, got %d", c.Camera.BitrateKbps)
	check(c.Camera.Rotation != 0 && c.Camera.Rotation != 180,
		"camera.rotation must be 0 or 180, got %d", c.Camera.Rotation)
	check(c.Camera.Camera < 0, "camera.camera must not be negative, got %d", c.Camera.Camera)

	switch c.Camera.Autofocus {
	case AutofocusAuto, AutofocusContinuous, AutofocusManual:
	default:
		errs = append(errs, fmt.Sprintf(
			"camera.autofocus must be one of %q, %q, %q, got %q",
			AutofocusAuto, AutofocusContinuous, AutofocusManual, c.Camera.Autofocus))
	}
	if c.Camera.Autofocus == AutofocusManual {
		check(c.Camera.LensPosition < 0, "camera.lens_position must not be negative when autofocus is manual, got %v", c.Camera.LensPosition)
	}

	check(strings.TrimSpace(c.Stream.Name) == "", "stream.name must not be empty")
	check(strings.ContainsAny(c.Stream.Name, " /\\?#"), "stream.name must not contain whitespace or URL-reserved characters, got %q", c.Stream.Name)

	check(strings.TrimSpace(c.Go2rtc.BinaryPath) == "", "go2rtc.binary_path must not be empty")
	check(strings.TrimSpace(c.Go2rtc.ConfigPath) == "", "go2rtc.config_path must not be empty")
	check(strings.TrimSpace(c.Go2rtc.RTSPListen) == "", "go2rtc.rtsp_listen must not be empty")
	check(strings.TrimSpace(c.Go2rtc.WebRTCListen) == "", "go2rtc.webrtc_listen must not be empty")
	check(strings.TrimSpace(c.Go2rtc.HTTPListen) == "", "go2rtc.http_listen must not be empty")
	check((c.Go2rtc.Auth.Username == "") != (c.Go2rtc.Auth.Password == ""),
		"go2rtc.auth.username and go2rtc.auth.password must both be set or both left empty")

	if c.PrusaConnect.Enabled {
		check(strings.TrimSpace(c.PrusaConnect.Token) == "", "prusa_connect.token must not be empty when enabled")
		check(len(c.PrusaConnect.Fingerprint) < 16,
			"prusa_connect.fingerprint must be at least 16 characters when enabled, got %d", len(c.PrusaConnect.Fingerprint))
		check(c.PrusaConnect.IntervalSeconds <= 0,
			"prusa_connect.interval_seconds must be positive when enabled, got %d", c.PrusaConnect.IntervalSeconds)
	}

	switch c.System.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Sprintf(
			"system.log_level must be one of \"debug\", \"info\", \"warn\", \"error\", got %q", c.System.LogLevel))
	}
	check(strings.TrimSpace(c.System.StateDir) == "", "system.state_dir must not be empty")
	check(strings.TrimSpace(c.System.RunDir) == "", "system.run_dir must not be empty")

	if len(errs) > 0 {
		return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}
