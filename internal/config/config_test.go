package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("default config should be valid: %v", err)
	}
}

func TestValidateCatchesBadValues(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"empty device name", func(c *Config) { c.Device.Name = "" }, "device.name"},
		{"zero width", func(c *Config) { c.Camera.Width = 0 }, "camera.width"},
		{"negative height", func(c *Config) { c.Camera.Height = -1 }, "camera.height"},
		{"framerate too high", func(c *Config) { c.Camera.Framerate = 500 }, "camera.framerate"},
		{"bad rotation", func(c *Config) { c.Camera.Rotation = 90 }, "camera.rotation"},
		{"bad autofocus", func(c *Config) { c.Camera.Autofocus = "sonar" }, "camera.autofocus"},
		{"empty stream name", func(c *Config) { c.Stream.Name = "" }, "stream.name"},
		{"stream name with slash", func(c *Config) { c.Stream.Name = "a/b" }, "stream.name"},
		{"empty go2rtc binary", func(c *Config) { c.Go2rtc.BinaryPath = "" }, "go2rtc.binary_path"},
		{"auth username without password", func(c *Config) { c.Go2rtc.Auth.Username = "admin" }, "go2rtc.auth"},
		{"auth password without username", func(c *Config) { c.Go2rtc.Auth.Password = "hunter2" }, "go2rtc.auth"},
		{"prusa enabled without token", func(c *Config) {
			c.PrusaConnect.Enabled = true
			c.PrusaConnect.Fingerprint = "at-least-16-characters"
		}, "prusa_connect.token"},
		{"prusa enabled with short fingerprint", func(c *Config) {
			c.PrusaConnect.Enabled = true
			c.PrusaConnect.Token = "tok"
			c.PrusaConnect.Fingerprint = "short"
		}, "prusa_connect.fingerprint"},
		{"prusa enabled with zero interval", func(c *Config) {
			c.PrusaConnect.Enabled = true
			c.PrusaConnect.Token = "tok"
			c.PrusaConnect.Fingerprint = "at-least-16-characters"
			c.PrusaConnect.IntervalSeconds = 0
		}, "prusa_connect.interval_seconds"},
		{"mqtt enabled without broker", func(c *Config) { c.MQTT.Enabled = true }, "mqtt.broker_url"},
		{"mqtt password without username", func(c *Config) {
			c.MQTT.Enabled = true
			c.MQTT.BrokerURL = "tcp://host:1883"
			c.MQTT.Password = "hunter2"
		}, "mqtt.password"},
		{"mqtt topic prefix with wildcard", func(c *Config) {
			c.MQTT.Enabled = true
			c.MQTT.BrokerURL = "tcp://host:1883"
			c.MQTT.TopicPrefix = "makereye/#"
		}, "mqtt.topic_prefix"},
		{"lighting enabled without lights", func(c *Config) { c.Lighting.Enabled = true }, "lighting.lights"},
		{"light with empty name", func(c *Config) {
			c.Lighting.Enabled = true
			c.Lighting.Lights = []LightConfig{{Name: "", Type: LightTypeWyzeSpotlight}}
		}, "lighting.lights[0].name"},
		{"light with bad type", func(c *Config) {
			c.Lighting.Enabled = true
			c.Lighting.Lights = []LightConfig{{Name: "spot", Type: "lava_lamp"}}
		}, "lighting.lights[0].type"},
		{"duplicate light names", func(c *Config) {
			c.Lighting.Enabled = true
			c.Lighting.Lights = []LightConfig{
				{Name: "spot", Type: LightTypeWyzeSpotlight},
				{Name: "spot", Type: LightTypeWyzeSpotlight},
			}
		}, "duplicated"},
		{"light startup brightness out of range", func(c *Config) {
			c.Lighting.Enabled = true
			c.Lighting.Lights = []LightConfig{{Name: "spot", Type: LightTypeWyzeSpotlight, StartupBrightness: 300}}
		}, "startup_brightness"},
		{"timelapse bad interval", func(c *Config) {
			c.Timelapse.Enabled = true
			c.Timelapse.DefaultIntervalSeconds = 0
		}, "timelapse.default_interval_seconds"},
		{"timelapse bad fps", func(c *Config) {
			c.Timelapse.Enabled = true
			c.Timelapse.DefaultPlaybackFPS = 500
		}, "timelapse.default_playback_fps"},
		{"timelapse bad encoder", func(c *Config) {
			c.Timelapse.Enabled = true
			c.Timelapse.Encoder = "divx"
		}, "timelapse.encoder"},
		{"timelapse light without lighting", func(c *Config) {
			c.Timelapse.Enabled = true
			c.Timelapse.Light = "spot"
		}, "lighting is disabled"},
		{"timelapse light unknown name", func(c *Config) {
			c.Lighting.Enabled = true
			c.Lighting.Lights = []LightConfig{{Name: "shelf", Type: LightTypeWyzeSpotlight}}
			c.Timelapse.Enabled = true
			c.Timelapse.Light = "spot"
		}, "does not match any configured"},
		{"bad log level", func(c *Config) { c.System.LogLevel = "loud" }, "system.log_level"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			tc.mutate(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected validation error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

func TestPrusaConnectValidWhenFullyConfigured(t *testing.T) {
	cfg := Default()
	cfg.PrusaConnect.Enabled = true
	cfg.PrusaConnect.Token = "some-token"
	cfg.PrusaConnect.Fingerprint = "at-least-16-characters"
	cfg.PrusaConnect.IntervalSeconds = 10
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected valid config, got error: %v", err)
	}
}

func TestLoadAppliesDefaultsAndOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yamlContent := `
device:
  name: bench-printer
camera:
  width: 1280
  height: 720
  framerate: 15
  bitrate_kbps: 2000
  autofocus: manual
  lens_position: 0.5
stream:
  name: bench
go2rtc:
  binary_path: /usr/local/bin/go2rtc
  config_path: /var/lib/makereye/go2rtc.yaml
  rtsp_listen: 127.0.0.1:8554
  webrtc_listen: 127.0.0.1:1984
  http_listen: 127.0.0.1:1984
system:
  log_level: debug
  state_dir: /var/lib/makereye
  run_dir: /run/makereye
`
	if err := os.WriteFile(path, []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Device.Name != "bench-printer" {
		t.Errorf("device.name = %q, want bench-printer", cfg.Device.Name)
	}
	if cfg.Camera.Width != 1280 || cfg.Camera.Height != 720 {
		t.Errorf("camera dims = %dx%d, want 1280x720", cfg.Camera.Width, cfg.Camera.Height)
	}
	// System log_level was overridden, but nothing in stream touches
	// go2rtc RTSPListen default indirectly; confirm override applied too.
	if cfg.System.LogLevel != "debug" {
		t.Errorf("system.log_level = %q, want debug", cfg.System.LogLevel)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("not_a_real_field: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("expected error for unknown top-level field, got nil")
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load("/nonexistent/path/config.yaml"); err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}
