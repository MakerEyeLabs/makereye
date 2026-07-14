// Package mqtt implements MakerEye's optional MQTT + Home Assistant
// integration: availability (LWT), state/telemetry topics, command
// topics mirroring the control-socket commands, and Home Assistant
// MQTT discovery so entities appear in HA automatically.
//
// This is an advisory subsystem in the same sense as the Prusa Connect
// uploader: it is an in-process goroutine (not a supervised child
// process), it MUST NOT be required for core operation, and a broker
// that is down or unreachable is logged and retried in the background,
// never blocking daemon startup or affecting streaming.
package mqtt

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"github.com/MakerEyeLabs/makereye/internal/config"
	"github.com/MakerEyeLabs/makereye/internal/version"
)

// commandTimeout bounds how long an MQTT-initiated command (stream
// stop, prusa restart, ...) may take before it is abandoned and the
// error published/logged.
const commandTimeout = 15 * time.Second

// stateRefreshInterval is how often current state is republished even
// without a command, so HA converges after missed messages.
const stateRefreshInterval = 30 * time.Second

// Status is the state snapshot the bridge publishes. The daemon
// supplies it via Hooks.Status so this package doesn't depend on the
// go2rtc/prusaconnect packages directly.
type Status struct {
	StreamPhase    string `json:"stream_phase"`
	StreamRestarts int    `json:"stream_restarts"`
	PrusaPhase     string `json:"prusa_phase,omitempty"`
	PrusaUploads   int    `json:"prusa_uploads"`
	PrusaFailures  int    `json:"prusa_failures"`
}

// Hooks are the daemon operations the bridge dispatches MQTT commands
// to. They mirror the control-socket commands; the daemon wires them to
// the same internals. Prusa hooks may be nil when Prusa Connect is
// disabled, in which case no Prusa entities are published.
type Hooks struct {
	StreamStart   func(ctx context.Context) error
	StreamStop    func(ctx context.Context) error
	StreamRestart func(ctx context.Context) error
	PrusaStart    func(ctx context.Context) error
	PrusaStop     func(ctx context.Context) error
	PrusaRestart  func(ctx context.Context) error
	Status        func() Status
}

// Bridge owns the MQTT client and the Home Assistant integration for
// one MakerEye device.
type Bridge struct {
	cfg    *config.Config
	logger *slog.Logger
	hooks  Hooks

	// newClient is the paho client factory; a field so tests can
	// substitute a fake client without a broker.
	newClient func(*paho.ClientOptions) paho.Client

	mu      sync.Mutex
	client  paho.Client
	started bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// NewBridge creates a Bridge. It does not connect; call Start.
func NewBridge(cfg *config.Config, logger *slog.Logger, hooks Hooks) *Bridge {
	if logger == nil {
		logger = slog.Default()
	}
	return &Bridge{
		cfg:       cfg,
		logger:    logger,
		hooks:     hooks,
		newClient: paho.NewClient,
	}
}

// slug converts a device name into a topic/unique_id-safe identifier.
func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

func (b *Bridge) deviceSlug() string { return slug(b.cfg.Device.Name) }

// baseTopic is the root of this device's own topics,
// "<topic_prefix>/<device-slug>".
func (b *Bridge) baseTopic() string {
	return b.cfg.MQTT.TopicPrefix + "/" + b.deviceSlug()
}

func (b *Bridge) availabilityTopic() string { return b.baseTopic() + "/availability" }
func (b *Bridge) statusTopic() string       { return b.baseTopic() + "/status" }

// Start connects to the broker in the background and returns
// immediately. A broker that is down is not an error: paho keeps
// retrying, and the OnConnect handler (re)publishes discovery, state,
// and subscriptions on every (re)connect.
func (b *Bridge) Start(ctx context.Context) error {
	b.mu.Lock()
	if b.started {
		b.mu.Unlock()
		return fmt.Errorf("mqtt bridge is already running")
	}
	b.started = true
	b.mu.Unlock()

	opts := paho.NewClientOptions().
		AddBroker(b.cfg.MQTT.BrokerURL).
		SetClientID("makereye-"+b.deviceSlug()).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5*time.Second).
		SetWill(b.availabilityTopic(), "offline", 0, true).
		SetOnConnectHandler(b.onConnect).
		SetConnectionLostHandler(func(_ paho.Client, err error) {
			b.logger.Warn("mqtt connection lost, retrying in background", "error", err)
		})
	if b.cfg.MQTT.Username != "" {
		opts.SetUsername(b.cfg.MQTT.Username)
		opts.SetPassword(b.cfg.MQTT.Password)
	}

	client := b.newClient(opts)

	b.mu.Lock()
	b.client = client
	b.mu.Unlock()

	// Don't block startup on the broker: check briefly so obvious
	// misconfiguration is logged immediately, then let the retry loop
	// own it.
	token := client.Connect()
	if !token.WaitTimeout(2*time.Second) || token.Error() != nil {
		b.logger.Warn("mqtt broker not reachable yet, retrying in background",
			"broker", b.cfg.MQTT.BrokerURL, "error", token.Error())
	}

	runCtx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	b.wg.Add(1)
	go b.refreshLoop(runCtx)

	return nil
}

// Stop publishes offline availability (best effort) and disconnects.
func (b *Bridge) Stop(ctx context.Context) error {
	b.mu.Lock()
	if !b.started {
		b.mu.Unlock()
		return nil
	}
	b.started = false
	client := b.client
	cancel := b.cancel
	b.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	b.wg.Wait()

	if client != nil {
		if client.IsConnected() {
			client.Publish(b.availabilityTopic(), 0, true, "offline").WaitTimeout(time.Second)
		}
		client.Disconnect(250)
	}
	return nil
}

// Connected reports whether the broker connection is currently up.
// IsConnectionOpen (not IsConnected) is deliberate: paho's IsConnected
// also returns true while a reconnect is merely pending, which would
// make status report "connected" against a down broker.
func (b *Bridge) Connected() bool {
	b.mu.Lock()
	client := b.client
	b.mu.Unlock()
	return client != nil && client.IsConnectionOpen()
}

func (b *Bridge) refreshLoop(ctx context.Context) {
	defer b.wg.Done()
	ticker := time.NewTicker(stateRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if b.Connected() {
				b.publishState()
			}
		}
	}
}

// onConnect runs on every connect and reconnect: availability,
// discovery configs (retained, so HA restarts pick them up too),
// current state, and command subscriptions.
func (b *Bridge) onConnect(client paho.Client) {
	b.logger.Info("mqtt connected", "broker", b.cfg.MQTT.BrokerURL)

	client.Publish(b.availabilityTopic(), 0, true, "online")

	for topic, payload := range b.discoveryConfigs() {
		client.Publish(topic, 0, true, payload)
	}

	base := b.baseTopic()
	client.Subscribe(base+"/stream/set", 1, b.commandHandler("stream", b.hooks.StreamStart, b.hooks.StreamStop))
	client.Subscribe(base+"/stream/restart", 1, b.pressHandler("stream restart", b.hooks.StreamRestart))
	if b.prusaEnabled() {
		client.Subscribe(base+"/prusa/set", 1, b.commandHandler("prusa", b.hooks.PrusaStart, b.hooks.PrusaStop))
		client.Subscribe(base+"/prusa/restart", 1, b.pressHandler("prusa restart", b.hooks.PrusaRestart))
	}

	b.publishState()
}

func (b *Bridge) prusaEnabled() bool {
	return b.cfg.PrusaConnect.Enabled && b.hooks.PrusaStart != nil
}

// commandHandler dispatches an ON/OFF switch command to the start/stop
// hooks, then republishes state as the acknowledgement.
func (b *Bridge) commandHandler(name string, start, stop func(context.Context) error) paho.MessageHandler {
	return func(_ paho.Client, msg paho.Message) {
		payload := strings.ToUpper(strings.TrimSpace(string(msg.Payload())))
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()

		var err error
		switch payload {
		case "ON":
			err = start(ctx)
		case "OFF":
			err = stop(ctx)
		default:
			b.logger.Warn("mqtt: unknown switch payload", "entity", name, "payload", payload)
			return
		}
		if err != nil {
			b.logger.Warn("mqtt command failed", "entity", name, "payload", payload, "error", err)
		}
		b.publishState()
	}
}

// pressHandler dispatches a button press to hook, then republishes
// state.
func (b *Bridge) pressHandler(name string, hook func(context.Context) error) paho.MessageHandler {
	return func(_ paho.Client, _ paho.Message) {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		if err := hook(ctx); err != nil {
			b.logger.Warn("mqtt command failed", "entity", name, "error", err)
		}
		b.publishState()
	}
}

// publishState publishes the JSON status document (for sensors) and
// the per-switch ON/OFF state topics.
func (b *Bridge) publishState() {
	b.mu.Lock()
	client := b.client
	b.mu.Unlock()
	if client == nil {
		return
	}

	st := b.hooks.Status()
	data, err := json.Marshal(st)
	if err != nil {
		b.logger.Error("mqtt: marshaling status", "error", err)
		return
	}
	base := b.baseTopic()
	client.Publish(b.statusTopic(), 0, false, data)
	client.Publish(base+"/stream/state", 0, false, onOff(st.StreamPhase))
	if b.prusaEnabled() {
		client.Publish(base+"/prusa/state", 0, false, onOff(st.PrusaPhase))
	}
}

// onOff maps a subsystem phase to a HA switch state. "restarting"
// counts as ON (the stream is meant to be up); "failed" and "stopped"
// count as OFF.
func onOff(phase string) string {
	switch phase {
	case "running", "restarting":
		return "ON"
	default:
		return "OFF"
	}
}

// discoveryConfigs returns Home Assistant MQTT discovery topic →
// retained JSON payload for every entity this device exposes.
func (b *Bridge) discoveryConfigs() map[string][]byte {
	base := b.baseTopic()
	node := "makereye_" + b.deviceSlug()

	device := map[string]any{
		"identifiers":  []string{node},
		"name":         b.cfg.Device.Name,
		"manufacturer": "MakerEye",
		"model":        "MakerEye camera appliance",
		"sw_version":   version.String(),
	}
	common := func(name, uniqueSuffix string) map[string]any {
		return map[string]any{
			"name":               name,
			"unique_id":          node + "_" + uniqueSuffix,
			"availability_topic": b.availabilityTopic(),
			"device":             device,
		}
	}
	merge := func(m map[string]any, extra map[string]any) map[string]any {
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	configs := map[string]map[string]any{
		fmt.Sprintf("%s/switch/%s/stream/config", b.cfg.MQTT.DiscoveryPrefix, node): merge(
			common("Stream", "stream"), map[string]any{
				"state_topic":   base + "/stream/state",
				"command_topic": base + "/stream/set",
			}),
		fmt.Sprintf("%s/button/%s/stream_restart/config", b.cfg.MQTT.DiscoveryPrefix, node): merge(
			common("Restart stream", "stream_restart"), map[string]any{
				"command_topic": base + "/stream/restart",
				"payload_press": "PRESS",
			}),
		fmt.Sprintf("%s/sensor/%s/stream_phase/config", b.cfg.MQTT.DiscoveryPrefix, node): merge(
			common("Stream phase", "stream_phase"), map[string]any{
				"state_topic":    b.statusTopic(),
				"value_template": "{{ value_json.stream_phase }}",
			}),
	}
	if b.prusaEnabled() {
		configs[fmt.Sprintf("%s/switch/%s/prusa/config", b.cfg.MQTT.DiscoveryPrefix, node)] = merge(
			common("Prusa Connect uploads", "prusa"), map[string]any{
				"state_topic":   base + "/prusa/state",
				"command_topic": base + "/prusa/set",
			})
		configs[fmt.Sprintf("%s/button/%s/prusa_restart/config", b.cfg.MQTT.DiscoveryPrefix, node)] = merge(
			common("Restart Prusa Connect uploads", "prusa_restart"), map[string]any{
				"command_topic": base + "/prusa/restart",
				"payload_press": "PRESS",
			})
		configs[fmt.Sprintf("%s/sensor/%s/prusa_uploads/config", b.cfg.MQTT.DiscoveryPrefix, node)] = merge(
			common("Prusa Connect uploads count", "prusa_uploads"), map[string]any{
				"state_topic":    b.statusTopic(),
				"value_template": "{{ value_json.prusa_uploads }}",
				"state_class":    "total_increasing",
			})
		configs[fmt.Sprintf("%s/sensor/%s/prusa_failures/config", b.cfg.MQTT.DiscoveryPrefix, node)] = merge(
			common("Prusa Connect upload failures", "prusa_failures"), map[string]any{
				"state_topic":    b.statusTopic(),
				"value_template": "{{ value_json.prusa_failures }}",
				"state_class":    "total_increasing",
			})
	}

	out := make(map[string][]byte, len(configs))
	for topic, payload := range configs {
		data, err := json.Marshal(payload)
		if err != nil {
			b.logger.Error("mqtt: marshaling discovery config", "topic", topic, "error", err)
			continue
		}
		out[topic] = data
	}
	return out
}
