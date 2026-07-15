package timelapse

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/MakerEyeLabs/makereye/internal/config"
	"github.com/MakerEyeLabs/makereye/internal/snapshot"
)

// maxConsecutiveCaptureFailures bounds how long a job keeps retrying
// failed captures before giving up (entering PhaseCaptureFailed with
// frames preserved) -- the capture-side analogue of the go2rtc
// supervisor's bounded restarts.
const maxConsecutiveCaptureFailures = 10

// manifestPersistEvery controls how often the manifest is rewritten
// during steady capture. Frame counts are reconciled from disk at load,
// so a slightly stale manifest is harmless, and this keeps SD writes
// down at short intervals.
const manifestPersistEvery = 10

// LightHooks let a job hold a configured light at a brightness during
// capture without this package depending on internal/lighting. Any of
// them may be nil when lighting is disabled.
type LightHooks struct {
	// States returns current brightness per light name.
	States func() map[string]int
	// Set commands a light to a level.
	Set func(name string, level int) error
}

// Manager owns timelapse jobs: at most one actively capturing, plus the
// on-disk history of past jobs.
type Manager struct {
	cfg    *config.Config
	logger *slog.Logger
	source snapshot.Source
	lights LightHooks

	// onChange, when set, is invoked after phase transitions (job
	// started/stopped/failed, render started/finished) -- the daemon
	// uses it to push MQTT state promptly. Never called per frame.
	onChange func()

	// freeBytes reports available bytes on the filesystem holding path;
	// injectable for tests.
	freeBytes func(path string) (uint64, error)

	// newTicker returns a tick channel and a stop func; injectable so
	// capture-loop tests are deterministic instead of sleeping.
	newTicker func(d time.Duration) (<-chan time.Time, func())

	// runRender is the render entry point, split out in render.go and
	// injectable at the command-runner level (see renderRunner).
	renderRunner CommandRunner

	mu       sync.Mutex
	jobs     map[string]*Job // by ID, including finished ones
	active   *Job            // job currently in PhaseCapturing, if any
	stopCh   chan struct{}   // closes to stop the active capture loop
	captureW sync.WaitGroup

	rendering bool // one render at a time, device-wide

	heldLight string // name of the light a hold is applied to, "" if none
	prevLight int    // that light's level before the hold
	loaded    bool
	initErr   error // Start failure, surfaced by every later operation
	shutdown  bool  // daemon is stopping: active jobs stay "capturing" on disk
}

// NewManager creates a Manager. Call Start to load state from disk.
func NewManager(cfg *config.Config, source snapshot.Source, lights LightHooks, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		cfg:    cfg,
		logger: logger,
		source: source,
		lights: lights,
		freeBytes: func(path string) (uint64, error) {
			var st unix.Statfs_t
			if err := unix.Statfs(path, &st); err != nil {
				return 0, err
			}
			return uint64(st.Bavail) * uint64(st.Bsize), nil
		},
		newTicker: func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		},
		renderRunner: defaultRunner,
		jobs:         map[string]*Job{},
	}
}

// SetOnChange registers the phase-transition callback.
func (m *Manager) SetOnChange(fn func()) { m.onChange = fn }

func (m *Manager) notify() {
	if m.onChange != nil {
		m.onChange()
	}
}

// Start loads job history from the output directory and handles jobs
// that were capturing when the daemon stopped: they are resumed when
// cfg.Timelapse.ResumeInterrupted is set, otherwise marked interrupted.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.loaded {
		m.mu.Unlock()
		return fmt.Errorf("timelapse manager is already started")
	}
	m.loaded = true
	m.mu.Unlock()

	dir := m.cfg.TimelapseOutputDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		err = fmt.Errorf("creating timelapse output dir %q: %w (note: the service cannot use paths under /home -- ProtectHome -- and needs write access; /var/lib/makereye, /mnt, or /media work)", dir, err)
		m.mu.Lock()
		m.initErr = err
		m.mu.Unlock()
		return err
	}

	jobs, err := loadJobs(dir)
	if err != nil {
		err = fmt.Errorf("loading timelapse jobs from %q: %w", dir, err)
		m.mu.Lock()
		m.initErr = err
		m.mu.Unlock()
		return err
	}

	var toResume *Job
	m.mu.Lock()
	for _, j := range jobs {
		if j.Phase == PhaseCapturing || j.Phase == PhaseRendering {
			// The daemon died mid-capture or mid-render.
			if j.Phase == PhaseCapturing && m.cfg.Timelapse.ResumeInterrupted && toResume == nil {
				toResume = j
			} else {
				j.Phase = PhaseInterrupted
				j.LastError = "daemon stopped while job was active"
				_ = saveManifest(j)
			}
		}
		m.jobs[j.ID] = j
	}
	m.mu.Unlock()

	if toResume != nil {
		m.logger.Info("resuming interrupted timelapse job",
			"job", toResume.ID, "name", toResume.Name, "frames", toResume.FrameCount)
		if err := m.beginCapture(toResume, true); err != nil {
			m.logger.Warn("could not resume timelapse job, marking interrupted",
				"job", toResume.ID, "error", err)
			toResume.Phase = PhaseInterrupted
			toResume.LastError = "resume failed: " + err.Error()
			_ = saveManifest(toResume)
		}
	}
	return nil
}

// Stop halts any active capture and waits for the capture goroutine.
// Unlike StopJob, this is a daemon shutdown: the job's manifest is left
// in the "capturing" phase so the next daemon start resumes it (or
// marks it interrupted, per config) -- a restart or update must not
// silently end a long-running capture. Renders in flight are abandoned
// to their context.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	m.shutdown = true
	stopCh := m.stopCh
	m.mu.Unlock()
	if stopCh != nil {
		select {
		case <-stopCh:
		default:
			close(stopCh)
		}
	}
	done := make(chan struct{})
	go func() {
		m.captureW.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// JobParams are the per-job choices made at start time; zero values
// fall back to the configured defaults.
type JobParams struct {
	Name            string
	IntervalSeconds int
	PlaybackFPS     int
	Light           string
	LightBrightness int
}

// StartJob begins a new capture job. It fails clearly when another job
// is capturing, free space is below the threshold, the output dir is
// not writable, or the camera stream is unavailable (the first frame is
// captured synchronously as the liveness check).
func (m *Manager) StartJob(params JobParams) (Job, error) {
	m.mu.Lock()
	if !m.loaded {
		m.mu.Unlock()
		return Job{}, fmt.Errorf("timelapse is not running")
	}
	if m.initErr != nil {
		err := m.initErr
		m.mu.Unlock()
		return Job{}, fmt.Errorf("timelapse failed to initialize: %w", err)
	}
	if m.active != nil {
		id := m.active.ID
		m.mu.Unlock()
		return Job{}, fmt.Errorf("a timelapse job is already capturing (%s); stop it first", id)
	}
	m.mu.Unlock()

	interval := params.IntervalSeconds
	if interval <= 0 {
		interval = m.cfg.Timelapse.DefaultIntervalSeconds
	}
	fps := params.PlaybackFPS
	if fps <= 0 {
		fps = m.cfg.Timelapse.DefaultPlaybackFPS
	}
	if interval < 1 || interval > 3600 {
		return Job{}, fmt.Errorf("capture interval must be 1-3600 seconds, got %d", interval)
	}
	if fps < 1 || fps > 120 {
		return Job{}, fmt.Errorf("playback fps must be 1-120, got %d", fps)
	}
	name := params.Name
	if name == "" {
		name = time.Now().Format("timelapse-20060102-150405")
	}
	light := params.Light
	brightness := params.LightBrightness
	if light == "" {
		light = m.cfg.Timelapse.Light
		brightness = m.cfg.Timelapse.LightBrightness
	}

	if err := m.checkFreeSpace(); err != nil {
		return Job{}, err
	}

	suffix := make([]byte, 2)
	_, _ = rand.Read(suffix)
	id := time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(suffix)

	job := &Job{
		ID:              id,
		Name:            name,
		IntervalSeconds: interval,
		PlaybackFPS:     fps,
		StartedAt:       time.Now(),
		Phase:           PhaseCapturing,
		Light:           light,
		LightBrightness: brightness,
	}
	job.Dir = fmt.Sprintf("%s/%s-%s", m.cfg.TimelapseOutputDir(), id, sanitizeName(name))

	if err := os.MkdirAll(job.FramesDir(), 0o750); err != nil {
		return Job{}, fmt.Errorf("creating job directory: %w", err)
	}

	if err := m.beginCapture(job, false); err != nil {
		return Job{}, err
	}
	return *job, nil
}

// beginCapture applies the lighting hold, captures the first frame
// synchronously (unless resuming), persists the manifest, and starts
// the capture loop.
func (m *Manager) beginCapture(job *Job, resuming bool) error {
	m.applyLightHold(job)

	if !resuming {
		if err := m.captureFrame(job); err != nil {
			m.releaseLightHold()
			return fmt.Errorf("camera stream unavailable (first frame capture failed): %w", err)
		}
	}
	job.Phase = PhaseCapturing
	if err := saveManifest(job); err != nil {
		m.releaseLightHold()
		return fmt.Errorf("writing job manifest: %w", err)
	}

	stopCh := make(chan struct{})
	m.mu.Lock()
	m.active = job
	m.jobs[job.ID] = job
	m.stopCh = stopCh
	m.mu.Unlock()

	m.captureW.Add(1)
	go m.captureLoop(job, stopCh)
	m.notify()
	return nil
}

// StopJob stops the active capture job, finalizes it as stopped, and
// (if configured) kicks off an automatic render. Idempotent: stopping
// with no active job returns the most recent job without error.
func (m *Manager) StopJob() (Job, error) {
	m.mu.Lock()
	job := m.active
	stopCh := m.stopCh
	m.mu.Unlock()

	if job == nil {
		last, _ := m.LastJob()
		return last, nil
	}
	select {
	case <-stopCh:
	default:
		close(stopCh)
	}
	m.captureW.Wait() // finalization happens in the loop's defer

	m.mu.Lock()
	frames := job.FrameCount
	id := job.ID
	snapshot := *job
	m.mu.Unlock()

	if m.cfg.Timelapse.AutoRender && frames > 0 {
		if err := m.Render(id); err != nil {
			m.logger.Warn("auto-render failed to start", "job", id, "error", err)
		}
	}
	return snapshot, nil
}

// captureLoop runs until stopped or the job fails itself.
func (m *Manager) captureLoop(job *Job, stopCh chan struct{}) {
	defer m.captureW.Done()

	interval := time.Duration(job.IntervalSeconds) * time.Second
	tick, stopTicker := m.newTicker(interval)
	defer stopTicker()

	consecutive := 0
	finalPhase := PhaseStopped
	defer func() {
		m.mu.Lock()
		if m.active == job {
			m.active = nil
			m.stopCh = nil
		}
		// A clean stop during daemon shutdown keeps the on-disk phase
		// "capturing" so the next start resumes/flags it; self-failures
		// (capture_failed) are final regardless.
		if finalPhase == PhaseStopped && m.shutdown {
			finalPhase = PhaseCapturing
		}
		job.Phase = finalPhase
		if finalPhase != PhaseCapturing {
			job.StoppedAt = time.Now()
		}
		m.mu.Unlock()

		if err := saveManifest(job); err != nil {
			m.logger.Error("persisting job manifest at stop", "job", job.ID, "error", err)
		}
		m.releaseLightHold()
		m.notify()
	}()

	for {
		select {
		case <-stopCh:
			return
		case <-tick:
		}

		if err := m.checkFreeSpace(); err != nil {
			m.mu.Lock()
			job.LastError = err.Error()
			m.mu.Unlock()
			finalPhase = PhaseCaptureFailed
			m.logger.Error("timelapse capture stopped", "job", job.ID, "error", err)
			return
		}

		if err := m.captureFrame(job); err != nil {
			consecutive++
			m.mu.Lock()
			job.FailureCount++
			job.LastError = err.Error()
			m.mu.Unlock()
			m.logger.Warn("timelapse frame capture failed",
				"job", job.ID, "consecutive", consecutive, "error", err)
			if consecutive >= maxConsecutiveCaptureFailures {
				m.mu.Lock()
				job.LastError = fmt.Sprintf("%d consecutive capture failures, last: %s",
					consecutive, err)
				m.mu.Unlock()
				finalPhase = PhaseCaptureFailed
				return
			}
			continue
		}
		consecutive = 0
		m.mu.Lock()
		job.LastError = ""
		m.mu.Unlock()
		if job.FrameCount%manifestPersistEvery == 0 {
			if err := saveManifest(job); err != nil {
				m.logger.Warn("persisting job manifest", "job", job.ID, "error", err)
			}
		}
	}
}

// captureFrame fetches one frame and writes it atomically as the next
// numbered file.
func (m *Manager) captureFrame(job *Job) error {
	timeout := time.Duration(m.cfg.Timelapse.SnapshotTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	frame, err := m.source.Capture(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	next := job.FrameCount + 1
	m.mu.Unlock()
	if err := writeFileAtomic(job.framePath(next), frame.Data, false); err != nil {
		return fmt.Errorf("writing frame: %w", err)
	}
	m.mu.Lock()
	job.FrameCount = next
	m.mu.Unlock()
	return nil
}

// checkFreeSpace enforces the configured minimum free space on the
// output filesystem.
func (m *Manager) checkFreeSpace() error {
	minBytes := uint64(m.cfg.Timelapse.MinimumFreeSpaceMB) * 1024 * 1024
	if minBytes == 0 {
		return nil
	}
	free, err := m.freeBytes(m.cfg.TimelapseOutputDir())
	if err != nil {
		return fmt.Errorf("checking free space for %q: %w (note: the service cannot access paths under /home -- ProtectHome)", m.cfg.TimelapseOutputDir(), err)
	}
	if free < minBytes {
		return fmt.Errorf("free space %d MB is below the configured minimum %d MB; capture stopped, frames preserved (free up space or lower timelapse.minimum_free_space_mb)",
			free/1024/1024, m.cfg.Timelapse.MinimumFreeSpaceMB)
	}
	return nil
}

// applyLightHold pins the job's light (if any) at its configured
// brightness, remembering the previous level.
func (m *Manager) applyLightHold(job *Job) {
	if job.Light == "" || m.lights.Set == nil || m.lights.States == nil {
		return
	}
	states := m.lights.States()
	prev, ok := states[job.Light]
	if !ok {
		m.logger.Warn("timelapse light hold: light not found", "light", job.Light)
		return
	}
	if err := m.lights.Set(job.Light, job.LightBrightness); err != nil {
		m.logger.Warn("timelapse light hold failed", "light", job.Light, "error", err)
		return
	}
	m.mu.Lock()
	m.heldLight = job.Light
	m.prevLight = prev
	m.mu.Unlock()
}

// releaseLightHold restores the held light to its pre-hold level.
func (m *Manager) releaseLightHold() {
	m.mu.Lock()
	name := m.heldLight
	prev := m.prevLight
	m.heldLight = ""
	m.mu.Unlock()
	if name == "" || m.lights.Set == nil {
		return
	}
	if err := m.lights.Set(name, prev); err != nil {
		m.logger.Warn("restoring light after timelapse", "light", name, "error", err)
	}
}

// Active returns a copy of the currently capturing job.
func (m *Manager) Active() (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil {
		return Job{}, false
	}
	return *m.active, true
}

// Get returns a copy of a job by ID.
func (m *Manager) Get(id string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *j, true
}

// List returns copies of all known jobs, oldest first.
func (m *Manager) List() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, *j)
	}
	sortJobs(out)
	return out
}

// LastJob returns a copy of the most recently started job.
func (m *Manager) LastJob() (Job, bool) {
	jobs := m.List()
	if len(jobs) == 0 {
		return Job{}, false
	}
	return jobs[len(jobs)-1], true
}

func sortJobs(jobs []Job) {
	for i := 1; i < len(jobs); i++ {
		for k := i; k > 0 && jobs[k].StartedAt.Before(jobs[k-1].StartedAt); k-- {
			jobs[k], jobs[k-1] = jobs[k-1], jobs[k]
		}
	}
}
