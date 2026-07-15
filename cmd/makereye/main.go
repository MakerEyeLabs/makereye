// Command makereye is the MakerEye CLI and daemon entry point.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/MakerEyeLabs/makereye/internal/config"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		printUsage()
		return 2
	}

	cmd := args[0]
	rest := args[1:]

	switch cmd {
	case "run":
		return cmdRun(rest)
	case "version":
		return cmdVersion(rest)
	case "validate-config":
		return cmdValidateConfig(rest)
	case "status":
		return cmdStatus(rest)
	case "stream":
		return cmdStream(rest)
	case "prusa":
		return cmdPrusa(rest)
	case "light":
		return cmdLight(rest)
	case "timelapse":
		return cmdTimelapse(rest)
	case "help", "-h", "--help":
		printUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "makereye: unknown command %q\n\n", cmd)
		printUsage()
		return 2
	}
}

func printUsage() {
	fmt.Fprint(os.Stderr, `makereye: MakerEye camera appliance daemon and CLI

Usage:
  makereye run [-config path]              Run the MakerEye daemon in the foreground
  makereye version                         Print version information
  makereye validate-config [-config path]  Load and validate a config file
  makereye status [-config path]           Show daemon and stream status
  makereye stream start [-config path]     Start the camera stream
  makereye stream stop [-config path]      Stop the camera stream
  makereye stream restart [-config path]   Restart the camera stream
  makereye prusa start [-config path]      Start Prusa Connect snapshot uploads
  makereye prusa stop [-config path]       Stop Prusa Connect snapshot uploads
  makereye prusa restart [-config path]    Restart Prusa Connect snapshot uploads
  makereye light <name> <0-255|on|off>     Set a configured light's brightness
  makereye timelapse start [-name N] [-interval SEC] [-fps N]
                                           Start a timelapse capture job
  makereye timelapse stop                  Stop the active capture job
  makereye timelapse status                Show active/most recent job
  makereye timelapse list                  List all timelapse jobs
  makereye timelapse render <job-id>       Render (or re-render) a job

The default config path is `+config.DefaultConfigPath+`.
`)
}

// configFlag adds the shared -config flag to fs and returns the pointer to
// its value.
func configFlag(fs *flag.FlagSet) *string {
	return fs.String("config", config.DefaultConfigPath, "path to MakerEye config file")
}

// loadConfig loads and validates the config at path, printing an
// actionable error to stderr on failure.
func loadConfig(path string) (*config.Config, bool) {
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "makereye: %v\n", err)
		return nil, false
	}
	return cfg, true
}

// signalContext returns a context cancelled on SIGTERM or SIGINT.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
}

func newLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
}
