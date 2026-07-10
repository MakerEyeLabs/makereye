package camera

import (
	"strings"
	"testing"

	"github.com/MakerEyeLabs/makereye/internal/config"
)

func TestBuildArgsBasics(t *testing.T) {
	c := config.Default().Camera
	args := BuildArgs(c)
	line := strings.Join(args, " ")

	for _, want := range []string{
		"--width 1920", "--height 1080", "--framerate 30",
		"--bitrate 4000000", "--codec h264", "--inline", "--timeout 0",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("args %q missing %q", line, want)
		}
	}
	if strings.Contains(line, "--rotation") {
		t.Errorf("args %q should omit --rotation when rotation is 0", line)
	}
}

func TestBuildArgsRotationAndFlips(t *testing.T) {
	c := config.Default().Camera
	c.Rotation = 180
	c.HFlip = true
	c.VFlip = true
	line := strings.Join(BuildArgs(c), " ")

	for _, want := range []string{"--rotation 180", "--hflip", "--vflip"} {
		if !strings.Contains(line, want) {
			t.Errorf("args %q missing %q", line, want)
		}
	}
}

func TestBuildArgsAutofocusModes(t *testing.T) {
	cases := []struct {
		mode config.AutofocusMode
		want string
	}{
		{config.AutofocusAuto, "--autofocus-mode auto"},
		{config.AutofocusContinuous, "--autofocus-mode continuous"},
		{config.AutofocusManual, "--autofocus-mode manual"},
	}
	for _, tc := range cases {
		c := config.Default().Camera
		c.Autofocus = tc.mode
		c.LensPosition = 1.5
		line := strings.Join(BuildArgs(c), " ")
		if !strings.Contains(line, tc.want) {
			t.Errorf("mode %s: args %q missing %q", tc.mode, line, tc.want)
		}
		if tc.mode == config.AutofocusManual && !strings.Contains(line, "--lens-position 1.5") {
			t.Errorf("manual mode should include lens-position, got %q", line)
		}
	}
}

func TestCommandLineIsSingleLine(t *testing.T) {
	line := CommandLine(config.Default().Camera)
	if !strings.HasPrefix(line, Binary+" ") {
		t.Errorf("command line %q should start with binary name", line)
	}
	if strings.Contains(line, "\n") {
		t.Errorf("command line must not contain newlines: %q", line)
	}
}
