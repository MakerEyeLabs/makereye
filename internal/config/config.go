// Package config defines MakerEye's persistent YAML configuration: its
// schema, defaults, and validation. Loading never mutates or rewrites the
// user's file on disk.
package config

import (
	"fmt"
	"os"
	"path/filepath"
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
	MQTT         MQTTConfig         `yaml:"mqtt"`
	Lighting     LightingConfig     `yaml:"lighting"`
	Timelapse    TimelapseConfig    `yaml:"timelapse"`
	System       SystemConfig       `yaml:"system"`

	// The subsystems below are not implemented yet. Their config
	// sections are accepted and validated only enough to catch obvious
	// mistakes; setting enabled: true has no runtime effect yet.
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

// MQTTConfig controls the optional MQTT + Home Assistant integration.
// Core operation (streaming, Prusa Connect uploads) never requires it.
type MQTTConfig struct {
	Enabled bool `yaml:"enabled"`

	// BrokerURL is the MQTT broker address, e.g.
	// "tcp://homeassistant.local:1883" (or "ssl://host:8883" for TLS).
	BrokerURL string `yaml:"broker_url"`

	// Username/Password authenticate against the broker. Password is
	// stored as configured, not hashed -- the broker needs the literal
	// credential, the same constraint as Go2rtcConfig's AuthConfig.
	// Username may be set alone (some brokers allow passwordless
	// users); Password requires Username.
	Username string `yaml:"username"`
	Password string `yaml:"password"`

	// TopicPrefix is the root of MakerEye's own MQTT topics
	// (availability, state, commands). Topics look like
	// "<topic_prefix>/<device.name>/...". Default "makereye".
	TopicPrefix string `yaml:"topic_prefix"`

	// DiscoveryPrefix is Home Assistant's MQTT discovery prefix.
	// Default "homeassistant" (HA's own default); only change it if
	// your HA install changed it too.
	DiscoveryPrefix string `yaml:"discovery_prefix"`
}

// LightTypeWyzeSpotlight drives a Wyze Cam v3 Spotlight Kit over its
// USB serial interface (see scripts/spotlight_ctl.sh for the protocol).
const LightTypeWyzeSpotlight = "wyze_spotlight"

// LightingConfig controls the optional lighting subsystem: named
// lights MakerEye can switch and dim.
type LightingConfig struct {
	Enabled bool          `yaml:"enabled"`
	Lights  []LightConfig `yaml:"lights"`
}

// LightConfig describes one light. Type selects the backend; the
// remaining fields are interpreted per type.
type LightConfig struct {
	// Name identifies the light in the CLI (`makereye light <name> ...`)
	// and in MQTT topics/Home Assistant entity ids.
	Name string `yaml:"name"`

	// Type is the backend type. Supported: "wyze_spotlight".
	Type string `yaml:"type"`

	// Device is the backend's device path. For wyze_spotlight this is
	// the USB serial device; empty means /dev/ttyUSB0.
	Device string `yaml:"device"`

	// StartupBrightness (0-255) is applied when the daemon starts, so
	// the light is in a known state (the Wyze spotlight is write-only,
	// its actual state can't be read back). 0 = off.
	StartupBrightness int `yaml:"startup_brightness"`
}

// Timelapse encoder types.
const (
	EncoderLibx264 = "libx264"
	EncoderV4L2M2M = "h264_v4l2m2m"
)

// TimelapseConfig controls the optional timelapse subsystem. Enabling
// it makes `makereye timelapse ...` and the HA timelapse entities
// available; it does not start a job.
type TimelapseConfig struct {
	Enabled bool `yaml:"enabled"`

	// OutputDir holds per-job directories (frames, manifest, rendered
	// output). Empty means <system.state_dir>/timelapses. Point it at a
	// mounted NAS share to avoid SD-card wear entirely.
	OutputDir string `yaml:"output_dir"`

	// DefaultIntervalSeconds is the capture interval used when a job
	// doesn't specify one.
	DefaultIntervalSeconds int `yaml:"default_interval_seconds"`

	// DefaultPlaybackFPS is the rendered video's frame rate used when a
	// job doesn't specify one.
	DefaultPlaybackFPS int `yaml:"default_playback_fps"`

	// AutoRender renders automatically when a job is stopped. Manual
	// rendering (`makereye timelapse render <job-id>`) always works.
	AutoRender bool `yaml:"auto_render"`

	// RetainFrames keeps source frames after a successful, validated
	// render. Frames are never deleted on failure regardless.
	RetainFrames bool `yaml:"retain_frames"`

	// ResumeInterrupted continues capturing jobs that were active when
	// the daemon stopped (restarts, updates, power loss), instead of
	// only marking them interrupted.
	ResumeInterrupted bool `yaml:"resume_interrupted"`

	// MinimumFreeSpaceMB stops capture (and refuses renders) when the
	// output filesystem's available space falls below this.
	MinimumFreeSpaceMB int `yaml:"minimum_free_space_mb"`

	// SnapshotTimeoutSeconds bounds each frame capture.
	SnapshotTimeoutSeconds int `yaml:"snapshot_timeout_seconds"`

	// Encoder is the ffmpeg video encoder: "libx264" (default, safe) or
	// "h264_v4l2m2m" (Pi hardware encoder; may contend with the live
	// stream's encoding, validate before relying on it).
	Encoder string `yaml:"encoder"`

	// RenderTimeoutMinutes is the watchdog for a single ffmpeg render.
	RenderTimeoutMinutes int `yaml:"render_timeout_minutes"`

	// Light optionally names a configured light (see lighting.lights)
	// to hold at LightBrightness while capturing; its previous level is
	// restored when capture stops. Empty disables the hold.
	Light string `yaml:"light"`

	// LightBrightness (0-255) is the level held during capture when
	// Light is set.
	LightBrightness int `yaml:"light_brightness"`
}

// TimelapseOutputDir resolves the effective timelapse output directory.
func (c *Config) TimelapseOutputDir() string {
	if c.Timelapse.OutputDir != "" {
		return c.Timelapse.OutputDir
	}
	return filepath.Join(c.System.StateDir, "timelapses")
}

// PrusaLinkConfig is a placeholder for Milestone 6. No runtime effect.
type PrusaLinkConfig struct {
	Enabled bool `yaml:"enabled"`
}

// MotionConfig is a placeholder for Milestone 8. No runtime effect.
type MotionConfig struct {
	Enabled bool `yaml:"enabled"`
}

// AIConfig is a placeholder for Milestone 9. No runtime effect.
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
		MQTT: MQTTConfig{
			Enabled:         false,
			TopicPrefix:     "makereye",
			DiscoveryPrefix: "homeassistant",
		},
		Lighting: LightingConfig{
			Enabled: false,
		},
		Timelapse: TimelapseConfig{
			Enabled:                false,
			DefaultIntervalSeconds: 30,
			DefaultPlaybackFPS:     30,
			AutoRender:             true,
			RetainFrames:           true,
			ResumeInterrupted:      true,
			MinimumFreeSpaceMB:     1024,
			SnapshotTimeoutSeconds: 10,
			Encoder:                EncoderLibx264,
			RenderTimeoutMinutes:   60,
			LightBrightness:        255,
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

	if c.MQTT.Enabled {
		check(strings.TrimSpace(c.MQTT.BrokerURL) == "", "mqtt.broker_url must not be empty when enabled")
		check(c.MQTT.Username == "" && c.MQTT.Password != "",
			"mqtt.password requires mqtt.username to be set")
		check(strings.TrimSpace(c.MQTT.TopicPrefix) == "", "mqtt.topic_prefix must not be empty when enabled")
		check(strings.ContainsAny(c.MQTT.TopicPrefix, " #+"), "mqtt.topic_prefix must not contain spaces or MQTT wildcards, got %q", c.MQTT.TopicPrefix)
		check(strings.TrimSpace(c.MQTT.DiscoveryPrefix) == "", "mqtt.discovery_prefix must not be empty when enabled")
	}

	if c.Lighting.Enabled {
		check(len(c.Lighting.Lights) == 0, "lighting.lights must not be empty when lighting is enabled")
		seen := map[string]bool{}
		for i, l := range c.Lighting.Lights {
			check(strings.TrimSpace(l.Name) == "", "lighting.lights[%d].name must not be empty", i)
			check(strings.ContainsAny(l.Name, " /\\?#+"),
				"lighting.lights[%d].name must not contain whitespace, URL-reserved, or MQTT wildcard characters, got %q", i, l.Name)
			check(seen[l.Name], "lighting.lights[%d].name %q is duplicated", i, l.Name)
			seen[l.Name] = true
			switch l.Type {
			case LightTypeWyzeSpotlight:
			default:
				errs = append(errs, fmt.Sprintf(
					"lighting.lights[%d].type must be %q, got %q", i, LightTypeWyzeSpotlight, l.Type))
			}
			check(l.StartupBrightness < 0 || l.StartupBrightness > 255,
				"lighting.lights[%d].startup_brightness must be 0-255, got %d", i, l.StartupBrightness)
		}
	}

	if c.Timelapse.Enabled {
		check(c.Timelapse.DefaultIntervalSeconds < 1 || c.Timelapse.DefaultIntervalSeconds > 3600,
			"timelapse.default_interval_seconds must be 1-3600, got %d", c.Timelapse.DefaultIntervalSeconds)
		check(c.Timelapse.DefaultPlaybackFPS < 1 || c.Timelapse.DefaultPlaybackFPS > 120,
			"timelapse.default_playback_fps must be 1-120, got %d", c.Timelapse.DefaultPlaybackFPS)
		check(c.Timelapse.MinimumFreeSpaceMB < 0,
			"timelapse.minimum_free_space_mb must not be negative, got %d", c.Timelapse.MinimumFreeSpaceMB)
		check(c.Timelapse.SnapshotTimeoutSeconds < 1 || c.Timelapse.SnapshotTimeoutSeconds > 60,
			"timelapse.snapshot_timeout_seconds must be 1-60, got %d", c.Timelapse.SnapshotTimeoutSeconds)
		check(c.Timelapse.RenderTimeoutMinutes < 1 || c.Timelapse.RenderTimeoutMinutes > 720,
			"timelapse.render_timeout_minutes must be 1-720, got %d", c.Timelapse.RenderTimeoutMinutes)
		switch c.Timelapse.Encoder {
		case EncoderLibx264, EncoderV4L2M2M:
		default:
			errs = append(errs, fmt.Sprintf(
				"timelapse.encoder must be %q or %q, got %q", EncoderLibx264, EncoderV4L2M2M, c.Timelapse.Encoder))
		}
		check(c.Timelapse.LightBrightness < 0 || c.Timelapse.LightBrightness > 255,
			"timelapse.light_brightness must be 0-255, got %d", c.Timelapse.LightBrightness)
		if c.Timelapse.Light != "" {
			check(!c.Lighting.Enabled, "timelapse.light is set but lighting is disabled")
			found := false
			for _, l := range c.Lighting.Lights {
				if l.Name == c.Timelapse.Light {
					found = true
					break
				}
			}
			check(c.Lighting.Enabled && !found,
				"timelapse.light %q does not match any configured lighting.lights name", c.Timelapse.Light)
		}
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
