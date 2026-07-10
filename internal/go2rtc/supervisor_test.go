package go2rtc

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MakerEyeLabs/makereye/internal/config"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeBinary writes a small shell script to dir and returns its path.
func fakeBinary(t *testing.T, dir, script string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-go2rtc.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func testConfig(t *testing.T, binaryScript string) *config.Config {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Go2rtc.BinaryPath = fakeBinary(t, dir, binaryScript)
	cfg.Go2rtc.ConfigPath = filepath.Join(dir, "go2rtc.yaml")
	return cfg
}

func TestSupervisorStartStop(t *testing.T) {
	cfg := testConfig(t, "sleep 30")
	s := NewSupervisor(cfg, testLogger())

	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitForPhase(t, s, PhaseRunning, 2*time.Second)

	if _, err := os.Stat(cfg.Go2rtc.ConfigPath); err != nil {
		t.Errorf("expected go2rtc config to be written: %v", err)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if got := s.Status().Phase; got != PhaseStopped {
		t.Errorf("phase after Stop = %s, want %s", got, PhaseStopped)
	}
}

func TestSupervisorDoubleStartFails(t *testing.T) {
	cfg := testConfig(t, "sleep 30")
	s := NewSupervisor(cfg, testLogger())

	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop(context.Background())

	waitForPhase(t, s, PhaseRunning, 2*time.Second)

	if err := s.Start(ctx); err == nil {
		t.Error("expected error starting an already-running supervisor")
	}
}

func TestSupervisorCrashLoopEntersFailed(t *testing.T) {
	orig := backoffSchedule
	origMax := maxRestarts
	backoffSchedule = []time.Duration{10 * time.Millisecond}
	maxRestarts = 2
	t.Cleanup(func() {
		backoffSchedule = orig
		maxRestarts = origMax
	})

	cfg := testConfig(t, "exit 1")
	s := NewSupervisor(cfg, testLogger())

	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitForPhase(t, s, PhaseFailed, 5*time.Second)

	st := s.Status()
	if st.RestartCount < maxRestarts {
		t.Errorf("restart count = %d, want at least %d", st.RestartCount, maxRestarts)
	}
}

func TestSupervisorRestart(t *testing.T) {
	cfg := testConfig(t, "sleep 30")
	s := NewSupervisor(cfg, testLogger())

	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, s, PhaseRunning, 2*time.Second)
	firstPID := s.Status().PID

	restartCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Restart(restartCtx); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	waitForPhase(t, s, PhaseRunning, 2*time.Second)

	if s.Status().PID == firstPID {
		t.Error("expected a new PID after restart")
	}

	s.Stop(context.Background())
}

func waitForPhase(t *testing.T, s *Supervisor, want Phase, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.Status().Phase == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("phase did not reach %s within %s, got %s", want, timeout, s.Status().Phase)
}
