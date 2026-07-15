package timelapse

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// CommandRunner executes an external command and returns its combined
// output; injectable so render tests never need a real ffmpeg.
type CommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// defaultRunner runs the command via nice -n 19 (CPU) and, when
// available, ionice -c3 (idle I/O class) so rendering has minimal
// scheduling impact on live streaming and frame captures.
func defaultRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	argv := append([]string{"-n", "19"}, name)
	if ionice, err := exec.LookPath("ionice"); err == nil {
		argv = append([]string{"-n", "19", ionice, "-c", "3"}, name)
	}
	cmd := exec.CommandContext(ctx, "nice", append(argv, args...)...)
	return cmd.CombinedOutput()
}

// ffmpegArgs builds the render invocation for a job. Kept as a pure
// function so command construction is unit-testable.
func ffmpegArgs(job *Job, encoder string) []string {
	return []string{
		"-y",
		"-framerate", fmt.Sprintf("%d", job.PlaybackFPS),
		"-i", job.FramesDir() + "/%08d.jpg",
		"-c:v", encoder,
		"-preset", "ultrafast",
		"-pix_fmt", "yuv420p",
		"-movflags", "+faststart",
		job.OutputPath(),
	}
}

// Render starts rendering the given job in the background. It refuses
// jobs that are still capturing, jobs with no frames, and concurrent
// renders (one at a time, device-wide -- a Pi Zero 2 W cannot afford
// two). Works for stopped, interrupted, complete (re-render), and
// previously failed jobs.
func (m *Manager) Render(jobID string) error {
	m.mu.Lock()
	job, ok := m.jobs[jobID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("unknown timelapse job %q", jobID)
	}
	if job.Phase == PhaseCapturing {
		m.mu.Unlock()
		return fmt.Errorf("job %s is still capturing; stop it before rendering", jobID)
	}
	if m.rendering {
		m.mu.Unlock()
		return fmt.Errorf("another render is already in progress (one at a time)")
	}
	if job.FrameCount == 0 {
		m.mu.Unlock()
		return fmt.Errorf("job %s has no captured frames to render", jobID)
	}
	m.rendering = true
	m.mu.Unlock()

	if err := m.checkFreeSpace(); err != nil {
		m.mu.Lock()
		m.rendering = false
		m.mu.Unlock()
		return err
	}

	m.mu.Lock()
	job.OutputFile = sanitizeName(job.Name) + ".mp4"
	job.Phase = PhaseRendering
	job.LastError = ""
	m.mu.Unlock()
	_ = saveManifest(job)
	m.notify()

	go m.runRender(job)
	return nil
}

// runRender executes ffmpeg under the watchdog timeout and finalizes
// the job phase based on validated output.
func (m *Manager) runRender(job *Job) {
	defer func() {
		m.mu.Lock()
		m.rendering = false
		m.mu.Unlock()
		if err := saveManifest(job); err != nil {
			m.logger.Error("persisting job manifest after render", "job", job.ID, "error", err)
		}
		m.notify()
	}()

	timeout := time.Duration(m.cfg.Timelapse.RenderTimeoutMinutes) * time.Minute
	if timeout <= 0 {
		timeout = time.Hour
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	m.logger.Info("rendering timelapse", "job", job.ID, "frames", job.FrameCount,
		"fps", job.PlaybackFPS, "encoder", m.cfg.Timelapse.Encoder)

	out, err := m.renderRunner(ctx, "ffmpeg", ffmpegArgs(job, m.cfg.Timelapse.Encoder)...)
	if err != nil {
		m.setRenderResult(job, PhaseRenderFailed, renderError(err, out))
		return
	}

	// Validate before declaring success: ffmpeg exited zero AND the
	// output exists non-empty.
	info, statErr := os.Stat(job.OutputPath())
	if statErr != nil || info.Size() == 0 {
		m.setRenderResult(job, PhaseRenderFailed, "ffmpeg exited successfully but produced no output file")
		return
	}

	m.setRenderResult(job, PhaseComplete, "")
	m.logger.Info("timelapse render complete", "job", job.ID, "output", job.OutputPath(),
		"bytes", info.Size())

	// Frames may be removed only after a validated successful render,
	// and only when explicitly configured.
	if !m.cfg.Timelapse.RetainFrames {
		if err := os.RemoveAll(job.FramesDir()); err != nil {
			m.logger.Warn("removing source frames after render", "job", job.ID, "error", err)
		} else {
			m.mu.Lock()
			job.FrameCount = 0
			m.mu.Unlock()
		}
	}
}

// IsRendering reports whether a render is currently in progress. Other
// subsystems use this to yield the CPU-starved snapshot pipeline (e.g.
// the Prusa uploader pauses during renders).
func (m *Manager) IsRendering() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rendering
}

// setRenderResult records a render outcome under the lock.
func (m *Manager) setRenderResult(job *Job, phase Phase, lastError string) {
	m.mu.Lock()
	job.Phase = phase
	job.LastError = lastError
	if lastError != "" {
		job.LastErrorAt = time.Now()
	}
	m.mu.Unlock()
	if phase == PhaseRenderFailed {
		m.logger.Error("timelapse render failed", "job", job.ID, "error", lastError)
	}
}

// renderError condenses a runner failure plus ffmpeg output into one
// actionable string (last portion of output only; ffmpeg is chatty).
func renderError(err error, out []byte) string {
	const tail = 400
	s := string(out)
	if len(s) > tail {
		s = "..." + s[len(s)-tail:]
	}
	if s == "" {
		return err.Error()
	}
	return fmt.Sprintf("%v: %s", err, s)
}
