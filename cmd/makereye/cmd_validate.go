package main

import (
	"flag"
	"fmt"
	"strings"
)

func cmdValidateConfig(args []string) int {
	fs := flag.NewFlagSet("validate-config", flag.ContinueOnError)
	path := configFlag(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, ok := loadConfig(*path)
	if !ok {
		return 1
	}
	fmt.Printf("config OK: %s\n", *path)
	fmt.Printf("  device: %s\n", cfg.Device.Name)
	fmt.Printf("  camera: %dx%d @%dfps, %dkbps, autofocus=%s\n",
		cfg.Camera.Width, cfg.Camera.Height, cfg.Camera.Framerate, cfg.Camera.BitrateKbps, cfg.Camera.Autofocus)
	fmt.Printf("  stream: %s\n", cfg.Stream.Name)
	authState := "disabled"
	if cfg.Go2rtc.Auth.Username != "" {
		authState = fmt.Sprintf("enabled (user=%s)", cfg.Go2rtc.Auth.Username)
	}
	fmt.Printf("  go2rtc: rtsp=%s webrtc=%s http=%s auth=%s\n",
		cfg.Go2rtc.RTSPListen, cfg.Go2rtc.WebRTCListen, cfg.Go2rtc.HTTPListen, authState)
	prusaState := "disabled"
	if cfg.PrusaConnect.Enabled {
		prusaState = fmt.Sprintf("enabled (fingerprint=%s, every %ds)",
			cfg.PrusaConnect.Fingerprint, cfg.PrusaConnect.IntervalSeconds)
	}
	fmt.Printf("  prusa_connect: %s\n", prusaState)
	mqttState := "disabled"
	if cfg.MQTT.Enabled {
		mqttState = fmt.Sprintf("enabled (broker=%s, topic_prefix=%s)",
			cfg.MQTT.BrokerURL, cfg.MQTT.TopicPrefix)
	}
	fmt.Printf("  mqtt: %s\n", mqttState)
	lightingState := "disabled"
	if cfg.Lighting.Enabled {
		names := make([]string, 0, len(cfg.Lighting.Lights))
		for _, l := range cfg.Lighting.Lights {
			names = append(names, fmt.Sprintf("%s(%s)", l.Name, l.Type))
		}
		lightingState = "enabled: " + strings.Join(names, ", ")
	}
	fmt.Printf("  lighting: %s\n", lightingState)
	tlState := "disabled"
	if cfg.Timelapse.Enabled {
		tlState = fmt.Sprintf("enabled (dir=%s, every %ds, %dfps, auto_render=%v)",
			cfg.TimelapseOutputDir(), cfg.Timelapse.DefaultIntervalSeconds,
			cfg.Timelapse.DefaultPlaybackFPS, cfg.Timelapse.AutoRender)
	}
	fmt.Printf("  timelapse: %s\n", tlState)
	return 0
}
