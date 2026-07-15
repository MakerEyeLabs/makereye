package timelapse

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MakerEyeLabs/makereye/internal/config"
	"github.com/MakerEyeLabs/makereye/internal/snapshot"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

var testJPEGOnce = sync.OnceValue(func() []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
		panic(err)
	}
	return buf.Bytes()
})

// fakeSource returns the test JPEG, or fails when told to.
type fakeSource struct {
	mu   sync.Mutex
	fail bool
}

func (f *fakeSource) setFail(v bool) {
	f.mu.Lock()
	f.fail = v
	f.mu.Unlock()
}

func (f *fakeSource) Capture(ctx context.Context) (snapshot.Frame, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return snapshot.Frame{}, fmt.Errorf("stream unavailable")
	}
	return snapshot.Frame{Data: testJPEGOnce(), ContentType: "image/jpeg", CapturedAt: time.Now()}, nil
}

// testEnv bundles a manager with its injected fakes.
type testEnv struct {
	m      *Manager
	source *fakeSource
	tick   chan time.Time
	free   *uint64
	runner *fakeRunner
	cfg    *config.Config
}

type fakeRunner struct {
	mu       sync.Mutex
	calls    [][]string
	fail     bool
	makeFile bool // write the output file like real ffmpeg would
}

func (r *fakeRunner) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string{name}, args...))
	fail := r.fail
	makeFile := r.makeFile
	r.mu.Unlock()
	if fail {
		return []byte("ffmpeg: something exploded"), fmt.Errorf("exit status 1")
	}
	if makeFile {
		out := args[len(args)-1]
		if err := os.WriteFile(out, []byte("fake-mp4"), 0o644); err != nil {
			return nil, err
		}
	}
	return []byte("ok"), nil
}

func (r *fakeRunner) lastCall() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return nil
	}
	return r.calls[len(r.calls)-1]
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	cfg := config.Default()
	cfg.Timelapse.Enabled = true
	cfg.Timelapse.OutputDir = t.TempDir()
	cfg.Timelapse.AutoRender = false
	cfg.Timelapse.SnapshotTimeoutSeconds = 2

	free := uint64(10 * 1024 * 1024 * 1024) // 10 GiB
	env := &testEnv{
		source: &fakeSource{},
		tick:   make(chan time.Time),
		free:   &free,
		runner: &fakeRunner{makeFile: true},
		cfg:    cfg,
	}
	env.m = NewManager(cfg, env.source, LightHooks{}, testLogger())
	env.m.freeBytes = func(string) (uint64, error) { return *env.free, nil }
	env.m.newTicker = func(time.Duration) (<-chan time.Time, func()) { return env.tick, func() {} }
	env.m.renderRunner = env.runner.run

	if err := env.m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { env.m.Stop(context.Background()) })
	return env
}

// tickAndWait delivers n ticks, waiting for each to be consumed.
func (e *testEnv) tickN(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case e.tick <- time.Now():
		case <-time.After(2 * time.Second):
			t.Fatal("capture loop did not consume tick")
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestJobLifecycle(t *testing.T) {
	env := newTestEnv(t)

	job, err := env.m.StartJob(JobParams{Name: "Test Print", IntervalSeconds: 30, PlaybackFPS: 24})
	if err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	if job.Phase != PhaseCapturing || job.FrameCount != 1 {
		t.Errorf("after start: phase=%s frames=%d, want capturing/1 (synchronous first frame)", job.Phase, job.FrameCount)
	}

	env.tickN(t, 3)
	waitFor(t, "4 frames", func() bool { a, _ := env.m.Active(); return a.FrameCount == 4 })

	stopped, err := env.m.StopJob()
	if err != nil {
		t.Fatalf("StopJob: %v", err)
	}
	if stopped.Phase != PhaseStopped && stopped.Phase != PhaseCapturing {
		// phase is finalized by the loop defer; re-read from manager
	}
	got, _ := env.m.Get(job.ID)
	if got.Phase != PhaseStopped {
		t.Errorf("phase after stop = %s, want stopped", got.Phase)
	}
	if got.FrameCount != 4 {
		t.Errorf("frames = %d, want 4", got.FrameCount)
	}

	// Frames on disk, zero-padded, plus a manifest.
	if _, err := os.Stat(filepath.Join(got.Dir, "frames", "00000004.jpg")); err != nil {
		t.Errorf("expected frame 00000004.jpg: %v", err)
	}
	if _, err := os.Stat(filepath.Join(got.Dir, "job.json")); err != nil {
		t.Errorf("expected job.json: %v", err)
	}

	// Stop is idempotent.
	if _, err := env.m.StopJob(); err != nil {
		t.Errorf("second StopJob should not error: %v", err)
	}
}

func TestStartRejectsSecondJob(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.m.StartJob(JobParams{}); err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	if _, err := env.m.StartJob(JobParams{}); err == nil ||
		!strings.Contains(err.Error(), "already capturing") {
		t.Errorf("expected already-capturing error, got %v", err)
	}
}

func TestStartFailsWhenStreamUnavailable(t *testing.T) {
	env := newTestEnv(t)
	env.source.setFail(true)
	if _, err := env.m.StartJob(JobParams{}); err == nil ||
		!strings.Contains(err.Error(), "stream unavailable") {
		t.Errorf("expected stream-unavailable error, got %v", err)
	}
}

func TestStartFailsBelowFreeSpaceThreshold(t *testing.T) {
	env := newTestEnv(t)
	*env.free = 100 * 1024 * 1024 // below the 1024 MB default minimum
	if _, err := env.m.StartJob(JobParams{}); err == nil ||
		!strings.Contains(err.Error(), "free space") {
		t.Errorf("expected free-space error, got %v", err)
	}
}

func TestFreeSpaceExhaustionStopsCapture(t *testing.T) {
	env := newTestEnv(t)
	job, err := env.m.StartJob(JobParams{})
	if err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	*env.free = 0
	env.tickN(t, 1)
	waitFor(t, "capture_failed", func() bool {
		j, _ := env.m.Get(job.ID)
		return j.Phase == PhaseCaptureFailed
	})
	j, _ := env.m.Get(job.ID)
	if !strings.Contains(j.LastError, "free space") {
		t.Errorf("LastError = %q, want free-space explanation", j.LastError)
	}
	if j.FrameCount != 1 {
		t.Errorf("frames should be preserved, got %d", j.FrameCount)
	}
}

func TestConsecutiveFailuresEndCapture(t *testing.T) {
	env := newTestEnv(t)
	job, err := env.m.StartJob(JobParams{})
	if err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	env.source.setFail(true)
	env.tickN(t, maxConsecutiveCaptureFailures)
	waitFor(t, "capture_failed", func() bool {
		j, _ := env.m.Get(job.ID)
		return j.Phase == PhaseCaptureFailed
	})
	j, _ := env.m.Get(job.ID)
	if j.FailureCount != maxConsecutiveCaptureFailures {
		t.Errorf("failure count = %d, want %d", j.FailureCount, maxConsecutiveCaptureFailures)
	}
}

func TestSingleFailureIsRetried(t *testing.T) {
	env := newTestEnv(t)
	job, err := env.m.StartJob(JobParams{})
	if err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	env.source.setFail(true)
	env.tickN(t, 1)
	waitFor(t, "one failure", func() bool { j, _ := env.m.Get(job.ID); return j.FailureCount == 1 })
	env.source.setFail(false)
	env.tickN(t, 1)
	waitFor(t, "recovery", func() bool { j, _ := env.m.Get(job.ID); return j.FrameCount == 2 })
	j, _ := env.m.Get(job.ID)
	if j.Phase != PhaseCapturing || j.LastError != "" {
		t.Errorf("after recovery: phase=%s lastError=%q", j.Phase, j.LastError)
	}
}

func TestRenderSuccess(t *testing.T) {
	env := newTestEnv(t)
	job, _ := env.m.StartJob(JobParams{Name: "render me", PlaybackFPS: 24})
	env.tickN(t, 2)
	env.m.StopJob()

	if err := env.m.Render(job.ID); err != nil {
		t.Fatalf("Render: %v", err)
	}
	waitFor(t, "complete", func() bool { j, _ := env.m.Get(job.ID); return j.Phase == PhaseComplete })

	j, _ := env.m.Get(job.ID)
	if j.OutputPath() == "" {
		t.Fatal("expected output path")
	}
	if _, err := os.Stat(j.OutputPath()); err != nil {
		t.Errorf("output file missing: %v", err)
	}
	// Frames retained by default.
	if j.FrameCount == 0 {
		t.Error("frames should be retained after render by default")
	}

	call := env.runner.lastCall()
	if call[0] != "ffmpeg" {
		t.Errorf("runner invoked %q, want ffmpeg", call[0])
	}
	joined := strings.Join(call, " ")
	if !strings.Contains(joined, "-framerate 24") || !strings.Contains(joined, "%08d.jpg") ||
		!strings.Contains(joined, "-c:v libx264") {
		t.Errorf("unexpected ffmpeg args: %s", joined)
	}
}

func TestRenderFailurePreservesFrames(t *testing.T) {
	env := newTestEnv(t)
	job, _ := env.m.StartJob(JobParams{})
	env.tickN(t, 2)
	env.m.StopJob()

	env.runner.fail = true
	if err := env.m.Render(job.ID); err != nil {
		t.Fatalf("Render: %v", err)
	}
	waitFor(t, "render_failed", func() bool { j, _ := env.m.Get(job.ID); return j.Phase == PhaseRenderFailed })

	j, _ := env.m.Get(job.ID)
	if !strings.Contains(j.LastError, "exploded") {
		t.Errorf("LastError should carry ffmpeg output, got %q", j.LastError)
	}
	if countFrames(j.FramesDir()) != 3 {
		t.Errorf("frames must survive a failed render, found %d", countFrames(j.FramesDir()))
	}

	// Retry succeeds.
	env.runner.fail = false
	if err := env.m.Render(job.ID); err != nil {
		t.Fatalf("retry Render: %v", err)
	}
	waitFor(t, "complete after retry", func() bool { j, _ := env.m.Get(job.ID); return j.Phase == PhaseComplete })
}

func TestRenderRefusesWhileCapturing(t *testing.T) {
	env := newTestEnv(t)
	job, _ := env.m.StartJob(JobParams{})
	if err := env.m.Render(job.ID); err == nil ||
		!strings.Contains(err.Error(), "still capturing") {
		t.Errorf("expected still-capturing refusal, got %v", err)
	}
}

func TestRenderUnknownJob(t *testing.T) {
	env := newTestEnv(t)
	if err := env.m.Render("nope"); err == nil {
		t.Error("expected unknown-job error")
	}
}

func TestRetainFramesFalseDeletesAfterValidatedRender(t *testing.T) {
	env := newTestEnv(t)
	env.cfg.Timelapse.RetainFrames = false
	job, _ := env.m.StartJob(JobParams{})
	env.tickN(t, 2)
	env.m.StopJob()

	if err := env.m.Render(job.ID); err != nil {
		t.Fatalf("Render: %v", err)
	}
	waitFor(t, "complete", func() bool { j, _ := env.m.Get(job.ID); return j.Phase == PhaseComplete })
	j, _ := env.m.Get(job.ID)
	if countFrames(j.FramesDir()) != 0 {
		t.Error("frames should be removed after a validated render when retain_frames is false")
	}
	if _, err := os.Stat(j.OutputPath()); err != nil {
		t.Errorf("rendered output must exist: %v", err)
	}
}

func TestAutoRenderOnStop(t *testing.T) {
	env := newTestEnv(t)
	env.cfg.Timelapse.AutoRender = true
	job, _ := env.m.StartJob(JobParams{})
	env.tickN(t, 1)
	env.m.StopJob()
	waitFor(t, "auto-rendered complete", func() bool {
		j, _ := env.m.Get(job.ID)
		return j.Phase == PhaseComplete
	})
}

func TestReloadAfterRestartMarksInterrupted(t *testing.T) {
	env := newTestEnv(t)
	job, _ := env.m.StartJob(JobParams{Name: "interrupted"})
	env.tickN(t, 2)
	// Simulate a crash: abandon without StopJob. Manifest still says
	// capturing (persisted at start).
	env.m.Stop(context.Background())
	// Force the manifest back to capturing to simulate a hard kill
	// (clean Stop finalizes to stopped).
	j, _ := env.m.Get(job.ID)
	jc := j
	jc.Phase = PhaseCapturing
	if err := saveManifest(&jc); err != nil {
		t.Fatal(err)
	}

	cfg2 := env.cfg
	cfg2.Timelapse.ResumeInterrupted = false
	m2 := NewManager(cfg2, env.source, LightHooks{}, testLogger())
	m2.freeBytes = env.m.freeBytes
	if err := m2.Start(context.Background()); err != nil {
		t.Fatalf("reload Start: %v", err)
	}
	defer m2.Stop(context.Background())

	got, ok := m2.Get(job.ID)
	if !ok {
		t.Fatal("job not found after reload")
	}
	if got.Phase != PhaseInterrupted {
		t.Errorf("phase after reload = %s, want interrupted", got.Phase)
	}
	if got.FrameCount != 3 {
		t.Errorf("frame count reconciled from disk = %d, want 3", got.FrameCount)
	}

	// An interrupted job can still be rendered.
	m2.renderRunner = env.runner.run
	if err := m2.Render(job.ID); err != nil {
		t.Fatalf("Render interrupted job: %v", err)
	}
	waitFor(t, "interrupted job rendered", func() bool {
		j, _ := m2.Get(job.ID)
		return j.Phase == PhaseComplete
	})
}

func TestReloadResumesWhenConfigured(t *testing.T) {
	env := newTestEnv(t)
	job, _ := env.m.StartJob(JobParams{Name: "resume-me", IntervalSeconds: 30})
	env.tickN(t, 2)
	env.m.Stop(context.Background())
	j, _ := env.m.Get(job.ID)
	jc := j
	jc.Phase = PhaseCapturing
	if err := saveManifest(&jc); err != nil {
		t.Fatal(err)
	}

	tick2 := make(chan time.Time)
	m2 := NewManager(env.cfg, env.source, LightHooks{}, testLogger())
	m2.freeBytes = env.m.freeBytes
	m2.newTicker = func(time.Duration) (<-chan time.Time, func()) { return tick2, func() {} }
	if err := m2.Start(context.Background()); err != nil {
		t.Fatalf("reload Start: %v", err)
	}
	defer m2.Stop(context.Background())

	active, ok := m2.Active()
	if !ok || active.ID != job.ID {
		t.Fatalf("expected job %s to resume, active=%v ok=%v", job.ID, active.ID, ok)
	}

	// New frames continue the numbering.
	tick2 <- time.Now()
	waitFor(t, "resumed frame", func() bool { j, _ := m2.Get(job.ID); return j.FrameCount == 4 })
	got, _ := m2.Get(job.ID)
	if _, err := os.Stat(filepath.Join(got.Dir, "frames", "00000004.jpg")); err != nil {
		t.Errorf("resumed frame should continue numbering: %v", err)
	}
}

func TestDaemonShutdownKeepsJobResumable(t *testing.T) {
	env := newTestEnv(t)
	job, err := env.m.StartJob(JobParams{Name: "long-print"})
	if err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	env.tickN(t, 1)

	// Daemon shutdown (Manager.Stop), NOT a user StopJob: the on-disk
	// manifest must still say capturing so the next start resumes it.
	if err := env.m.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	jobs, err := loadJobs(env.cfg.TimelapseOutputDir())
	if err != nil || len(jobs) != 1 {
		t.Fatalf("loadJobs: %v (%d jobs)", err, len(jobs))
	}
	if jobs[0].Phase != PhaseCapturing {
		t.Errorf("phase on disk after daemon shutdown = %s, want capturing (resumable)", jobs[0].Phase)
	}
	if jobs[0].ID != job.ID {
		t.Errorf("unexpected job on disk: %s", jobs[0].ID)
	}
}

func TestLightHoldAppliedAndRestored(t *testing.T) {
	env := newTestEnv(t)
	var mu sync.Mutex
	levels := map[string]int{"spot": 40}
	var sets []string
	env.m.lights = LightHooks{
		States: func() map[string]int {
			mu.Lock()
			defer mu.Unlock()
			return map[string]int{"spot": levels["spot"]}
		},
		Set: func(name string, level int) error {
			mu.Lock()
			defer mu.Unlock()
			levels[name] = level
			sets = append(sets, fmt.Sprintf("%s=%d", name, level))
			return nil
		},
	}

	_, err := env.m.StartJob(JobParams{Light: "spot", LightBrightness: 255})
	if err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	mu.Lock()
	held := levels["spot"]
	mu.Unlock()
	if held != 255 {
		t.Errorf("light during capture = %d, want 255", held)
	}

	env.m.StopJob()
	mu.Lock()
	restored := levels["spot"]
	history := strings.Join(sets, ",")
	mu.Unlock()
	if restored != 40 {
		t.Errorf("light after stop = %d, want restored 40 (history: %s)", restored, history)
	}
}

func TestFfmpegArgs(t *testing.T) {
	j := &Job{Name: "My Print", PlaybackFPS: 30, Dir: "/data/x", OutputFile: "my-print.mp4"}
	args := strings.Join(ffmpegArgs(j, "libx264"), " ")
	for _, want := range []string{
		"-framerate 30", "/data/x/frames/%08d.jpg", "-c:v libx264",
		"-pix_fmt yuv420p", "/data/x/my-print.mp4",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("args missing %q: %s", want, args)
		}
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"My Print Job":  "my-print-job",
		"weird/../name": "weirdname",
		"":              "timelapse",
		"UPPER_case-1":  "upper_case-1",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}
