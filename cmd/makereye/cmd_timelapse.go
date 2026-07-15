package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/MakerEyeLabs/makereye/internal/daemon"
	"github.com/MakerEyeLabs/makereye/internal/ipc"
)

func cmdTimelapse(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "makereye: timelapse requires a subcommand: start, stop, status, list, render")
		return 2
	}

	sub := args[0]
	rest := args[1:]
	req := ipc.Request{}

	fs := flag.NewFlagSet("timelapse "+sub, flag.ContinueOnError)
	path := configFlag(fs)

	switch sub {
	case "start":
		req.Command = ipc.CmdTimelapseStart
		fs.StringVar(&req.TimelapseName, "name", "", "job name (default: timestamp)")
		fs.IntVar(&req.TimelapseInterval, "interval", 0, "capture interval in seconds (default: config)")
		fs.IntVar(&req.TimelapseFPS, "fps", 0, "playback frame rate (default: config)")
		fs.StringVar(&req.TimelapseLight, "light", "", "light to hold during capture (default: config)")
		fs.IntVar(&req.TimelapseLightVal, "light-brightness", 0, "brightness for the held light (default: config)")
	case "stop":
		req.Command = ipc.CmdTimelapseStop
	case "status":
		req.Command = ipc.CmdTimelapseStatus
	case "list":
		req.Command = ipc.CmdTimelapseList
	case "render":
		req.Command = ipc.CmdTimelapseRender
		if len(rest) < 1 || len(rest[0]) == 0 || rest[0][0] == '-' {
			fmt.Fprintln(os.Stderr, "makereye: usage: timelapse render <job-id> [-config path]")
			return 2
		}
		req.TimelapseJobID = rest[0]
		rest = rest[1:]
	default:
		fmt.Fprintf(os.Stderr, "makereye: unknown timelapse subcommand %q\n", sub)
		return 2
	}

	if err := fs.Parse(rest); err != nil {
		return 2
	}

	cfg, ok := loadConfig(*path)
	if !ok {
		return 1
	}

	ctx, cancel := signalContext()
	defer cancel()

	resp, err := ipc.CallRequest(ctx, daemon.SocketPath(cfg), req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "makereye: %v\n", err)
		return 1
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "makereye: %s\n", resp.Error)
		return 1
	}

	fmt.Println(resp.Message)
	return 0
}
