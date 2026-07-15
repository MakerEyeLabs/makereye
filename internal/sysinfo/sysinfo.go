// Package sysinfo collects lightweight device telemetry (OS version,
// CPU/memory/disk usage, CPU temperature, uptime) for MQTT/Home
// Assistant diagnostic sensors. Everything reads Linux's /proc and
// /sys interfaces directly; paths and the statfs syscall are injectable
// for tests.
package sysinfo

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// Collector gathers telemetry. CPU usage is measured as the busy share
// of the interval between successive CPUPercent calls (the first call
// reports the average since boot).
type Collector struct {
	ProcStat    string
	ProcMeminfo string
	ProcUptime  string
	ProcMounts  string
	OSRelease   string
	ThermalZone string

	// Statfs reports (usedBytes, availBytes) for a mount point;
	// injectable for tests.
	Statfs func(path string) (used, avail uint64, err error)

	mu        sync.Mutex
	prevBusy  uint64
	prevTotal uint64
}

// New returns a Collector using the standard Linux paths.
func New() *Collector {
	return &Collector{
		ProcStat:    "/proc/stat",
		ProcMeminfo: "/proc/meminfo",
		ProcUptime:  "/proc/uptime",
		ProcMounts:  "/proc/mounts",
		OSRelease:   "/etc/os-release",
		ThermalZone: "/sys/class/thermal/thermal_zone0/temp",
		Statfs: func(path string) (uint64, uint64, error) {
			var st unix.Statfs_t
			if err := unix.Statfs(path, &st); err != nil {
				return 0, 0, err
			}
			bsize := uint64(st.Bsize)
			used := (uint64(st.Blocks) - uint64(st.Bfree)) * bsize
			avail := uint64(st.Bavail) * bsize
			return used, avail, nil
		},
	}
}

// OSVersion returns the OS pretty name plus the kernel release, e.g.
// "Debian GNU/Linux 12 (bookworm), kernel 6.6.51+rpt-rpi-v8".
func (c *Collector) OSVersion() string {
	name := "unknown"
	if data, err := os.ReadFile(c.OSRelease); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
				name = strings.Trim(v, `"`)
				break
			}
		}
	}
	var uts unix.Utsname
	if err := unix.Uname(&uts); err == nil {
		return name + ", kernel " + unix.ByteSliceToString(uts.Release[:])
	}
	return name
}

// CPUPercent returns the CPU busy percentage since the previous call
// (or since boot on the first call).
func (c *Collector) CPUPercent() (float64, error) {
	data, err := os.ReadFile(c.ProcStat)
	if err != nil {
		return 0, err
	}
	line, _, _ := strings.Cut(string(data), "\n")
	fields := strings.Fields(line)
	if len(fields) < 8 || fields[0] != "cpu" {
		return 0, fmt.Errorf("unexpected /proc/stat format")
	}
	var vals [8]uint64
	for i := 0; i < 8; i++ {
		v, err := strconv.ParseUint(fields[i+1], 10, 64)
		if err != nil {
			return 0, err
		}
		vals[i] = v
	}
	// user nice system idle iowait irq softirq steal
	var total uint64
	for _, v := range vals {
		total += v
	}
	idle := vals[3] + vals[4]
	busy := total - idle

	c.mu.Lock()
	dBusy := busy - c.prevBusy
	dTotal := total - c.prevTotal
	c.prevBusy, c.prevTotal = busy, total
	c.mu.Unlock()

	if dTotal == 0 {
		return 0, nil
	}
	return 100 * float64(dBusy) / float64(dTotal), nil
}

// MemoryPercent returns used memory as a percentage (based on
// MemAvailable, matching what `free` considers actually available).
func (c *Collector) MemoryPercent() (float64, error) {
	data, err := os.ReadFile(c.ProcMeminfo)
	if err != nil {
		return 0, err
	}
	var total, avail uint64
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total, _ = strconv.ParseUint(fields[1], 10, 64)
		case "MemAvailable:":
			avail, _ = strconv.ParseUint(fields[1], 10, 64)
		}
	}
	if total == 0 {
		return 0, fmt.Errorf("MemTotal not found")
	}
	return 100 * float64(total-avail) / float64(total), nil
}

// CPUTempC returns the CPU temperature in degrees Celsius.
func (c *Collector) CPUTempC() (float64, error) {
	data, err := os.ReadFile(c.ThermalZone)
	if err != nil {
		return 0, err
	}
	milli, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, err
	}
	return float64(milli) / 1000, nil
}

// MaxDiskUsage returns the highest usage percentage across real
// (device-backed) filesystems and the mount point it belongs to.
func (c *Collector) MaxDiskUsage() (float64, string, error) {
	data, err := os.ReadFile(c.ProcMounts)
	if err != nil {
		return 0, "", err
	}
	seen := map[string]bool{}
	maxPct, maxMount := -1.0, ""
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(fields[0], "/dev/") || seen[fields[0]] {
			continue
		}
		seen[fields[0]] = true
		used, avail, err := c.Statfs(fields[1])
		if err != nil || used+avail == 0 {
			continue
		}
		pct := 100 * float64(used) / float64(used+avail)
		if pct > maxPct {
			maxPct, maxMount = pct, fields[1]
		}
	}
	if maxMount == "" {
		return 0, "", fmt.Errorf("no device-backed filesystems found")
	}
	return maxPct, maxMount, nil
}

// UptimeSeconds returns the system uptime.
func (c *Collector) UptimeSeconds() (int64, error) {
	data, err := os.ReadFile(c.ProcUptime)
	if err != nil {
		return 0, err
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(data)), " ")
	secs, err := strconv.ParseFloat(first, 64)
	if err != nil {
		return 0, err
	}
	return int64(secs), nil
}
