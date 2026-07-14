// Package lighting implements MakerEye's optional lighting subsystem:
// named lights with pluggable backends, controllable from the CLI (via
// the control socket) and, with MQTT enabled, as dimmable Home
// Assistant light entities.
//
// Like the Prusa uploader and the MQTT bridge, this is advisory and
// in-process: a light that is missing or unplugged is logged, never
// affecting camera streaming. The first backend is the Wyze Cam v3
// Spotlight Kit (see wyze.go); the framework exists so GPIO-driven
// lights and addressable LED strips can be added as types later
// without config churn.
package lighting

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/MakerEyeLabs/makereye/internal/config"
)

// Backend drives one physical light. Implementations must be safe for
// repeated calls and tolerate the device disappearing between calls
// (unplugged USB, etc.) by returning an error rather than wedging.
type Backend interface {
	// SetBrightness sets the light to level 0 (off) through 255 (max).
	SetBrightness(level int) error
}

// light pairs a backend with the last commanded state. Brightness is
// tracked as intent: the Wyze spotlight (and likely most cheap light
// hardware) is write-only, so MakerEye's view of "current brightness"
// is the last level it successfully commanded.
type light struct {
	backend    Backend
	brightness int
	// lastOn is the most recent non-zero brightness, restored by On.
	lastOn int
}

// Manager owns all configured lights.
type Manager struct {
	cfg    *config.Config
	logger *slog.Logger

	// newBackend builds a backend for one light config; a field so
	// tests can substitute fakes without hardware.
	newBackend func(config.LightConfig) (Backend, error)

	mu     sync.Mutex
	lights map[string]*light
}

// NewManager creates a Manager for cfg's lighting section. It does not
// touch any hardware; call Start.
func NewManager(cfg *config.Config, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		cfg:        cfg,
		logger:     logger,
		newBackend: defaultBackend,
	}
}

func defaultBackend(lc config.LightConfig) (Backend, error) {
	switch lc.Type {
	case config.LightTypeWyzeSpotlight:
		return newWyzeSpotlight(lc.Device), nil
	default:
		return nil, fmt.Errorf("unsupported light type %q", lc.Type)
	}
}

// Start builds backends for every configured light and applies each
// light's startup brightness so the (write-only) hardware is in a known
// state. Backend construction errors fail Start; startup-brightness
// write failures are logged and tolerated, matching the advisory model
// (the light may simply not be plugged in yet).
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.lights != nil {
		m.mu.Unlock()
		return fmt.Errorf("lighting manager is already started")
	}
	lights := make(map[string]*light, len(m.cfg.Lighting.Lights))
	m.lights = lights
	m.mu.Unlock()

	for _, lc := range m.cfg.Lighting.Lights {
		backend, err := m.newBackend(lc)
		if err != nil {
			return fmt.Errorf("light %q: %w", lc.Name, err)
		}
		l := &light{backend: backend, brightness: 0, lastOn: 255}

		if err := backend.SetBrightness(lc.StartupBrightness); err != nil {
			m.logger.Warn("applying startup brightness failed (is the light connected?)",
				"light", lc.Name, "error", err)
		} else {
			l.brightness = lc.StartupBrightness
			if lc.StartupBrightness > 0 {
				l.lastOn = lc.StartupBrightness
			}
		}

		m.mu.Lock()
		lights[lc.Name] = l
		m.mu.Unlock()
	}
	return nil
}

// Stop releases the manager. Lights are deliberately left in their
// current state: turning everything off on daemon shutdown would make
// every service restart (including updates) flap the room lighting.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	m.lights = nil
	m.mu.Unlock()
	return nil
}

func (m *Manager) get(name string) (*light, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lights == nil {
		return nil, fmt.Errorf("lighting is not running")
	}
	l, ok := m.lights[name]
	if !ok {
		names := make([]string, 0, len(m.lights))
		for n := range m.lights {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("unknown light %q (configured lights: %v)", name, names)
	}
	return l, nil
}

// Set commands the named light to level 0-255.
func (m *Manager) Set(name string, level int) error {
	if level < 0 || level > 255 {
		return fmt.Errorf("brightness must be 0-255, got %d", level)
	}
	l, err := m.get(name)
	if err != nil {
		return err
	}
	if err := l.backend.SetBrightness(level); err != nil {
		return fmt.Errorf("setting light %q: %w", name, err)
	}
	m.mu.Lock()
	l.brightness = level
	if level > 0 {
		l.lastOn = level
	}
	m.mu.Unlock()
	return nil
}

// On turns the named light on, restoring its last non-zero brightness
// (255 if it has never been on).
func (m *Manager) On(name string) error {
	l, err := m.get(name)
	if err != nil {
		return err
	}
	m.mu.Lock()
	level := l.lastOn
	m.mu.Unlock()
	return m.Set(name, level)
}

// Off turns the named light off, preserving its last-on brightness for
// a later On.
func (m *Manager) Off(name string) error {
	return m.Set(name, 0)
}

// States returns the last commanded brightness per light name.
func (m *Manager) States() map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]int, len(m.lights))
	for name, l := range m.lights {
		out[name] = l.brightness
	}
	return out
}
