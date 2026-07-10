package main

import (
	"flag"
	"fmt"

	"github.com/MakerEyeLabs/makereye/internal/version"
)

func cmdVersion(args []string) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fmt.Println("makereye " + version.String())
	return 0
}
