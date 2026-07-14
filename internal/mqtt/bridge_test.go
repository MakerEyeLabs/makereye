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
