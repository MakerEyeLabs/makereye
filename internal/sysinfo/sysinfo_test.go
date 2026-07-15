package sysinfo

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCPUPercentDelta(t *testing.T) {
	dir := t.TempDir()
	c := New()
	// t0: 1000 busy (500u+100n+400s), 1000 idle -> total 2000
	c.ProcStat = write(t, dir, "stat",
		"cpu  500 100 400 900 100 0 0 0 0 0\nignored\n")
	if _, err := c.CPUPercent(); err != nil {
		t.Fatalf("first sample: %v", err)
	}
	// t1: +300 busy, +100 idle -> 75% of the 400-tick window
	write(t, dir, "stat",
		"cpu  700 150 450 950 150 0 0 0 0 0\nignored\n")
	pct, err := c.CPUPercent()
	if err != nil {
		t.Fatalf("second sample: %v", err)
	}
	if pct < 74.9 || pct > 75.1 {
		t.Errorf("cpu percent = %v, want 75", pct)
	}
}

func TestMemoryPercent(t *testing.T) {
	dir := t.TempDir()
	c := New()
	c.ProcMeminfo = write(t, dir, "meminfo",
		"MemTotal:        1000000 kB\nMemFree:          100000 kB\nMemAvailable:     250000 kB\n")
	pct, err := c.MemoryPercent()
	if err != nil {
		t.Fatal(err)
	}
	if pct != 75 {
		t.Errorf("memory percent = %v, want 75", pct)
	}
}

func TestCPUTempC(t *testing.T) {
	dir := t.TempDir()
	c := New()
	c.ThermalZone = write(t, dir, "temp", "51234\n")
	temp, err := c.CPUTempC()
	if err != nil {
		t.Fatal(err)
	}
	if temp != 51.234 {
		t.Errorf("temp = %v, want 51.234", temp)
	}
}

func TestMaxDiskUsage(t *testing.T) {
	dir := t.TempDir()
	c := New()
	c.ProcMounts = write(t, dir, "mounts",
		"/dev/mmcblk0p2 / ext4 rw 0 0\n"+
			"/dev/mmcblk0p1 /boot/firmware vfat rw 0 0\n"+
			"tmpfs /tmp tmpfs rw 0 0\n"+ // ignored: not device-backed
			"/dev/mmcblk0p2 /var/bind ext4 rw 0 0\n") // ignored: duplicate device
	c.Statfs = func(path string) (uint64, uint64, error) {
		switch path {
		case "/":
			return 70, 30, nil // 70%
		case "/boot/firmware":
			return 15, 85, nil // 15%
		case "/tmp", "/var/bind":
			return 0, 0, fmt.Errorf("should not be queried: %s", path)
		}
		return 0, 0, fmt.Errorf("unexpected path %s", path)
	}
	pct, mount, err := c.MaxDiskUsage()
	if err != nil {
		t.Fatal(err)
	}
	if mount != "/" || pct != 70 {
		t.Errorf("max disk = %v%% on %q, want 70%% on /", pct, mount)
	}
}

func TestUptimeSeconds(t *testing.T) {
	dir := t.TempDir()
	c := New()
	c.ProcUptime = write(t, dir, "uptime", "12345.67 23456.78\n")
	secs, err := c.UptimeSeconds()
	if err != nil {
		t.Fatal(err)
	}
	if secs != 12345 {
		t.Errorf("uptime = %d, want 12345", secs)
	}
}

func TestOSVersionFallsBackGracefully(t *testing.T) {
	c := New()
	c.OSRelease = "/nonexistent/os-release"
	if got := c.OSVersion(); got == "" {
		t.Error("OSVersion should never be empty")
	}
}

// Sanity check against the real host: everything should return sane
// values on any Linux box.
func TestRealHostSmoke(t *testing.T) {
	c := New()
	if pct, err := c.CPUPercent(); err != nil || pct < 0 || pct > 100 {
		t.Errorf("CPUPercent = %v, %v", pct, err)
	}
	if pct, err := c.MemoryPercent(); err != nil || pct <= 0 || pct >= 100 {
		t.Errorf("MemoryPercent = %v, %v", pct, err)
	}
	if pct, mount, err := c.MaxDiskUsage(); err != nil || pct < 0 || pct > 100 || mount == "" {
		t.Errorf("MaxDiskUsage = %v on %q, %v", pct, mount, err)
	}
	if secs, err := c.UptimeSeconds(); err != nil || secs <= 0 {
		t.Errorf("UptimeSeconds = %v, %v", secs, err)
	}
}
