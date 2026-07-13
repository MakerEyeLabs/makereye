// Package go2rtc generates go2rtc configuration from MakerEye config and
// supervises the go2rtc process as MakerEye's camera streaming backend.
package go2rtc

import (
	"fmt"
	"net/url"

	"gopkg.in/yaml.v3"

	"github.com/MakerEyeLabs/makereye/internal/camera"
	"github.com/MakerEyeLabs/makereye/internal/config"
)

// go2rtcYAML is the shape of the go2rtc configuration file MakerEye
// generates. It is intentionally minimal: only the fields MakerEye
// controls are set, everything else uses go2rtc's own defaults.
type go2rtcYAML struct {
	Streams map[string]string `yaml:"streams"`
	RTSP    rtspSection       `yaml:"rtsp"`
	WebRTC  webrtcSection     `yaml:"webrtc"`
	API     apiSection        `yaml:"api"`
	Log     logSection        `yaml:"log"`
}

type rtspSection struct {
	Listen   string `yaml:"listen"`
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`
}

type webrtcSection struct {
	Listen string `yaml:"listen"`
}

type apiSection struct {
	Listen   string `yaml:"listen"`
	Username string `yaml:"username,omitempty"`
	Password string `yaml:"password,omitempty"`
}

type logSection struct {
	Format string `yaml:"format"`
}

// Render builds the go2rtc YAML configuration document for cfg. The single
// stream source runs rpicam-vid directly via go2rtc's "exec:" source type,
// so go2rtc is the only process that reads camera frames from MakerEye's
// managed pipeline.
func Render(cfg *config.Config) ([]byte, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}

	source := "exec:" + camera.CommandLine(cfg.Camera)

	doc := go2rtcYAML{
		Streams: map[string]string{
			cfg.Stream.Name: source,
		},
		RTSP: rtspSection{
			Listen:   cfg.Go2rtc.RTSPListen,
			Username: cfg.Go2rtc.Auth.Username,
			Password: cfg.Go2rtc.Auth.Password,
		},
		WebRTC: webrtcSection{Listen: cfg.Go2rtc.WebRTCListen},
		API: apiSection{
			Listen:   cfg.Go2rtc.HTTPListen,
			Username: cfg.Go2rtc.Auth.Username,
			Password: cfg.Go2rtc.Auth.Password,
		},
		Log: logSection{Format: "text"},
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("marshaling go2rtc config: %w", err)
	}
	return out, nil
}

// URLs describes the client-facing endpoints exposed by a rendered go2rtc
// configuration for a given stream name.
type URLs struct {
	RTSP     string
	WebRTC   string
	MJPEG    string
	Snapshot string
}

// StreamURLs returns the URLs clients use to reach the stream named by
// cfg.Stream.Name, based on the configured listen addresses. When
// cfg.Go2rtc.Auth is set, the credentials are embedded (percent-encoded)
// in the returned URLs, since that's what RTSP/HTTP clients expect --
// callers that print these (e.g. `makereye status`) should treat the
// output as sensitive.
func StreamURLs(cfg *config.Config) URLs {
	userinfo := ""
	if cfg.Go2rtc.Auth.Username != "" {
		userinfo = url.UserPassword(cfg.Go2rtc.Auth.Username, cfg.Go2rtc.Auth.Password).String() + "@"
	}

	return URLs{
		RTSP:     fmt.Sprintf("rtsp://%s%s/%s", userinfo, cfg.Go2rtc.RTSPListen, cfg.Stream.Name),
		WebRTC:   fmt.Sprintf("http://%s%s/api/webrtc?src=%s", userinfo, cfg.Go2rtc.WebRTCListen, cfg.Stream.Name),
		MJPEG:    fmt.Sprintf("http://%s%s/api/stream.mjpeg?src=%s", userinfo, cfg.Go2rtc.HTTPListen, cfg.Stream.Name),
		Snapshot: fmt.Sprintf("http://%s%s/api/frame.jpeg?src=%s", userinfo, cfg.Go2rtc.HTTPListen, cfg.Stream.Name),
	}
}
