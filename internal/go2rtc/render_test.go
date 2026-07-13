package go2rtc

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/MakerEyeLabs/makereye/internal/config"
)

func TestRenderProducesValidYAML(t *testing.T) {
	cfg := config.Default()
	out, err := Render(cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("rendered config is not valid YAML: %v", err)
	}

	streams, ok := doc["streams"].(map[string]any)
	if !ok {
		t.Fatalf("streams section missing or wrong type: %v", doc["streams"])
	}
	src, ok := streams[cfg.Stream.Name].(string)
	if !ok || !strings.HasPrefix(src, "exec:rpicam-vid") {
		t.Errorf("stream source = %v, want exec:rpicam-vid prefix", streams[cfg.Stream.Name])
	}
}

func TestRenderRejectsNilConfig(t *testing.T) {
	if _, err := Render(nil); err == nil {
		t.Fatal("expected error for nil config")
	}
}

func TestStreamURLs(t *testing.T) {
	cfg := config.Default()
	cfg.Stream.Name = "printer"
	urls := StreamURLs(cfg)

	if !strings.Contains(urls.RTSP, "printer") || !strings.HasPrefix(urls.RTSP, "rtsp://") {
		t.Errorf("RTSP URL = %q", urls.RTSP)
	}
	if !strings.Contains(urls.Snapshot, "src=printer") {
		t.Errorf("snapshot URL = %q", urls.Snapshot)
	}
	if strings.Contains(urls.RTSP, "@") {
		t.Errorf("RTSP URL should have no embedded credentials when auth is unset: %q", urls.RTSP)
	}
}

func TestStreamURLsEmbedsCredentialsWhenAuthSet(t *testing.T) {
	cfg := config.Default()
	cfg.Go2rtc.Auth.Username = "admin"
	cfg.Go2rtc.Auth.Password = "p@ss/word"
	urls := StreamURLs(cfg)

	if !strings.HasPrefix(urls.RTSP, "rtsp://admin:") || !strings.Contains(urls.RTSP, "@127.0.0.1:8554/") {
		t.Errorf("RTSP URL should embed percent-encoded credentials, got %q", urls.RTSP)
	}
	if !strings.HasPrefix(urls.Snapshot, "http://admin:") {
		t.Errorf("snapshot URL should embed credentials, got %q", urls.Snapshot)
	}
}

func TestRenderIncludesAuthWhenSet(t *testing.T) {
	cfg := config.Default()
	cfg.Go2rtc.Auth.Username = "admin"
	cfg.Go2rtc.Auth.Password = "hunter2"
	out, err := Render(cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("rendered config is not valid YAML: %v", err)
	}
	for _, section := range []string{"rtsp", "api"} {
		s, ok := doc[section].(map[string]any)
		if !ok {
			t.Fatalf("%s section missing or wrong type: %v", section, doc[section])
		}
		if s["username"] != "admin" || s["password"] != "hunter2" {
			t.Errorf("%s section auth = %+v, want username=admin password=hunter2", section, s)
		}
	}
}

func TestRenderOmitsAuthWhenUnset(t *testing.T) {
	out, err := Render(config.Default())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(string(out), "username") || strings.Contains(string(out), "password") {
		t.Errorf("rendered config should omit empty auth fields, got:\n%s", out)
	}
}
