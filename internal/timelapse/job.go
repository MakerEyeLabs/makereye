// Package timelapse implements MakerEye's manual timelapse subsystem:
// at most one active capture job pulling JPEG frames from the shared
// snapshot source on a monotonic interval, with durable on-disk job
// state and ffmpeg rendering as a separate step. Advisory like the
// other subsystems: nothing here can take down streaming. Full design
// in DESIGN.md "Timelapse".
package timelapse

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Phase is a job's lifecycle phase.
type Phase string

const (
	// PhaseCapturing means frames are being captured on the interval.
	PhaseCapturing Phase = "capturing"
	// PhaseStopped means capture finished cleanly and no render has
	// succeeded yet.
	PhaseStopped Phase = "stopped"
	// PhaseRendering means ffmpeg is producing the output video.
	PhaseRendering Phase = "rendering"
	// PhaseComplete means a validated render exists.
	PhaseComplete Phase = "complete"
	// PhaseCaptureFailed means capture stopped itself: either
	// maxConsecutiveCaptureFailures snapshot failures in a row, or the
	// free-space threshold was hit. Frames are preserved.
	PhaseCaptureFailed Phase = "capture_failed"
	// PhaseRenderFailed means the last render attempt failed; frames
	// are preserved and rendering may be retried.
	PhaseRenderFailed Phase = "render_failed"
	// PhaseInterrupted means the daemon stopped while this job was
	// capturing and it was not resumed. It can still be rendered.
	PhaseInterrupted Phase = "interrupted"
)

// Job is the persisted manifest of one timelapse job (job.json).
type Job struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	IntervalSeconds int       `json:"interval_seconds"`
	PlaybackFPS     int       `json:"playback_fps"`
	StartedAt       time.Time `json:"started_at"`
	StoppedAt       time.Time `json:"stopped_at,omitzero"`
	Phase           Phase     `json:"phase"`
	FrameCount      int       `json:"frame_count"`
	FailureCount    int       `json:"failure_count"`
	LastError       string    `json:"last_error,omitempty"`
	// Dir is the absolute job directory; derived at load, not trusted
	// from the manifest (the tree may have been moved).
	Dir string `json:"-"`
	// OutputFile is the rendered video's filename within Dir.
	OutputFile string `json:"output_file,omitempty"`

	// Light/LightBrightness record an optional lighting hold applied
	// during capture (see DESIGN.md "Timelapse", Lighting hold).
	Light           string `json:"light,omitempty"`
	LightBrightness int    `json:"light_brightness,omitempty"`
}

// OutputPath is the absolute path of the rendered video, or "" if none.
func (j *Job) OutputPath() string {
	if j.OutputFile == "" {
		return ""
	}
	return filepath.Join(j.Dir, j.OutputFile)
}

// FramesDir is the absolute path of the job's frame directory.
func (j *Job) FramesDir() string { return filepath.Join(j.Dir, "frames") }

// framePath returns the absolute path for frame number n (1-based).
func (j *Job) framePath(n int) string {
	return filepath.Join(j.FramesDir(), fmt.Sprintf("%08d.jpg", n))
}

// sanitizeName makes a job name safe for use in a directory name.
func sanitizeName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	s := b.String()
	if len(s) > 40 {
		s = s[:40]
	}
	if s == "" {
		s = "timelapse"
	}
	return s
}

// writeFileAtomic writes data to path via a temp file + rename. sync
// controls whether the file is fsynced before the rename: manifests are
// synced (small, critical); frames are not (losing the newest frame on
// power cut is acceptable, per-frame fsync grinds SD cards).
func writeFileAtomic(path string, data []byte, sync bool) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if sync {
		if err := tmp.Sync(); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// saveManifest persists the job manifest atomically.
func saveManifest(j *Job) error {
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(j.Dir, "job.json"), data, true)
}

// loadJobs scans dir for job manifests. Frame counts are reconciled
// against the files actually on disk, since manifests are only
// persisted every few frames.
func loadJobs(dir string) ([]*Job, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var jobs []*Job
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		jobDir := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(filepath.Join(jobDir, "job.json"))
		if err != nil {
			continue // not a job dir, or unreadable; skip, never delete
		}
		var j Job
		if err := json.Unmarshal(data, &j); err != nil {
			continue
		}
		j.Dir = jobDir
		j.FrameCount = countFrames(j.FramesDir())
		jobs = append(jobs, &j)
	}
	sort.Slice(jobs, func(a, b int) bool { return jobs[a].StartedAt.Before(jobs[b].StartedAt) })
	return jobs, nil
}

func countFrames(framesDir string) int {
	entries, err := os.ReadDir(framesDir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jpg") && !strings.HasPrefix(e.Name(), ".") {
			n++
		}
	}
	return n
}
