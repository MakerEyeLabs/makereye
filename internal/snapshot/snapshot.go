// Package snapshot provides the shared "give me one JPEG frame now"
// source used by subsystems that consume still frames (the Prusa
// Connect uploader, timelapse). It exists so job logic doesn't depend
// on go2rtc HTTP details directly, and as the seam where coordinated
// full-resolution still capture can slot in later. It is deliberately
// narrow -- not a plugin framework.
package snapshot

import (
	"bytes"
	"context"
	"fmt"
	"image/jpeg"
	"io"
	"net/http"
	"time"

	"github.com/MakerEyeLabs/makereye/internal/config"
	"github.com/MakerEyeLabs/makereye/internal/go2rtc"
)

// maxFrameBytes caps how much of a response body is read; a sanity
// bound, not a real limit (JPEGs at any sane camera resolution are a
// small fraction of this).
const maxFrameBytes = 32 << 20 // 32 MiB

// Frame is one captured still image.
type Frame struct {
	Data        []byte
	ContentType string
	CapturedAt  time.Time
}

// Source captures a single frame. Implementations must be safe for
// concurrent use.
type Source interface {
	Capture(ctx context.Context) (Frame, error)
}

// Go2rtcSource fetches JPEG frames from go2rtc's /api/frame.jpeg
// endpoint for the configured stream, using the same authentication an
// external client would.
type Go2rtcSource struct {
	cfg    *config.Config
	client *http.Client
}

// NewGo2rtcSource creates a Source backed by cfg's go2rtc pipeline.
func NewGo2rtcSource(cfg *config.Config) *Go2rtcSource {
	return &Go2rtcSource{cfg: cfg, client: &http.Client{}}
}

// Capture fetches and validates one JPEG frame. The caller bounds the
// operation via ctx.
func (s *Go2rtcSource) Capture(ctx context.Context) (Frame, error) {
	url := fmt.Sprintf("http://%s/api/frame.jpeg?src=%s",
		go2rtc.ClientHostPort(s.cfg.Go2rtc.HTTPListen), s.cfg.Stream.Name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Frame{}, err
	}
	if s.cfg.Go2rtc.Auth.Username != "" {
		req.SetBasicAuth(s.cfg.Go2rtc.Auth.Username, s.cfg.Go2rtc.Auth.Password)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return Frame{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFrameBytes))
	if err != nil {
		return Frame{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Frame{}, fmt.Errorf("go2rtc returned status %d", resp.StatusCode)
	}
	if err := validateJPEG(body); err != nil {
		return Frame{}, err
	}

	return Frame{
		Data:        body,
		ContentType: "image/jpeg",
		CapturedAt:  time.Now(),
	}, nil
}

// validateJPEG rejects empty bodies and non-JPEG data (error pages,
// truncated responses) before they are accepted as captured frames.
// DecodeConfig parses only the header -- structural validation without
// the CPU cost of a full decode.
func validateJPEG(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("empty response body")
	}
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
		return fmt.Errorf("response is not a JPEG (missing SOI marker)")
	}
	if _, err := jpeg.DecodeConfig(bytes.NewReader(data)); err != nil {
		return fmt.Errorf("response is not a decodable JPEG: %w", err)
	}
	return nil
}
