package mqtt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"github.com/MakerEyeLabs/makereye/internal/config"
)

// --- fake paho client -------------------------------------------------

type fakeToken struct{}

func (fakeToken) Wait() bool                     { return true }
func (fakeToken) WaitTimeout(time.Duration) bool { return true }
func (fakeToken) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
func (fakeToken) Error() error { return nil }

type publishRecord struct {
	topic    string
	retained bool
	payload  []byte
}

type fakeClient struct {
	mu        sync.Mutex
	connected bool
	published []publishRecord
	handlers  map[string]paho.MessageHandler
}

func newFakeClient() *fakeClient {
	return &fakeClient{handlers: map[string]paho.MessageHandler{}}
}

func (c *fakeClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}
func (c *fakeClient) IsConnectionOpen() bool { return c.IsConnected() }
func (c *fakeClient) Connect() paho.Token {
	c.mu.Lock()
	c.connected = true
	c.mu.Unlock()
	return fakeToken{}
}
func (c *fakeClient) Disconnect(uint) {
	c.mu.Lock()
	c.connected = false
	c.mu.Unlock()
}
func (c *fakeClient) Publish(topic string, _ byte, retained bool, payload interface{}) paho.Token {
	var data []byte
	switch p := payload.(type) {
	case []byte:
		data = p
	case string:
		data = []byte(p)
	}
	c.mu.Lock()
	c.published = append(c.published, publishRecord{topic: topic, retained: retained, payload: data})
	c.mu.Unlock()
	return fakeToken{}
}
func (c *fakeClient) Subscribe(topic string, _ byte, callback paho.MessageHandler) paho.Token {
	c.mu.Lock()
	c.handlers[topic] = callback
	c.mu.Unlock()
	return fakeToken{}
}
func (c *fakeClient) SubscribeMultiple(map[string]byte, paho.MessageHandler) paho.Token {
	return fakeToken{}
}
func (c *fakeClient) Unsubscribe(...string) paho.Token        { return fakeToken{} }
func (c *fakeClient) AddRoute(string, paho.MessageHandler)    {}
func (c *fakeClient) OptionsReader() paho.ClientOptionsReader { return paho.ClientOptionsReader{} }

func (c *fakeClient) publishedTo(topic string) []publishRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []publishRecord
	for _, p := range c.published {
		if p.topic == topic {
			out = append(out, p)
		}
	}
	return out
}

func (c *fakeClient) handlerFor(topic string) paho.MessageHandler {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.handlers[topic]
}

type fakeMessage struct {
	topic   string
	payload []byte
}

func (m fakeMessage) Duplicate() bool   { return false }
func (m fakeMessage) Qos() byte         { return 0 }
func (m fakeMessage) Retained() bool    { return false }
func (m fakeMessage) Topic() string     { return m.topic }
func (m fakeMessage) MessageID() uint16 { return 0 }
func (m fakeMessage) Payload() []byte   { return m.payload }
func (m fakeMessage) Ack()              {}

// --- test scaffolding -------------------------------------------------

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testConfig() *config.Config {
	cfg := config.Default()
	cfg.Device.Name = "Bench Printer 1"
	cfg.MQTT.Enabled = true
	cfg.MQTT.BrokerURL = "tcp://127.0.0.1:1883"
	cfg.PrusaConnect.Enabled = true
	cfg.PrusaConnect.Token = "tok"
	cfg.PrusaConnect.Fingerprint = "at-least-16-characters"
	cfg.Lighting.Enabled = true
	cfg.Lighting.Lights = []config.LightConfig{
		{Name: "spotlight", Type: config.LightTypeWyzeSpotlight},
	}
	cfg.Timelapse.Enabled = true
	return cfg
}

type calls struct {
	mu     sync.Mutex
	events []string
}

func (c *calls) record(name string) func(context.Context) error {
	return func(context.Context) error {
		c.mu.Lock()
		c.events = append(c.events, name)
		c.mu.Unlock()
		return nil
	}
}

func (c *calls) list() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.events...)
}

// startTestBridge builds a bridge on a fake client and simulates the
// broker connect (paho would normally invoke OnConnect itself).
func startTestBridge(t *testing.T, cfg *config.Config) (*Bridge, *fakeClient, *calls) {
	t.Helper()
	fc := newFakeClient()
	c := &calls{}
	hooks := Hooks{
		StreamStart:   c.record("stream-start"),
		StreamStop:    c.record("stream-stop"),
		StreamRestart: c.record("stream-restart"),
		PrusaStart:    c.record("prusa-start"),
		PrusaStop:     c.record("prusa-stop"),
		PrusaRestart:  c.record("prusa-restart"),
		Status: func() Status {
			return Status{StreamPhase: "running", PrusaPhase: "running", PrusaUploads: 7}
		},
	}
	lightLevels := map[string]int{"spotlight": 0}
	var lightMu sync.Mutex
	hooks.LightOn = func(_ context.Context, name string) error {
		c.record("light-on-" + name)(nil)
		lightMu.Lock()
		lightLevels[name] = 255
		lightMu.Unlock()
		return nil
	}
	hooks.LightOff = func(_ context.Context, name string) error {
		c.record("light-off-" + name)(nil)
		lightMu.Lock()
		lightLevels[name] = 0
		lightMu.Unlock()
		return nil
	}
	hooks.LightSet = func(_ context.Context, name string, level int) error {
		c.record(fmt.Sprintf("light-set-%s-%d", name, level))(nil)
		lightMu.Lock()
		lightLevels[name] = level
		lightMu.Unlock()
		return nil
	}
	hooks.LightStates = func() map[string]int {
		lightMu.Lock()
		defer lightMu.Unlock()
		out := map[string]int{}
		for k, v := range lightLevels {
			out[k] = v
		}
		return out
	}

	hooks.TimelapseStart = func(_ context.Context, name string, interval, fps int) error {
		c.record(fmt.Sprintf("timelapse-start-%d-%d", interval, fps))(nil)
		return nil
	}
	hooks.TimelapseStop = func(_ context.Context) error {
		c.record("timelapse-stop")(nil)
		return nil
	}
	hooks.TimelapseRenderLast = func(_ context.Context) error {
		c.record("timelapse-render-last")(nil)
		return nil
	}

	b := NewBridge(cfg, testLogger(), hooks)
	b.newClient = func(opts *paho.ClientOptions) paho.Client { return fc }

	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { b.Stop(context.Background()) })

	// Simulate what paho does on a successful connect.
	b.onConnect(fc)
	return b, fc, c
}

// --- tests -------------------------------------------------------------

func TestOnConnectPublishesAvailabilityAndDiscovery(t *testing.T) {
	_, fc, _ := startTestBridge(t, testConfig())

	avail := fc.publishedTo("makereye/bench_printer_1/availability")
	if len(avail) == 0 || string(avail[0].payload) != "online" || !avail[0].retained {
		t.Errorf("expected retained 'online' availability publish, got %+v", avail)
	}

	wantDiscovery := []string{
		"homeassistant/switch/makereye_bench_printer_1/stream/config",
		"homeassistant/button/makereye_bench_printer_1/stream_restart/config",
		"homeassistant/sensor/makereye_bench_printer_1/stream_phase/config",
		"homeassistant/switch/makereye_bench_printer_1/prusa/config",
		"homeassistant/button/makereye_bench_printer_1/prusa_restart/config",
		"homeassistant/sensor/makereye_bench_printer_1/prusa_uploads/config",
		"homeassistant/sensor/makereye_bench_printer_1/prusa_failures/config",
	}
	for _, topic := range wantDiscovery {
		recs := fc.publishedTo(topic)
		if len(recs) == 0 {
			t.Errorf("missing discovery publish on %s", topic)
			continue
		}
		if !recs[0].retained {
			t.Errorf("discovery publish on %s should be retained", topic)
		}
		var doc map[string]any
		if err := json.Unmarshal(recs[0].payload, &doc); err != nil {
			t.Errorf("discovery payload on %s is not valid JSON: %v", topic, err)
			continue
		}
		if doc["unique_id"] == nil || doc["device"] == nil || doc["availability_topic"] == nil {
			t.Errorf("discovery payload on %s missing required fields: %v", topic, doc)
		}
	}
}

func TestOnConnectPublishesState(t *testing.T) {
	_, fc, _ := startTestBridge(t, testConfig())

	status := fc.publishedTo("makereye/bench_printer_1/status")
	if len(status) == 0 {
		t.Fatal("expected a status publish")
	}
	var st Status
	if err := json.Unmarshal(status[0].payload, &st); err != nil {
		t.Fatalf("status payload is not valid JSON: %v", err)
	}
	if st.StreamPhase != "running" || st.PrusaUploads != 7 {
		t.Errorf("status = %+v, want stream running with 7 uploads", st)
	}

	stream := fc.publishedTo("makereye/bench_printer_1/stream/state")
	if len(stream) == 0 || string(stream[0].payload) != "ON" {
		t.Errorf("stream state = %+v, want ON", stream)
	}
}

func TestSwitchCommandsDispatchToHooks(t *testing.T) {
	_, fc, c := startTestBridge(t, testConfig())

	h := fc.handlerFor("makereye/bench_printer_1/stream/set")
	if h == nil {
		t.Fatal("no handler subscribed for stream/set")
	}
	h(fc, fakeMessage{topic: "makereye/bench_printer_1/stream/set", payload: []byte("OFF")})
	h(fc, fakeMessage{topic: "makereye/bench_printer_1/stream/set", payload: []byte("on")})

	pr := fc.handlerFor("makereye/bench_printer_1/prusa/restart")
	if pr == nil {
		t.Fatal("no handler subscribed for prusa/restart")
	}
	pr(fc, fakeMessage{topic: "makereye/bench_printer_1/prusa/restart", payload: []byte("PRESS")})

	got := strings.Join(c.list(), ",")
	want := "stream-stop,stream-start,prusa-restart"
	if got != want {
		t.Errorf("hook calls = %s, want %s", got, want)
	}
}

func TestCommandRepublishesStateAsAck(t *testing.T) {
	_, fc, _ := startTestBridge(t, testConfig())
	before := len(fc.publishedTo("makereye/bench_printer_1/status"))

	h := fc.handlerFor("makereye/bench_printer_1/stream/set")
	h(fc, fakeMessage{payload: []byte("ON")})

	after := len(fc.publishedTo("makereye/bench_printer_1/status"))
	if after != before+1 {
		t.Errorf("expected one more status publish after command (ack), got %d -> %d", before, after)
	}
}

func TestPrusaEntitiesOmittedWhenDisabled(t *testing.T) {
	cfg := testConfig()
	cfg.PrusaConnect.Enabled = false
	_, fc, _ := startTestBridge(t, cfg)

	if recs := fc.publishedTo("homeassistant/switch/makereye_bench_printer_1/prusa/config"); len(recs) != 0 {
		t.Error("prusa switch discovery should not be published when prusa_connect is disabled")
	}
	if h := fc.handlerFor("makereye/bench_printer_1/prusa/set"); h != nil {
		t.Error("prusa command handler should not be subscribed when prusa_connect is disabled")
	}
}

func TestStopPublishesOfflineAndDisconnects(t *testing.T) {
	b, fc, _ := startTestBridge(t, testConfig())

	if err := b.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	avail := fc.publishedTo("makereye/bench_printer_1/availability")
	last := avail[len(avail)-1]
	if string(last.payload) != "offline" || !last.retained {
		t.Errorf("expected final retained 'offline' availability publish, got %+v", last)
	}
	if fc.IsConnected() {
		t.Error("client should be disconnected after Stop")
	}
}

func TestDoubleStartFails(t *testing.T) {
	b, _, _ := startTestBridge(t, testConfig())
	if err := b.Start(context.Background()); err == nil {
		t.Error("expected error starting an already-started bridge")
	}
}

func TestUnknownSwitchPayloadIsIgnored(t *testing.T) {
	_, fc, c := startTestBridge(t, testConfig())
	h := fc.handlerFor("makereye/bench_printer_1/stream/set")
	h(fc, fakeMessage{payload: []byte("TOGGLE")})
	if len(c.list()) != 0 {
		t.Errorf("unknown payload should not dispatch any hook, got %v", c.list())
	}
}

func TestLightEntityDiscoveryAndCommands(t *testing.T) {
	_, fc, c := startTestBridge(t, testConfig())

	// Discovery: a dimmable light entity.
	recs := fc.publishedTo("homeassistant/light/makereye_bench_printer_1/light_spotlight/config")
	if len(recs) == 0 {
		t.Fatal("missing light discovery publish")
	}
	var doc map[string]any
	if err := json.Unmarshal(recs[0].payload, &doc); err != nil {
		t.Fatalf("light discovery payload not valid JSON: %v", err)
	}
	if doc["brightness_scale"] != float64(255) || doc["brightness_command_topic"] == nil {
		t.Errorf("light discovery should declare brightness support, got %v", doc)
	}

	// Commands: ON, brightness, OFF.
	onOffHandler := fc.handlerFor("makereye/bench_printer_1/light/spotlight/set")
	if onOffHandler == nil {
		t.Fatal("no handler subscribed for light set topic")
	}
	brightnessHandler := fc.handlerFor("makereye/bench_printer_1/light/spotlight/brightness/set")
	if brightnessHandler == nil {
		t.Fatal("no handler subscribed for light brightness topic")
	}

	onOffHandler(fc, fakeMessage{payload: []byte("ON")})
	brightnessHandler(fc, fakeMessage{payload: []byte("128")})
	onOffHandler(fc, fakeMessage{payload: []byte("OFF")})

	got := strings.Join(c.list(), ",")
	want := "light-on-spotlight,light-set-spotlight-128,light-off-spotlight"
	if got != want {
		t.Errorf("light hook calls = %s, want %s", got, want)
	}

	// State: after the OFF command the ack publish should show OFF/0.
	states := fc.publishedTo("makereye/bench_printer_1/light/spotlight/state")
	if len(states) == 0 || string(states[len(states)-1].payload) != "OFF" {
		t.Errorf("last light state = %v, want OFF", states)
	}
	levels := fc.publishedTo("makereye/bench_printer_1/light/spotlight/brightness")
	if len(levels) == 0 || string(levels[len(levels)-1].payload) != "0" {
		t.Errorf("last brightness state = %v, want 0", levels)
	}
}

func TestLightEntitiesOmittedWhenDisabled(t *testing.T) {
	cfg := testConfig()
	cfg.Lighting.Enabled = false
	_, fc, _ := startTestBridge(t, cfg)

	if recs := fc.publishedTo("homeassistant/light/makereye_bench_printer_1/light_spotlight/config"); len(recs) != 0 {
		t.Error("light discovery should not be published when lighting is disabled")
	}
	if h := fc.handlerFor("makereye/bench_printer_1/light/spotlight/set"); h != nil {
		t.Error("light command handler should not be subscribed when lighting is disabled")
	}
}

func TestTimelapseEntitiesAndCommands(t *testing.T) {
	_, fc, c := startTestBridge(t, testConfig())

	// Discovery: buttons, numbers, sensors.
	for _, topic := range []string{
		"homeassistant/button/makereye_bench_printer_1/timelapse_start/config",
		"homeassistant/button/makereye_bench_printer_1/timelapse_stop/config",
		"homeassistant/button/makereye_bench_printer_1/timelapse_render_last/config",
		"homeassistant/number/makereye_bench_printer_1/timelapse_interval/config",
		"homeassistant/number/makereye_bench_printer_1/timelapse_fps/config",
		"homeassistant/sensor/makereye_bench_printer_1/timelapse_phase/config",
		"homeassistant/sensor/makereye_bench_printer_1/timelapse_frames/config",
		"homeassistant/sensor/makereye_bench_printer_1/timelapse_last_result/config",
	} {
		if recs := fc.publishedTo(topic); len(recs) == 0 {
			t.Errorf("missing discovery publish on %s", topic)
		}
	}

	// Numbers hold per-job parameters; the start button consumes them.
	intervalH := fc.handlerFor("makereye/bench_printer_1/timelapse/interval/set")
	fpsH := fc.handlerFor("makereye/bench_printer_1/timelapse/fps/set")
	startH := fc.handlerFor("makereye/bench_printer_1/timelapse/start")
	if intervalH == nil || fpsH == nil || startH == nil {
		t.Fatal("timelapse command handlers not subscribed")
	}
	intervalH(fc, fakeMessage{payload: []byte("15")})
	fpsH(fc, fakeMessage{payload: []byte("60")})
	startH(fc, fakeMessage{payload: []byte("PRESS")})

	stopH := fc.handlerFor("makereye/bench_printer_1/timelapse/stop")
	renderH := fc.handlerFor("makereye/bench_printer_1/timelapse/render_last")
	stopH(fc, fakeMessage{payload: []byte("PRESS")})
	renderH(fc, fakeMessage{payload: []byte("PRESS")})

	got := strings.Join(c.list(), ",")
	want := "timelapse-start-15-60,timelapse-stop,timelapse-render-last"
	if got != want {
		t.Errorf("timelapse hook calls = %s, want %s", got, want)
	}

	// Number state topics republished retained.
	intervals := fc.publishedTo("makereye/bench_printer_1/timelapse/interval")
	if len(intervals) == 0 {
		t.Fatal("expected interval state publish")
	}
	last := intervals[len(intervals)-1]
	if string(last.payload) != "15" || !last.retained {
		t.Errorf("interval state = %q retained=%v, want 15/true", last.payload, last.retained)
	}
}

func TestTimelapseNumberRejectsOutOfRange(t *testing.T) {
	b, fc, _ := startTestBridge(t, testConfig())
	h := fc.handlerFor("makereye/bench_printer_1/timelapse/interval/set")
	h(fc, fakeMessage{payload: []byte("99999")})
	h(fc, fakeMessage{payload: []byte("abc")})
	b.mu.Lock()
	got := b.tlInterval
	b.mu.Unlock()
	if got != 30 { // config default untouched
		t.Errorf("interval after invalid payloads = %d, want 30", got)
	}
}

func TestTimelapseEntitiesOmittedWhenDisabled(t *testing.T) {
	cfg := testConfig()
	cfg.Timelapse.Enabled = false
	_, fc, _ := startTestBridge(t, cfg)
	if recs := fc.publishedTo("homeassistant/button/makereye_bench_printer_1/timelapse_start/config"); len(recs) != 0 {
		t.Error("timelapse discovery should not be published when disabled")
	}
	if h := fc.handlerFor("makereye/bench_printer_1/timelapse/start"); h != nil {
		t.Error("timelapse handlers should not be subscribed when disabled")
	}
}

func TestCommandOutcomesPublishedToResultSensor(t *testing.T) {
	cfg := testConfig()
	fc := newFakeClient()
	c := &calls{}
	hooks := Hooks{
		StreamStart:   c.record("stream-start"),
		StreamStop:    func(context.Context) error { return fmt.Errorf("boom: stream jammed") },
		StreamRestart: c.record("stream-restart"),
		Status:        func() Status { return Status{StreamPhase: "running"} },
	}
	hooks.TimelapseStart = func(context.Context, string, int, int) error {
		return fmt.Errorf("free space too low")
	}
	hooks.TimelapseStop = func(context.Context) error { return nil }
	hooks.TimelapseRenderLast = func(context.Context) error { return nil }

	b := NewBridge(cfg, testLogger(), hooks)
	b.newClient = func(*paho.ClientOptions) paho.Client { return fc }
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { b.Stop(context.Background()) })
	b.onConnect(fc)

	resultTopic := "makereye/bench_printer_1/last_command_result"

	// Discovery for the result sensor exists.
	if recs := fc.publishedTo("homeassistant/sensor/makereye_bench_printer_1/last_command_result/config"); len(recs) == 0 {
		t.Error("missing last_command_result sensor discovery")
	}

	// A failing command publishes its error.
	h := fc.handlerFor("makereye/bench_printer_1/stream/set")
	h(fc, fakeMessage{payload: []byte("OFF")})
	results := fc.publishedTo(resultTopic)
	if len(results) == 0 || !strings.Contains(string(results[len(results)-1].payload), "stream jammed") {
		t.Errorf("expected failure published to result sensor, got %v", results)
	}
	if !results[len(results)-1].retained {
		t.Error("command result should be retained")
	}

	// A failing timelapse start publishes its error too.
	tlStart := fc.handlerFor("makereye/bench_printer_1/timelapse/start")
	tlStart(fc, fakeMessage{payload: []byte("PRESS")})
	results = fc.publishedTo(resultTopic)
	if !strings.Contains(string(results[len(results)-1].payload), "free space too low") {
		t.Errorf("expected timelapse start failure published, got %q", results[len(results)-1].payload)
	}

	// A succeeding command publishes ok.
	h(fc, fakeMessage{payload: []byte("ON")})
	results = fc.publishedTo(resultTopic)
	if !strings.Contains(string(results[len(results)-1].payload), "ok") {
		t.Errorf("expected ok result, got %q", results[len(results)-1].payload)
	}
}

func TestLastErrorSensorAndStatusField(t *testing.T) {
	cfg := testConfig()
	fc := newFakeClient()
	hooks := Hooks{
		StreamStart:   func(context.Context) error { return nil },
		StreamStop:    func(context.Context) error { return nil },
		StreamRestart: func(context.Context) error { return nil },
		Status: func() Status {
			return Status{
				StreamPhase: "running",
				LastError:   "prusa_connect: uploading to Prusa Connect: status 401",
				LastErrorAt: "2026-07-15T01:02:03Z",
			}
		},
	}
	b := NewBridge(cfg, testLogger(), hooks)
	b.newClient = func(*paho.ClientOptions) paho.Client { return fc }
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { b.Stop(context.Background()) })
	b.onConnect(fc)

	if recs := fc.publishedTo("homeassistant/sensor/makereye_bench_printer_1/last_error/config"); len(recs) == 0 {
		t.Error("missing last_error sensor discovery")
	}

	status := fc.publishedTo("makereye/bench_printer_1/status")
	if len(status) == 0 {
		t.Fatal("expected status publish")
	}
	var st Status
	if err := json.Unmarshal(status[len(status)-1].payload, &st); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.LastError, "status 401") || st.LastErrorAt == "" {
		t.Errorf("status last_error = %q at %q, want the aggregated error with timestamp", st.LastError, st.LastErrorAt)
	}
}

func TestTelemetryPublishAndDiscovery(t *testing.T) {
	cfg := testConfig()
	fc := newFakeClient()
	hooks := Hooks{
		StreamStart:   func(context.Context) error { return nil },
		StreamStop:    func(context.Context) error { return nil },
		StreamRestart: func(context.Context) error { return nil },
		Status:        func() Status { return Status{StreamPhase: "running"} },
		Telemetry: func() map[string]any {
			return map[string]any{
				"makereye_version":   "v1.2.3",
				"os_version":         "Debian 12, kernel 6.6",
				"cpu_percent":        12.3,
				"memory_percent":     40.0,
				"cpu_temp_c":         51.5,
				"disk_used_percent":  7.1,
				"disk_fullest_mount": "/",
				"uptime_seconds":     int64(3600),
			}
		},
	}
	b := NewBridge(cfg, testLogger(), hooks)
	b.newClient = func(*paho.ClientOptions) paho.Client { return fc }
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { b.Stop(context.Background()) })
	b.onConnect(fc)

	// Diagnostic sensor discovery for every metric.
	for _, key := range []string{
		"makereye_version", "os_version", "cpu_percent", "memory_percent",
		"cpu_temp_c", "disk_used_percent", "uptime_seconds",
	} {
		topic := "homeassistant/sensor/makereye_bench_printer_1/" + key + "/config"
		recs := fc.publishedTo(topic)
		if len(recs) == 0 {
			t.Errorf("missing telemetry discovery on %s", topic)
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal(recs[0].payload, &doc); err != nil {
			t.Errorf("bad discovery JSON on %s: %v", topic, err)
			continue
		}
		if doc["entity_category"] != "diagnostic" {
			t.Errorf("%s should be a diagnostic entity, got %v", key, doc["entity_category"])
		}
	}

	// Telemetry document published retained.
	recs := fc.publishedTo("makereye/bench_printer_1/telemetry")
	if len(recs) == 0 {
		t.Fatal("expected telemetry publish on connect")
	}
	if !recs[0].retained {
		t.Error("telemetry should be retained")
	}
	var doc map[string]any
	if err := json.Unmarshal(recs[0].payload, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["makereye_version"] != "v1.2.3" || doc["cpu_percent"] != 12.3 {
		t.Errorf("telemetry payload = %v", doc)
	}
}

func TestTelemetryOmittedWithoutHook(t *testing.T) {
	_, fc, _ := startTestBridge(t, testConfig()) // startTestBridge sets no Telemetry hook
	if recs := fc.publishedTo("makereye/bench_printer_1/telemetry"); len(recs) != 0 {
		t.Error("telemetry should not publish without a hook")
	}
	if recs := fc.publishedTo("homeassistant/sensor/makereye_bench_printer_1/cpu_percent/config"); len(recs) != 0 {
		t.Error("telemetry discovery should not publish without a hook")
	}
}

func TestDiscoveryPayloadsIncludeOrigin(t *testing.T) {
	_, fc, _ := startTestBridge(t, testConfig())
	recs := fc.publishedTo("homeassistant/switch/makereye_bench_printer_1/stream/config")
	if len(recs) == 0 {
		t.Fatal("missing stream switch discovery publish")
	}
	var doc map[string]any
	if err := json.Unmarshal(recs[0].payload, &doc); err != nil {
		t.Fatalf("discovery payload not valid JSON: %v", err)
	}
	origin, ok := doc["origin"].(map[string]any)
	if !ok || origin["name"] != "MakerEye" {
		t.Errorf("discovery payload should include origin with name MakerEye, got %v", doc["origin"])
	}
}

func TestHABirthMessageRepublishesDiscoveryAndState(t *testing.T) {
	_, fc, _ := startTestBridge(t, testConfig())

	h := fc.handlerFor("homeassistant/status")
	if h == nil {
		t.Fatal("no handler subscribed for the HA birth topic homeassistant/status")
	}

	discoveryTopic := "homeassistant/switch/makereye_bench_printer_1/stream/config"
	discoveryBefore := len(fc.publishedTo(discoveryTopic))
	stateBefore := len(fc.publishedTo("makereye/bench_printer_1/status"))

	h(fc, fakeMessage{topic: "homeassistant/status", payload: []byte("online")})

	if got := len(fc.publishedTo(discoveryTopic)); got != discoveryBefore+1 {
		t.Errorf("expected discovery republish on HA birth, got %d -> %d", discoveryBefore, got)
	}
	if got := len(fc.publishedTo("makereye/bench_printer_1/status")); got != stateBefore+1 {
		t.Errorf("expected state republish on HA birth, got %d -> %d", stateBefore, got)
	}

	// HA's will message ("offline" on the same topic) must not trigger
	// a republish storm.
	h(fc, fakeMessage{topic: "homeassistant/status", payload: []byte("offline")})
	if got := len(fc.publishedTo(discoveryTopic)); got != discoveryBefore+1 {
		t.Errorf("HA 'offline' status should not republish discovery, got %d publishes", got)
	}
}
