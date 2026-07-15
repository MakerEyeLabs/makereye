package go2rtc

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/MakerEyeLabs/makereye/internal/config"
)

// Phase describes the current lifecycle phase of the supervised go2rtc
// process.
type Phase string

const (
	// PhaseStopped means no process is running and none is being
	// supervised (either never started, or Stop was called).
	PhaseStopped Phase = "stopped"
	// PhaseRunning means the process is running.
	PhaseRunning Phase = "running"
	// PhaseRestarting means the process exited unexpectedly and the
	// supervisor is waiting out a backoff before relaunching it.
	PhaseRestarting Phase = "restarting"
	// PhaseFailed means the process crashed repeatedly and the
	// supervisor has given up automatic restarts; manual
	// "stream restart" is required.
	PhaseFailed Phase = "failed"
)

// State is a snapshot of the supervisor's current status.
type State struct {
	Phase        Phase
	PID          int
	StartedAt    time.Time
	RestartCount int
	LastError    string
	// LastErrorAt is when LastError was recorded, so consumers can
	// order errors across subsystems.
	LastErrorAt time.Time
}

const (
	// restartResetWindow: a clean run longer than this resets the
	// consecutive-restart counter, so a long-running process that
	// eventually crashes once is treated as a fresh failure, not part of
	// a crash loop.
	restartResetWindow = 2 * time.Minute
	// stopGracePeriod is how long Stop waits for SIGTERM before
	// escalating to SIGKILL.
	stopGracePeriod = 5 * time.Second
)

// maxRestarts is how many consecutive rapid restarts are tolerated before
// the supervisor gives up and enters PhaseFailed. This bounds restart
// behavior instead of retrying forever. It is a var (not const) so tests
// can shrink it to exercise the crash-loop path quickly.
var maxRestarts = 5

// backoffSchedule gives the delay before each successive restart attempt.
// It is a var so tests can shrink it to exercise restart behavior quickly.
var backoffSchedule = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	30 * time.Second,
}

// Supervisor launches and supervises a single go2rtc process as MakerEye's
// camera streaming backend (MakerEye owns the process directly rather than
// delegating to a second systemd unit; see DESIGN.md).
type Supervisor struct {
	cfg    *config.Config
	logger *slog.Logger

	mu    sync.Mutex
	state State
	cmd   *exec.Cmd

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewSupervisor creates a Supervisor for cfg. It does not start any
// process.
func NewSupervisor(cfg *config.Config, logger *slog.Logger) *Supervisor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Supervisor{
		cfg:    cfg,
		logger: logger,
		state:  State{Phase: PhaseStopped},
	}
}

// Start writes the generated go2rtc configuration and launches the go2rtc
// process, supervising it until Stop is called. Start returns once the
// process has been launched; it does not block for the process lifetime.
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.state.Phase == PhaseRunning || s.state.Phase == PhaseRestarting {
		s.mu.Unlock()
		return fmt.Errorf("go2rtc is already %s", s.state.Phase)
	}
	s.mu.Unlock()

	if err := s.writeConfig(); err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel

	s.wg.Add(1)
	go s.superviseLoop(runCtx)

	return nil
}

// Stop terminates the supervised process, if any, and stops all automatic
// restart behavior. It waits for the process to exit cleanly, escalating
// to SIGKILL after a grace period.
func (s *Supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	if s.state.Phase == PhaseStopped {
		s.mu.Unlock()
		return nil
	}
	cancel := s.cancel
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}

	s.mu.Lock()
	s.state = State{Phase: PhaseStopped}
	s.mu.Unlock()
	return nil
}

// Restart stops and then starts the supervised process, resetting the
// restart-backoff counter.
func (s *Supervisor) Restart(ctx context.Context) error {
	if err := s.Stop(ctx); err != nil {
		return err
	}
	return s.Start(ctx)
}

// Status returns a snapshot of the current supervisor state.
func (s *Supervisor) Status() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// HealthCheck queries the go2rtc HTTP API to confirm the process is
// actually accepting connections, not just running as an OS process.
func (s *Supervisor) HealthCheck(ctx context.Context) error {
	if s.Status().Phase != PhaseRunning {
		return fmt.Errorf("go2rtc is not running")
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	url := fmt.Sprintf("http://%s/api", ClientHostPort(s.cfg.Go2rtc.HTTPListen))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("go2rtc API unreachable at %s: %w", s.cfg.Go2rtc.HTTPListen, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("go2rtc API returned status %d", resp.StatusCode)
	}
	return nil
}

func (s *Supervisor) writeConfig() error {
	data, err := Render(s.cfg)
	if err != nil {
		return fmt.Errorf("rendering go2rtc config: %w", err)
	}
	dir := filepath.Dir(s.cfg.Go2rtc.ConfigPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating go2rtc config directory %q: %w", dir, err)
	}
	tmp := s.cfg.Go2rtc.ConfigPath + ".tmp"
	// 0o640, not 0o644: this file can contain the plaintext
	// go2rtc.auth.password from config.yaml (see internal/config and
	// DESIGN.md "Security considerations"), so it must not be
	// world-readable.
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return fmt.Errorf("writing go2rtc config: %w", err)
	}
	if err := os.Rename(tmp, s.cfg.Go2rtc.ConfigPath); err != nil {
		return fmt.Errorf("installing go2rtc config: %w", err)
	}
	return nil
}

// superviseLoop launches go2rtc, waits for it to exit, and restarts it
// with bounded backoff until ctx is cancelled or too many restarts occur
// in a row.
func (s *Supervisor) superviseLoop(ctx context.Context) {
	defer s.wg.Done()

	restarts := 0
	for {
		start := time.Now()
		exitErr := s.runOnce(ctx)

		if ctx.Err() != nil {
			return
		}

		if time.Since(start) >= restartResetWindow {
			restarts = 0
		}

		if exitErr != nil {
			s.setError(exitErr)
		}

		if restarts >= maxRestarts {
			s.mu.Lock()
			s.state.Phase = PhaseFailed
			s.mu.Unlock()
			s.logger.Error("go2rtc crash-looped, giving up automatic restarts",
				"restarts", restarts)
			return
		}

		delay := backoffSchedule[min(restarts, len(backoffSchedule)-1)]
		restarts++

		s.mu.Lock()
		s.state.Phase = PhaseRestarting
		s.state.RestartCount = restarts
		s.mu.Unlock()

		s.logger.Warn("go2rtc exited, restarting", "delay", delay, "attempt", restarts)

		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}
	}
}

// runOnce launches go2rtc and blocks until it exits or ctx is cancelled.
func (s *Supervisor) runOnce(ctx context.Context) error {
	cmd := exec.Command(s.cfg.Go2rtc.BinaryPath, "-config", s.cfg.Go2rtc.ConfigPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting go2rtc: %w", err)
	}

	s.mu.Lock()
	s.cmd = cmd
	s.state.Phase = PhaseRunning
	s.state.PID = cmd.Process.Pid
	s.state.StartedAt = time.Now()
	s.state.LastError = ""
	s.mu.Unlock()

	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()

	select {
	case <-ctx.Done():
		terminate(cmd)
		<-waitErr
		return nil
	case err := <-waitErr:
		return err
	}
}

// terminate sends SIGTERM to the process group, escalating to SIGKILL if
// it does not exit within stopGracePeriod.
func terminate(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pgid := cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)

	done := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(stopGracePeriod):
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
}

func (s *Supervisor) setError(err error) {
	s.mu.Lock()
	s.state.LastError = err.Error()
	s.state.LastErrorAt = time.Now()
	s.mu.Unlock()
}
