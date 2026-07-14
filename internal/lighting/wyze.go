package lighting

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// defaultWyzeDevice is where the Spotlight Kit's USB serial interface
// normally appears on a Pi with no other USB serial devices attached.
const defaultWyzeDevice = "/dev/ttyUSB0"

// wyzeSpotlight drives a Wyze Cam v3 Spotlight Kit over its USB serial
// interface. Protocol (reverse-engineered against real hardware, see
// scripts/spotlight_ctl.sh and DESIGN.md):
//
//	aa 55 43 05 16 <brightness> 07 <checksum_hi> <checksum_lo>
//
// where checksum is the 16-bit big-endian sum of all preceding bytes.
// The device validates the checksum: frames with a wrong one are
// silently ignored. The device is write-only; state cannot be read
// back.
type wyzeSpotlight struct {
	device string
}

func newWyzeSpotlight(device string) *wyzeSpotlight {
	if device == "" {
		device = defaultWyzeDevice
	}
	return &wyzeSpotlight{device: device}
}

// wyzeFrame builds the 9-byte command frame for a brightness level.
func wyzeFrame(level int) []byte {
	body := []byte{0xAA, 0x55, 0x43, 0x05, 0x16, byte(level), 0x07}
	sum := 0
	for _, b := range body {
		sum += int(b)
	}
	return append(body, byte(sum>>8), byte(sum&0xFF))
}

// SetBrightness writes the command frame for level to the serial
// device. The device is opened per call rather than held open, so an
// unplugged/replugged spotlight recovers on the next command without
// any reconnect logic.
func (w *wyzeSpotlight) SetBrightness(level int) error {
	if level < 0 || level > 255 {
		return fmt.Errorf("brightness must be 0-255, got %d", level)
	}

	// O_NOCTTY: never adopt the port as controlling terminal.
	// O_NONBLOCK: don't hang on carrier-detect for ports that model it.
	f, err := os.OpenFile(w.device, os.O_WRONLY|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("opening %s: %w", w.device, err)
	}
	defer f.Close()

	if err := setRawOutput(f); err != nil {
		return fmt.Errorf("configuring %s: %w", w.device, err)
	}

	if _, err := f.Write(wyzeFrame(level)); err != nil {
		return fmt.Errorf("writing to %s: %w", w.device, err)
	}
	return nil
}

// setRawOutput disables tty output post-processing on f. Without this,
// OPOST/ONLCR rewrites any 0x0A byte in the frame to 0x0D 0x0A, which
// corrupts the frame for brightness levels whose level or checksum
// byte happens to be 0x0A -- the same pitfall hit (and fixed the same
// way) while reverse-engineering the protocol. ENOTTY is ignored so
// tests can point the backend at a regular file. The baud rate is
// deliberately left at the port's default, matching the known-working
// shell script, which never set one.
func setRawOutput(f *os.File) error {
	t, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	if err != nil {
		if errors.Is(err, unix.ENOTTY) {
			return nil
		}
		return err
	}
	t.Oflag &^= unix.OPOST
	t.Iflag &^= unix.IXON | unix.IXOFF
	t.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG
	return unix.IoctlSetTermios(int(f.Fd()), unix.TCSETS, t)
}
