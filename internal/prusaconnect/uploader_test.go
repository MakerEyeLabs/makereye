package prusaconnect

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

// testJPEGOnce builds one minimal valid JPEG shared by all tests.
var testJPEGOnce = sync.OnceValue(func() []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
		panic(err)
	}
	return buf.Bytes()
})

func testJPEG() []byte { return testJPEGOnce() }

func testConfig() *config.Config {
	cfg := config.Default()
	cfg.Stream.Name = "camera"
	cfg.PrusaConnect.Enabled = true
	cfg.PrusaConnect.Token = "test-token"
	cfg.PrusaConnect.Fingerprint = "test-fingerprint-16"
	cfg.PrusaConnect.IntervalSeconds = 1
	return cfg
}

// newTestUploader wires cfg.Go2rtc.HTTPListen at go2rtcSrv and
// prusaURL at prusaSrv, both httptest servers, so no real network calls
// are ever made in tests.
func newTestUploader(cfg *config.Config, go2rtcSrv, prusaSrv *httptest.Server) *Uploader {
	cfg.Go2rtc.HTTPListen = strings.TrimPrefix(go2rtcSrv.URL, "http://")
	u := NewUploader(cfg, snapshot.NewGo2rtcSource(cfg), testLogger())
	u.prusaURL = prusaSrv.URL
	return u
}

func TestUploaderUploadsSuccessfully(t *testing.T) {
	fakeJPEG := testJPEG()

	go2rtcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/frame.jpeg" || r.URL.Query().Get("src") != "camera" {
			t.Errorf("unexpected go2rtc request: %s %s", r.Method, r.URL)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(fakeJPEG)
	}))
	defer go2rtcSrv.Close()

	var gotToken, gotFingerprint, gotContentType string
	var gotBody []byte
	prusaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		gotToken = r.Header.Get("token")
		gotFingerprint = r.Header.Get("fingerprint")
		gotContentType = r.Header.Get("content-type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer prusaSrv.Close()

	cfg := testConfig()
	u := newTestUploader(cfg, go2rtcSrv, prusaSrv)

	ctx := context.Background()
	if err := u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer u.Stop(context.Background())

	waitForUploadCount(t, u, 1, 4*time.Second)

	if gotToken != cfg.PrusaConnect.Token {
		t.Errorf("token header = %q, want %q", gotToken, cfg.PrusaConnect.Token)
	}
	if gotFingerprint != cfg.PrusaConnect.Fingerprint {
		t.Errorf("fingerprint header = %q, want %q", gotFingerprint, cfg.PrusaConnect.Fingerprint)
	}
	if gotContentType != "image/jpg" {
		t.Errorf("content-type header = %q, want image/jpg", gotContentType)
	}
	if !bytes.Equal(gotBody, fakeJPEG) {
		t.Errorf("uploaded body = %q, want %q", gotBody, fakeJPEG)
	}

	st := u.Status()
	if st.FailureCount != 0 {
		t.Errorf("failure count = %d, want 0", st.FailureCount)
	}
	if st.LastError != "" {
		t.Errorf("last error = %q, want empty", st.LastError)
	}
}

func TestUploaderFetchesViaLoopbackWhenListenIsWildcard(t *testing.T) {
	go2rtcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(testJPEG())
	}))
	defer go2rtcSrv.Close()

	prusaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer prusaSrv.Close()

	cfg := testConfig()
	u := newTestUploader(cfg, go2rtcSrv, prusaSrv)
	// Simulate a LAN-exposed listen address: the httptest server is
	// bound on 127.0.0.1:<port>, but the config says 0.0.0.0:<port> --
	// the uploader must dial loopback, not the wildcard address.
	_, port, _ := strings.Cut(strings.TrimPrefix(go2rtcSrv.URL, "http://"), ":")
	cfg.Go2rtc.HTTPListen = "0.0.0.0:" + port

	ctx := context.Background()
	if err := u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer u.Stop(context.Background())

	waitForUploadCount(t, u, 1, 4*time.Second)
	if st := u.Status(); st.FailureCount != 0 {
		t.Errorf("failures = %d (last: %s), want 0", st.FailureCount, st.LastError)
	}
}

func TestUploaderSendsGo2rtcBasicAuthWhenConfigured(t *testing.T) {
	var gotUser, gotPass string
	var gotOK bool
	go2rtcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, gotOK = r.BasicAuth()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(testJPEG())
	}))
	defer go2rtcSrv.Close()

	prusaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer prusaSrv.Close()

	cfg := testConfig()
	cfg.Go2rtc.Auth.Username = "admin"
	cfg.Go2rtc.Auth.Password = "hunter2"
	u := newTestUploader(cfg, go2rtcSrv, prusaSrv)

	ctx := context.Background()
	if err := u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer u.Stop(context.Background())

	waitForUploadCount(t, u, 1, 4*time.Second)

	if !gotOK || gotUser != "admin" || gotPass != "hunter2" {
		t.Errorf("go2rtc request auth = (%q, %q, ok=%v), want (admin, hunter2, true)", gotUser, gotPass, gotOK)
	}
}

func TestUploaderRecordsFailureOnGo2rtcError(t *testing.T) {
	go2rtcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer go2rtcSrv.Close()

	prusaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Prusa Connect should not be contacted when the go2rtc fetch fails")
		w.WriteHeader(http.StatusOK)
	}))
	defer prusaSrv.Close()

	cfg := testConfig()
	u := newTestUploader(cfg, go2rtcSrv, prusaSrv)

	ctx := context.Background()
	if err := u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer u.Stop(context.Background())

	waitForFailureCount(t, u, 1, 4*time.Second)

	st := u.Status()
	if st.Phase != PhaseRunning {
		t.Errorf("phase = %s, want %s (a single upload failure should not stop the loop)", st.Phase, PhaseRunning)
	}
	if st.LastError == "" {
		t.Error("expected LastError to be set")
	}
}

func TestUploaderRecordsFailureOnPrusaNon2xx(t *testing.T) {
	go2rtcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(testJPEG())
	}))
	defer go2rtcSrv.Close()

	prusaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer prusaSrv.Close()

	cfg := testConfig()
	u := newTestUploader(cfg, go2rtcSrv, prusaSrv)

	ctx := context.Background()
	if err := u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer u.Stop(context.Background())

	waitForFailureCount(t, u, 1, 4*time.Second)

	if !strings.Contains(u.Status().LastError, "401") {
		t.Errorf("LastError = %q, want it to mention status 401", u.Status().LastError)
	}
}

func TestUploaderDoubleStartFails(t *testing.T) {
	go2rtcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(testJPEG())
	}))
	defer go2rtcSrv.Close()
	prusaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer prusaSrv.Close()

	cfg := testConfig()
	u := newTestUploader(cfg, go2rtcSrv, prusaSrv)

	ctx := context.Background()
	if err := u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer u.Stop(context.Background())

	if err := u.Start(ctx); err == nil {
		t.Error("expected error starting an already-running uploader")
	}
}

func TestUploaderStop(t *testing.T) {
	go2rtcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(testJPEG())
	}))
	defer go2rtcSrv.Close()
	prusaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer prusaSrv.Close()

	cfg := testConfig()
	u := newTestUploader(cfg, go2rtcSrv, prusaSrv)

	ctx := context.Background()
	if err := u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForUploadCount(t, u, 1, 4*time.Second)

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := u.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := u.Status().Phase; got != PhaseStopped {
		t.Errorf("phase after Stop = %s, want %s", got, PhaseStopped)
	}
}

func waitForUploadCount(t *testing.T, u *Uploader, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if st := u.Status(); st.UploadCount >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("upload count did not reach %d within %s, got %+v", want, timeout, u.Status())
}

func waitForFailureCount(t *testing.T, u *Uploader, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if st := u.Status(); st.FailureCount >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("failure count did not reach %d within %s, got %+v", want, timeout, u.Status())
}
