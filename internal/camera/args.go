// Package camera builds rpicam-vid command lines from MakerEye camera
// configuration. It does not touch hardware itself; go2rtc launches the
// resulting command as its capture source.
package camera

import (
	"fmt"
	"strconv"

	"github.com/MakerEyeLabs/makereye/internal/config"
)

// Binary is the current Raspberry Pi camera capture tool. MakerEye
// intentionally does not support the legacy "raspivid" tool.
const Binary = "rpicam-vid"

// BuildArgs returns the rpicam-vid arguments for the given camera
// configuration. It streams raw H.264 Annex-B to stdout indefinitely,
// which go2rtc consumes directly as an "exec:" source.
func BuildArgs(c config.CameraConfig) []string {
	args := []string{
		"--camera", strconv.Itoa(c.Camera),
		"--nopreview",
		"--timeout", "0",
		"--codec", "h264",
		"--inline",
		"--width", strconv.Itoa(c.Width),
		"--height", strconv.Itoa(c.Height),
		"--framerate", strconv.Itoa(c.Framerate),
		"--bitrate", strconv.Itoa(c.BitrateKbps * 1000),
		"--output", "-",
	}

	if c.Rotation != 0 {
		args = append(args, "--rotation", strconv.Itoa(c.Rotation))
	}
	if c.HFlip {
		args = append(args, "--hflip")
	}
	if c.VFlip {
		args = append(args, "--vflip")
	}

	switch c.Autofocus {
	case config.AutofocusAuto:
		args = append(args, "--autofocus-mode", "auto")
	case config.AutofocusContinuous:
		args = append(args, "--autofocus-mode", "continuous")
	case config.AutofocusManual:
		args = append(args, "--autofocus-mode", "manual", "--lens-position", formatFloat(c.LensPosition))
	}

	return args
}

// CommandLine returns the full shell-quoted command line, for logging and
// for embedding in the generated go2rtc "exec:" source string.
func CommandLine(c config.CameraConfig) string {
	line := Binary
	for _, a := range BuildArgs(c) {
		line += " " + shellQuote(a)
	}
	return line
}

func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// shellQuote quotes a single argument for safe inclusion in the go2rtc
// exec source string, which go2rtc itself splits with shell-like parsing.
func shellQuote(s string) string {
	safe := true
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == '_') {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return fmt.Sprintf("%q", s)
}
