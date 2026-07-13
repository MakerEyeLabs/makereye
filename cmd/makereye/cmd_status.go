package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/MakerEyeLabs/makereye/internal/daemon"
	"github.com/MakerEyeLabs/makereye/internal/go2rtc"
	"github.com/MakerEyeLabs/makereye/internal/ipc"
)

func cmdStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	path := configFlag(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, ok := loadConfig(*path)
	if !ok {
		return 1
	}

	ctx, cancel := signalContext()
	defer cancel()

	resp, err := ipc.Call(ctx, daemon.SocketPath(cfg), ipc.CmdStatus)
	if err != nil {
		fmt.Fprintf(os.Stderr, "makereye: %v\n", err)
		return 1
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "makereye: %s\n", resp.Error)
		return 1
	}

	fmt.Println(resp.Message)
	urls := go2rtc.StreamURLs(cfg)
	fmt.Printf("  rtsp:     %s\n", urls.RTSP)
	fmt.Printf("  webrtc:   %s\n", urls.WebRTC)
	fmt.Printf("  mjpeg:    %s\n", urls.MJPEG)
	fmt.Printf("  snapshot: %s\n", urls.Snapshot)
	if cfg.Go2rtc.Auth.Username != "" {
		fmt.Println("  (these URLs include your go2rtc.auth credentials -- treat this output as sensitive)")
	}
	return 0
}
