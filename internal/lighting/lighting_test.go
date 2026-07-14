package lighting

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/MakerEyeLabs/makereye/internal/config"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeBackend records levels and can be made to fail.
type fakeBackend struct {
	mu     sync.Mutex
	levels []int
	fail   bool
}

func (f *fakeBackend) SetBrightness(level int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return fmt.Errorf("device unplugged")
	}
	f.levels = append(f.levels, level)
	return nil
}

func (f *fakeBackend) recorded() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.levels...)
}

func testConfig(lights ...config.LightConfig) *config.Config {
	cfg := config.Default()
	cfg.Lighting.Enabled = true
	cfg.Lighting.Lights = lights
	return cfg
}

func startManager(t *testing.T, cfg *config.Config) (*Manager, map[string]*fakeBackend) {
	t.Helper()
	backends := map[string]*fakeBackend{}
	m := NewManager(cfg, testLogger())
	m.newBackend = func(lc config.LightConfig) (Backend, error) {
		fb := &fakeBackend{}
		backends[lc.Name] = fb
		return fb, nil
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { m.Stop(context.Background()) })
	return m, backends
}

func TestStartAppliesStartupBrightness(t *testing.T) {
	cfg := testConfig(
		config.LightConfig{Name: "spot", Type: config.LightTypeWyzeSpotlight, StartupBrightness: 100},
		config.LightConfig{Name: "shelf", Type: config.LightTypeWyzeSpotlight, StartupBrightness: 0},
	)
	m, backends := startManager(t, cfg)

	if got := backends["spot"].recorded(); len(got) != 1 || got[0] != 100 {
		t.Errorf("spot startup levels = %v, want [100]", got)
	}
	if got := backends["shelf"].recorded(); len(got) != 1 || got[0] != 0 {
		t.Errorf("shelf startup levels = %v, want [0]", got)
	}
	states := m.States()
	if states["spot"] != 100 || states["shelf"] != 0 {
		t.Errorf("states = %v, want spot=100 shelf=0", states)
	}
}

func TestSetOnOffAndLastOnRestore(t *testing.T) {
	cfg := testConfig(config.LightConfig{Name: "spot", Type: config.LightTypeWyzeSpotlight})
	m, backends := startManager(t, cfg)

	if err := m.Set("spot", 180); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := m.Off("spot"); err != nil {
		t.Fatalf("Off: %v", err)
	}
	if got := m.States()["spot"]; got != 0 {
		t.Errorf("brightness after Off = %d, want 0", got)
	}
	if err := m.On("spot"); err != nil {
		t.Fatalf("On: %v", err)
	}
	if got := m.States()["spot"]; got != 180 {
		t.Errorf("On should restore last non-zero brightness, got %d, want 180", got)
	}

	want := []int{0, 180, 0, 180} // startup, set, off, on
	if got := backends["spot"].recorded(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("backend levels = %v, want %v", got, want)
	}
}

func TestOnDefaultsToMaxWhenNeverOn(t *testing.T) {
	cfg := testConfig(config.LightConfig{Name: "spot", Type: config.LightTypeWyzeSpotlight})
	m, _ := startManager(t, cfg)

	if err := m.On("spot"); err != nil {
		t.Fatalf("On: %v", err)
	}
	if got := m.States()["spot"]; got != 255 {
		t.Errorf("On with no history = %d, want 255", got)
	}
}

func TestSetUnknownLightErrors(t *testing.T) {
	cfg := testConfig(config.LightConfig{Name: "spot", Type: config.LightTypeWyzeSpotlight})
	m, _ := startManager(t, cfg)

	if err := m.Set("nope", 10); err == nil {
		t.Error("expected error for unknown light")
	}
}

func TestSetOutOfRangeErrors(t *testing.T) {
	cfg := testConfig(config.LightConfig{Name: "spot", Type: config.LightTypeWyzeSpotlight})
	m, _ := startManager(t, cfg)

	for _, level := range []int{-1, 256} {
		if err := m.Set("spot", level); err == nil {
			t.Errorf("expected error for level %d", level)
		}
	}
}

func TestBackendFailureDoesNotUpdateState(t *testing.T) {
	cfg := testConfig(config.LightConfig{Name: "spot", Type: config.LightTypeWyzeSpotlight})
	m, backends := startManager(t, cfg)

	if err := m.Set("spot", 50); err != nil {
		t.Fatalf("Set: %v", err)
	}
	backends["spot"].fail = true
	if err := m.Set("spot", 200); err == nil {
		t.Fatal("expected error when backend fails")
	}
	if got := m.States()["spot"]; got != 50 {
		t.Errorf("failed Set should not change tracked brightness, got %d, want 50", got)
	}
}

func TestStartupBrightnessFailureIsTolerated(t *testing.T) {
	cfg := testConfig(config.LightConfig{Name: "spot", Type: config.LightTypeWyzeSpotlight, StartupBrightness: 128})
	m := NewManager(cfg, testLogger())
	fb := &fakeBackend{fail: true}
	m.newBackend = func(config.LightConfig) (Backend, error) { return fb, nil }

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start should tolerate a startup write failure (light unplugged), got: %v", err)
	}
	defer m.Stop(context.Background())

	// The light stays usable: once the "device" comes back, Set works.
	fb.fail = false
	if err := m.Set("spot", 10); err != nil {
		t.Errorf("Set after device recovery: %v", err)
	}
}

// --- Wyze backend ------------------------------------------------------

func TestWyzeFrameMatchesKnownGoodFrames(t *testing.T) {
	known := map[int]string{
		255: "aa55430516ff070263",
		51:  "aa5543051633070197",
		0:   "aa5543051600070164",
		128: "aa55430516800701e4",
	}
	for level, want := range known {
		if got := fmt.Sprintf("%x", wyzeFrame(level)); got != want {
			t.Errorf("wyzeFrame(%d) = %s, want %s", level, got, want)
		}
	}
}

func TestWyzeSetBrightnessWritesFrame(t *testing.T) {
	dir := t.TempDir()
	dev := filepath.Join(dir, "fake-tty")
	if err := os.WriteFile(dev, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	w := newWyzeSpotlight(dev)
	if err := w.SetBrightness(51); err != nil {
		t.Fatalf("SetBrightness: %v", err)
	}

	data, err := os.ReadFile(dev)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", data); got != "aa5543051633070197" {
		t.Errorf("written frame = %s, want aa5543051633070197", got)
	}
}

func TestWyzeMissingDeviceErrors(t *testing.T) {
	w := newWyzeSpotlight(filepath.Join(t.TempDir(), "does-not-exist"))
	if err := w.SetBrightness(10); err == nil {
		t.Error("expected error for missing device")
	}
}

func TestWyzeDefaultDevice(t *testing.T) {
	if w := newWyzeSpotlight(""); w.device != "/dev/ttyUSB0" {
		t.Errorf("default device = %s, want /dev/ttyUSB0", w.device)
	}
}
