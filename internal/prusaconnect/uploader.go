// Package prusaconnect periodically captures a snapshot from go2rtc's own
// HTTP API and uploads it to Prusa Connect's webcam ingestion endpoint.
// This is an advisory feature: upload failures are logged and retried on
// the next interval, they never affect camera streaming itself.
package prusaconnect

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/MakerEyeLabs/makereye/internal/config"
	"github.com/MakerEyeLabs/makereye/internal/go2rtc"
)

// snapshotURL is Prusa Connect's fixed webcam snapshot ingestion
// endpoint. It is not user-configurable, there is no self-hosted variant
// of Prusa Connect to point at instead.
const snapshotURL = "https://webcam.connect.prusa3d.com/c/snapshot"

// fetchTimeout bounds the go2rtc snapshot fetch, a same-host call that
// should be near-instant.
const fetchTimeout = 5 * time.Second

// uploadTimeout bounds the Prusa Connect upload, a real network call to
// an external service.
const uploadTimeout = 15 * time.Second

// maxSnapshotBytes caps how much of go2rtc's response body is read, as a
// sanity bound, not a real limit: JPEG snapshots at any sane camera
// resolution are a small fraction of this.
const maxSnapshotBytes = 32 << 20 // 32 MiB

// Phase describes the current lifecycle phase of the uploader.
type Phase string

const (
	// PhaseStopped means the upload loop is not running.
	PhaseStopped Phase = "stopped"
	// PhaseRunning means the upload loop is running (individual upload
	// failures do not leave this phase, see State.LastError).
	PhaseRunning Phase = "running"
)

// State is a snapshot of the uploader's current status.
type State struct {
	Phase        Phase
	StartedAt    time.Time
	UploadCount  int
	FailureCount int
	LastError    string
	LastUploadAt time.Time
}

// Uploader periodically pulls a JPEG snapshot from go2rtc's HTTP API and
// PUTs it to Prusa Connect.
type Uploader struct {
	cfg    *config.Config
	logger *slog.Logger
	client *http.Client

	// prusaURL defaults to snapshotURL; it's a field rather than a
	// direct use of the constant so tests can point it at an
	// httptest.Server.
	prusaURL string

	mu     sync.Mutex
	state  State
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewUploader creates an Uploader for cfg. It does not start uploading,
// callers decide whether to Start it (e.g. based on
// cfg.PrusaConnect.Enabled).
func NewUploader(cfg *config.Config, logger *slog.Logger) *Uploader {
	if logger == nil {
		logger = slog.Default()
	}
	return &Uploader{
		cfg:      cfg,
		logger:   logger,
		client:   &http.Client{},
		prusaURL: snapshotURL,
		state:    State{Phase: PhaseStopped},
	}
}

// Start begins the periodic capture/upload loop, at
// cfg.PrusaConnect.IntervalSeconds. Start returns immediately, it does
// not block for the uploader's lifetime.
func (u *Uploader) Start(ctx context.Context) error {
	u.mu.Lock()
	if u.state.Phase == PhaseRunning {
		u.mu.Unlock()
		return fmt.Errorf("prusa connect uploader is already running")
	}
	u.mu.Unlock()

	runCtx, cancel := context.WithCancel(context.Background())
	u.cancel = cancel

	u.mu.Lock()
	u.state = State{Phase: PhaseRunning, StartedAt: time.Now()}
	u.mu.Unlock()

	u.wg.Add(1)
	go u.loop(runCtx)

	return nil
}

// Stop stops the upload loop, waiting for any in-flight request to
// finish (or be cancelled).
func (u *Uploader) Stop(ctx context.Context) error {
	u.mu.Lock()
	if u.state.Phase == PhaseStopped {
		u.mu.Unlock()
		return nil
	}
	cancel := u.cancel
	u.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	done := make(chan struct{})
	go func() {
		u.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}

	u.mu.Lock()
	u.state.Phase = PhaseStopped
	u.mu.Unlock()
	return nil
}

// Restart stops and starts the upload loop, resetting counters.
func (u *Uploader) Restart(ctx context.Context) error {
	if err := u.Stop(ctx); err != nil {
		return err
	}
	return u.Start(ctx)
}

// Status returns a snapshot of the uploader's current state.
func (u *Uploader) Status() State {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.state
}

func (u *Uploader) loop(ctx context.Context) {
	defer u.wg.Done()

	interval := time.Duration(u.cfg.PrusaConnect.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Wait a full interval before the first capture instead of firing
	// immediately: at daemon startup the uploader would otherwise race
	// go2rtc binding its HTTP port and record a spurious failure on
	// every boot.
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		u.uploadOnce(ctx)
	}
}

func (u *Uploader) uploadOnce(ctx context.Context) {
	snap, err := u.fetchSnapshot(ctx)
	if err != nil {
		u.recordFailure(fmt.Errorf("fetching snapshot from go2rtc: %w", err))
		return
	}

	if err := u.pushSnapshot(ctx, snap); err != nil {
		u.recordFailure(fmt.Errorf("uploading to Prusa Connect: %w", err))
		return
	}

	u.recordSuccess()
}

// fetchSnapshot pulls a JPEG frame from go2rtc's own HTTP API. This is
// always a same-host call to MakerEye's own configured go2rtc.http_listen,
// authenticated the same way an external client would be if
// go2rtc.auth is set.
func (u *Uploader) fetchSnapshot(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	url := fmt.Sprintf("http://%s/api/frame.jpeg?src=%s",
		go2rtc.ClientHostPort(u.cfg.Go2rtc.HTTPListen), u.cfg.Stream.Name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if u.cfg.Go2rtc.Auth.Username != "" {
		req.SetBasicAuth(u.cfg.Go2rtc.Auth.Username, u.cfg.Go2rtc.Auth.Password)
	}

	resp, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSnapshotBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("go2rtc returned status %d", resp.StatusCode)
	}
	return body, nil
}

// pushSnapshot uploads a JPEG snapshot to Prusa Connect, following the
// headers Prusa Connect's webcam ingestion endpoint expects: a bearer
// "token" and a stable "fingerprint" identifying this camera.
func (u *Uploader) pushSnapshot(ctx context.Context, snapshot []byte) error {
	ctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.prusaURL, bytes.NewReader(snapshot))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "image/jpg")
	req.Header.Set("token", u.cfg.PrusaConnect.Token)
	req.Header.Set("fingerprint", u.cfg.PrusaConnect.Fingerprint)

	resp, err := u.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Prusa Connect returned status %d", resp.StatusCode)
	}
	return nil
}

func (u *Uploader) recordFailure(err error) {
	u.logger.Warn("prusa connect upload failed", "error", err)
	u.mu.Lock()
	u.state.FailureCount++
	u.state.LastError = err.Error()
	u.mu.Unlock()
}

func (u *Uploader) recordSuccess() {
	u.mu.Lock()
	u.state.UploadCount++
	u.state.LastUploadAt = time.Now()
	u.state.LastError = ""
	u.mu.Unlock()
}
