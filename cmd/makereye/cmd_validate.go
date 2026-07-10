package main

import (
	"flag"
	"fmt"
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
	fmt.Printf("  go2rtc: rtsp=%s webrtc=%s http=%s\n",
		cfg.Go2rtc.RTSPListen, cfg.Go2rtc.WebRTCListen, cfg.Go2rtc.HTTPListen)
	return 0
}
