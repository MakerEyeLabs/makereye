package snapshot

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MakerEyeLabs/makereye/internal/config"
)

// makeJPEG returns a minimal valid JPEG for tests.
func makeJPEG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sourceFor(t *testing.T, srv *httptest.Server) *Go2rtcSource {
	t.Helper()
	cfg := config.Default()
	cfg.Go2rtc.HTTPListen = strings.TrimPrefix(srv.URL, "http://")
	return NewGo2rtcSource(cfg)
}

func TestCaptureSuccess(t *testing.T) {
	want := makeJPEG(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/frame.jpeg" || r.URL.Query().Get("src") != "camera" {
			t.Errorf("unexpected request: %s", r.URL)
		}
		_, _ = w.Write(want)
	}))
	defer srv.Close()

	before := time.Now()
	frame, err := sourceFor(t, srv).Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if !bytes.Equal(frame.Data, want) {
		t.Error("frame data does not match served JPEG")
	}
	if frame.ContentType != "image/jpeg" {
		t.Errorf("content type = %q", frame.ContentType)
	}
	if frame.CapturedAt.Before(before) {
		t.Error("CapturedAt should be set")
	}
}

func TestCaptureSendsBasicAuth(t *testing.T) {
	var user, pass string
	var ok bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok = r.BasicAuth()
		_, _ = w.Write(makeJPEG(t))
	}))
	defer srv.Close()

	src := sourceFor(t, srv)
	src.cfg.Go2rtc.Auth.Username = "admin"
	src.cfg.Go2rtc.Auth.Password = "hunter2"

	if _, err := src.Capture(context.Background()); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if !ok || user != "admin" || pass != "hunter2" {
		t.Errorf("auth = (%q, %q, %v), want (admin, hunter2, true)", user, pass, ok)
	}
}

func TestCaptureRejectsNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := sourceFor(t, srv).Capture(context.Background()); err == nil {
		t.Error("expected error for 500 response")
	}
}

func TestCaptureRejectsEmptyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if _, err := sourceFor(t, srv).Capture(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "empty") {
		t.Errorf("expected empty-body error, got %v", err)
	}
}

func TestCaptureRejectsNonJPEG(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>not a jpeg</html>"))
	}))
	defer srv.Close()

	if _, err := sourceFor(t, srv).Capture(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "JPEG") {
		t.Errorf("expected non-JPEG error, got %v", err)
	}
}

func TestCaptureRejectsTruncatedJPEG(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// SOI marker but nothing decodable after it.
		_, _ = w.Write([]byte{0xFF, 0xD8, 0x00, 0x01})
	}))
	defer srv.Close()

	if _, err := sourceFor(t, srv).Capture(context.Background()); err == nil {
		t.Error("expected error for truncated JPEG")
	}
}

func TestCaptureHonorsContextTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := sourceFor(t, srv).Capture(ctx); err == nil {
		t.Error("expected timeout error")
	}
}

func TestCaptureUnreachableServer(t *testing.T) {
	cfg := config.Default()
	cfg.Go2rtc.HTTPListen = "127.0.0.1:1" // nothing listens here
	// Callers always bound Capture with a timeout; do the same here so
	// environments that drop (rather than refuse) the connection don't
	// stall the test.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := NewGo2rtcSource(cfg).Capture(ctx); err == nil {
		t.Error("expected connection error")
	}
}
