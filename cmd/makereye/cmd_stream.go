package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/MakerEyeLabs/makereye/internal/daemon"
	"github.com/MakerEyeLabs/makereye/internal/ipc"
)

func cmdStream(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "makereye: stream requires a subcommand: start, stop, restart")
		return 2
	}

	sub := args[0]
	rest := args[1:]

	var command string
	switch sub {
	case "start":
		command = ipc.CmdStreamStart
	case "stop":
		command = ipc.CmdStreamStop
	case "restart":
		command = ipc.CmdStreamRestart
	default:
		fmt.Fprintf(os.Stderr, "makereye: unknown stream subcommand %q\n", sub)
		return 2
	}

	fs := flag.NewFlagSet("stream "+sub, flag.ContinueOnError)
	path := configFlag(fs)
	if err := fs.Parse(rest); err != nil {
		return 2
	}

	cfg, ok := loadConfig(*path)
	if !ok {
		return 1
	}

	ctx, cancel := signalContext()
	defer cancel()

	resp, err := ipc.Call(ctx, daemon.SocketPath(cfg), command)
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
