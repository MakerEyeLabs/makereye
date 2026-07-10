package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/MakerEyeLabs/makereye/internal/daemon"
)

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	path := configFlag(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, ok := loadConfig(*path)
	if !ok {
		return 1
	}

	logger := newLogger()
	d := daemon.New(cfg, logger)

	ctx, cancel := signalContext()
	defer cancel()

	if err := d.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "makereye: %v\n", err)
		return 1
	}
	return 0
}
