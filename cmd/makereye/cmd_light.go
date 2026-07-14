package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/MakerEyeLabs/makereye/internal/daemon"
	"github.com/MakerEyeLabs/makereye/internal/ipc"
)

func cmdLight(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "makereye: usage: light <name> <0-255|on|off> [-config path]")
		return 2
	}

	name := args[0]
	value := args[1]
	rest := args[2:]

	fs := flag.NewFlagSet("light", flag.ContinueOnError)
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

	resp, err := ipc.CallRequest(ctx, daemon.SocketPath(cfg), ipc.Request{
		Command:    ipc.CmdLightSet,
		Light:      name,
		Brightness: value,
	})
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
