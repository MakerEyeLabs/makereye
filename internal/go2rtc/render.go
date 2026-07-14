// Package go2rtc generates go2rtc configuration from MakerEye config and
// supervises the go2rtc process as MakerEye's camera streaming backend.
package go2rtc

import (
	"fmt"
	"net"
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

// ClientHostPort maps a configured listen address to an address a
// client on the same host can actually dial. Wildcard hosts ("",
// "0.0.0.0", "::") mean "bind every interface" and are not meaningful
// dial targets, so they become loopback; anything else passes through
// unchanged. Use this whenever MakerEye talks to its own go2rtc (Prusa
// snapshot fetches, health checks) so a LAN-exposed listen address
// doesn't break self-connections.
func ClientHostPort(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// AdvertiseHostPort maps a configured listen address to the address
// clients elsewhere on the network should be told to use: wildcard
// hosts become this device's primary LAN IP (falling back to loopback
// when none can be determined), specific hosts pass through unchanged.
// This is for display (e.g. `makereye status`), not for MakerEye's own
// self-connections -- those use ClientHostPort.
func AdvertiseHostPort(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = primaryIP()
	}
	return net.JoinHostPort(host, port)
}

// primaryIP returns the IP of the interface holding the default route,
// found by "connecting" a UDP socket to a routable address -- no packet
// is actually sent, connect on UDP only resolves the local endpoint.
// 192.0.2.1 is TEST-NET-1, guaranteed non-local.
func primaryIP() string {
	conn, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return addr.IP.String()
	}
	return "127.0.0.1"
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
// cfg.Stream.Name, based on the configured listen addresses; wildcard
// listen addresses are shown as this device's LAN IP (see
// AdvertiseHostPort), since "0.0.0.0" is not something another machine
// can dial. When cfg.Go2rtc.Auth is set, the credentials are embedded
// (percent-encoded) in the returned URLs, since that's what RTSP/HTTP
// clients expect -- callers that print these (e.g. `makereye status`)
// should treat the output as sensitive.
func StreamURLs(cfg *config.Config) URLs {
	userinfo := ""
	if cfg.Go2rtc.Auth.Username != "" {
		userinfo = url.UserPassword(cfg.Go2rtc.Auth.Username, cfg.Go2rtc.Auth.Password).String() + "@"
	}

	rtsp := AdvertiseHostPort(cfg.Go2rtc.RTSPListen)
	webrtc := AdvertiseHostPort(cfg.Go2rtc.WebRTCListen)
	http := AdvertiseHostPort(cfg.Go2rtc.HTTPListen)

	return URLs{
		RTSP:     fmt.Sprintf("rtsp://%s%s/%s", userinfo, rtsp, cfg.Stream.Name),
		WebRTC:   fmt.Sprintf("http://%s%s/api/webrtc?src=%s", userinfo, webrtc, cfg.Stream.Name),
		MJPEG:    fmt.Sprintf("http://%s%s/api/stream.mjpeg?src=%s", userinfo, http, cfg.Stream.Name),
		Snapshot: fmt.Sprintf("http://%s%s/api/frame.jpeg?src=%s", userinfo, http, cfg.Stream.Name),
	}
}
