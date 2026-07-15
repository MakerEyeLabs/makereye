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
	"strconv"
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
// subsystem packages directly.
type Status struct {
	StreamPhase    string `json:"stream_phase"`
	StreamRestarts int    `json:"stream_restarts"`
	PrusaPhase     string `json:"prusa_phase,omitempty"`
	PrusaUploads   int    `json:"prusa_uploads"`
	PrusaFailures  int    `json:"prusa_failures"`

	// Timelapse state: the active job when one is capturing, otherwise
	// the most recent job. Phase is "idle" when no jobs exist.
	TimelapsePhase      string `json:"timelapse_phase"`
	TimelapseJob        string `json:"timelapse_job"`
	TimelapseFrames     int    `json:"timelapse_frames"`
	TimelapseFailures   int    `json:"timelapse_failures"`
	TimelapseLastResult string `json:"timelapse_last_result"`
	TimelapseOutput     string `json:"timelapse_output"`

	// LastError is the newest error recorded by any subsystem
	// (stream/prusa/timelapse), prefixed with its origin, with
	// LastErrorAt as its RFC3339 timestamp. Empty when nothing has
	// errored. Command failures are reported separately (the "Last
	// command result" sensor).
	LastError   string `json:"last_error"`
	LastErrorAt string `json:"last_error_at"`
}

// Hooks are the daemon operations the bridge dispatches MQTT commands
// to. They mirror the control-socket commands; the daemon wires them to
// the same internals. Prusa hooks may be nil when Prusa Connect is
// disabled, and light hooks when lighting is disabled, in which case
// the corresponding entities are not published.
type Hooks struct {
	StreamStart   func(ctx context.Context) error
	StreamStop    func(ctx context.Context) error
	StreamRestart func(ctx context.Context) error
	PrusaStart    func(ctx context.Context) error
	PrusaStop     func(ctx context.Context) error
	PrusaRestart  func(ctx context.Context) error
	LightOn       func(ctx context.Context, name string) error
	LightOff      func(ctx context.Context, name string) error
	LightSet      func(ctx context.Context, name string, level int) error
	LightStates   func() map[string]int

	// Timelapse hooks. Start receives the per-job parameters currently
	// set through the HA number entities (0 = use config default).
	TimelapseStart      func(ctx context.Context, name string, intervalSec, fps int) error
	TimelapseStop       func(ctx context.Context) error
	TimelapseRenderLast func(ctx context.Context) error

	Status func() Status

	// Telemetry returns device metrics (version, OS, CPU/memory/disk
	// usage, temperature, uptime) published as HA diagnostic sensors.
	// Nil disables the telemetry entities.
	Telemetry func() map[string]any
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

	// Per-job timelapse parameters, settable through the HA number
	// entities and consumed by the start button. Initialized from the
	// config defaults; published retained so HA shows current values.
	tlInterval int
	tlFPS      int
}

// NewBridge creates a Bridge. It does not connect; call Start.
func NewBridge(cfg *config.Config, logger *slog.Logger, hooks Hooks) *Bridge {
	if logger == nil {
		logger = slog.Default()
	}
	return &Bridge{
		cfg:        cfg,
		logger:     logger,
		hooks:      hooks,
		newClient:  paho.NewClient,
		tlInterval: cfg.Timelapse.DefaultIntervalSeconds,
		tlFPS:      cfg.Timelapse.DefaultPlaybackFPS,
	}
}

// PublishState pushes current state immediately; safe to call anytime
// (no-op when not connected). Subsystems use this to make phase
// transitions visible in HA without waiting for the periodic refresh.
func (b *Bridge) PublishState() {
	if b.Connected() {
		b.publishState()
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
func (b *Bridge) commandResultTopic() string {
	return b.baseTopic() + "/last_command_result"
}
func (b *Bridge) telemetryTopic() string { return b.baseTopic() + "/telemetry" }

// publishTelemetry publishes the device-metrics document. Retained so
// HA restarts see the latest values immediately.
func (b *Bridge) publishTelemetry() {
	if b.hooks.Telemetry == nil {
		return
	}
	b.mu.Lock()
	client := b.client
	b.mu.Unlock()
	if client == nil {
		return
	}
	data, err := json.Marshal(b.hooks.Telemetry())
	if err != nil {
		b.logger.Error("mqtt: marshaling telemetry", "error", err)
		return
	}
	client.Publish(b.telemetryTopic(), 0, true, data)
}

// reportCommand publishes the outcome of an MQTT-initiated command so
// failures are visible in Home Assistant (the "Last command result"
// sensor), not only in journald. Retained, so the most recent outcome
// survives HA restarts.
func (b *Bridge) reportCommand(name string, err error) {
	result := name + ": ok"
	if err != nil {
		result = name + ": " + err.Error()
		b.logger.Warn("mqtt command failed", "command", name, "error", err)
	}
	b.mu.Lock()
	client := b.client
	b.mu.Unlock()
	if client != nil {
		client.Publish(b.commandResultTopic(), 0, true, result)
	}
}

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
				b.publishTelemetry()
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
	if b.lightingEnabled() {
		for _, lc := range b.cfg.Lighting.Lights {
			name := lc.Name
			lightBase := base + "/light/" + slug(name)
			client.Subscribe(lightBase+"/set", 1, b.lightSwitchHandler(name))
			client.Subscribe(lightBase+"/brightness/set", 1, b.lightBrightnessHandler(name))
		}
	}
	if b.timelapseEnabled() {
		client.Subscribe(base+"/timelapse/start", 1, b.timelapseStartHandler())
		client.Subscribe(base+"/timelapse/stop", 1, b.pressHandler("timelapse stop", b.hooks.TimelapseStop))
		client.Subscribe(base+"/timelapse/render_last", 1, b.pressHandler("timelapse render", b.hooks.TimelapseRenderLast))
		client.Subscribe(base+"/timelapse/interval/set", 1, b.numberHandler("timelapse interval", 1, 3600, &b.tlInterval))
		client.Subscribe(base+"/timelapse/fps/set", 1, b.numberHandler("timelapse fps", 1, 120, &b.tlFPS))
	}

	// Home Assistant publishes "online" to <discovery_prefix>/status
	// (its birth message) when it starts. Republishing discovery and
	// state then makes entities converge immediately after an HA
	// restart instead of waiting for the periodic refresh -- the
	// integration pattern HA's MQTT docs recommend.
	client.Subscribe(b.cfg.MQTT.DiscoveryPrefix+"/status", 1, func(c paho.Client, msg paho.Message) {
		if strings.TrimSpace(string(msg.Payload())) != "online" {
			return
		}
		b.logger.Info("home assistant came online, republishing discovery and state")
		for topic, payload := range b.discoveryConfigs() {
			c.Publish(topic, 0, true, payload)
		}
		b.publishState()
	})

	b.publishState()
	b.publishTelemetry()
}

func (b *Bridge) prusaEnabled() bool {
	return b.cfg.PrusaConnect.Enabled && b.hooks.PrusaStart != nil
}

func (b *Bridge) lightingEnabled() bool {
	return b.cfg.Lighting.Enabled && b.hooks.LightSet != nil
}

func (b *Bridge) timelapseEnabled() bool {
	return b.cfg.Timelapse.Enabled && b.hooks.TimelapseStart != nil
}

// timelapseStartHandler starts a job using the parameter values
// currently held by the number entities.
func (b *Bridge) timelapseStartHandler() paho.MessageHandler {
	return func(_ paho.Client, _ paho.Message) {
		b.mu.Lock()
		interval, fps := b.tlInterval, b.tlFPS
		b.mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		b.reportCommand("timelapse start", b.hooks.TimelapseStart(ctx, "", interval, fps))
		b.publishState()
	}
}

// numberHandler stores a HA number-entity command into target (bounded)
// and republishes state as the acknowledgement.
func (b *Bridge) numberHandler(name string, min, max int, target *int) paho.MessageHandler {
	return func(_ paho.Client, msg paho.Message) {
		v, err := strconv.Atoi(strings.TrimSpace(string(msg.Payload())))
		if err != nil || v < min || v > max {
			b.logger.Warn("mqtt: invalid number payload", "entity", name,
				"payload", string(msg.Payload()), "min", min, "max", max)
			return
		}
		b.mu.Lock()
		*target = v
		b.mu.Unlock()
		b.publishState()
	}
}

// lightSwitchHandler handles HA's ON/OFF light command.
func (b *Bridge) lightSwitchHandler(name string) paho.MessageHandler {
	return func(_ paho.Client, msg paho.Message) {
		payload := strings.ToUpper(strings.TrimSpace(string(msg.Payload())))
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()

		var err error
		switch payload {
		case "ON":
			err = b.hooks.LightOn(ctx, name)
		case "OFF":
			err = b.hooks.LightOff(ctx, name)
		default:
			b.logger.Warn("mqtt: unknown light payload", "light", name, "payload", payload)
			return
		}
		b.reportCommand("light "+name+" "+strings.ToLower(payload), err)
		b.publishState()
	}
}

// lightBrightnessHandler handles HA's numeric brightness command.
func (b *Bridge) lightBrightnessHandler(name string) paho.MessageHandler {
	return func(_ paho.Client, msg paho.Message) {
		level, err := strconv.Atoi(strings.TrimSpace(string(msg.Payload())))
		if err != nil {
			b.logger.Warn("mqtt: non-numeric brightness payload", "light", name, "payload", string(msg.Payload()))
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		b.reportCommand(fmt.Sprintf("light %s brightness %d", name, level), b.hooks.LightSet(ctx, name, level))
		b.publishState()
	}
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
		b.reportCommand(name+" "+strings.ToLower(payload), err)
		b.publishState()
	}
}

// pressHandler dispatches a button press to hook, then republishes
// state.
func (b *Bridge) pressHandler(name string, hook func(context.Context) error) paho.MessageHandler {
	return func(_ paho.Client, _ paho.Message) {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		b.reportCommand(name, hook(ctx))
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
	if b.lightingEnabled() {
		for name, level := range b.hooks.LightStates() {
			lightBase := base + "/light/" + slug(name)
			state := "OFF"
			if level > 0 {
				state = "ON"
			}
			client.Publish(lightBase+"/state", 0, false, state)
			client.Publish(lightBase+"/brightness", 0, false, strconv.Itoa(level))
		}
	}
	if b.timelapseEnabled() {
		b.mu.Lock()
		interval, fps := b.tlInterval, b.tlFPS
		b.mu.Unlock()
		// Retained so HA shows the current parameter values across its
		// own restarts.
		client.Publish(base+"/timelapse/interval", 0, true, strconv.Itoa(interval))
		client.Publish(base+"/timelapse/fps", 0, true, strconv.Itoa(fps))
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
	// origin identifies the integration publishing these entities;
	// recommended by HA's MQTT discovery docs for debuggability.
	origin := map[string]any{
		"name":        "MakerEye",
		"sw_version":  version.Version,
		"support_url": "https://github.com/MakerEyeLabs/makereye",
	}
	common := func(name, uniqueSuffix string) map[string]any {
		return map[string]any{
			"name":               name,
			"unique_id":          node + "_" + uniqueSuffix,
			"availability_topic": b.availabilityTopic(),
			"device":             device,
			"origin":             origin,
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
		fmt.Sprintf("%s/sensor/%s/last_command_result/config", b.cfg.MQTT.DiscoveryPrefix, node): merge(
			common("Last command result", "last_command_result"), map[string]any{
				"state_topic": b.commandResultTopic(),
			}),
		fmt.Sprintf("%s/sensor/%s/last_error/config", b.cfg.MQTT.DiscoveryPrefix, node): merge(
			common("Last error", "last_error"), map[string]any{
				"state_topic":    b.statusTopic(),
				"value_template": "{{ value_json.last_error }}",
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
	if b.lightingEnabled() {
		for _, lc := range b.cfg.Lighting.Lights {
			lightSlug := slug(lc.Name)
			lightBase := base + "/light/" + lightSlug
			configs[fmt.Sprintf("%s/light/%s/light_%s/config", b.cfg.MQTT.DiscoveryPrefix, node, lightSlug)] = merge(
				common(lc.Name, "light_"+lightSlug), map[string]any{
					"state_topic":              lightBase + "/state",
					"command_topic":            lightBase + "/set",
					"brightness_state_topic":   lightBase + "/brightness",
					"brightness_command_topic": lightBase + "/brightness/set",
					"brightness_scale":         255,
				})
		}
	}
	if b.timelapseEnabled() {
		tl := base + "/timelapse"
		dp := b.cfg.MQTT.DiscoveryPrefix
		configs[fmt.Sprintf("%s/button/%s/timelapse_start/config", dp, node)] = merge(
			common("Start timelapse", "timelapse_start"), map[string]any{
				"command_topic": tl + "/start",
				"payload_press": "PRESS",
			})
		configs[fmt.Sprintf("%s/button/%s/timelapse_stop/config", dp, node)] = merge(
			common("Stop timelapse", "timelapse_stop"), map[string]any{
				"command_topic": tl + "/stop",
				"payload_press": "PRESS",
			})
		configs[fmt.Sprintf("%s/button/%s/timelapse_render_last/config", dp, node)] = merge(
			common("Render last timelapse", "timelapse_render_last"), map[string]any{
				"command_topic": tl + "/render_last",
				"payload_press": "PRESS",
			})
		configs[fmt.Sprintf("%s/number/%s/timelapse_interval/config", dp, node)] = merge(
			common("Timelapse capture interval", "timelapse_interval"), map[string]any{
				"state_topic":         tl + "/interval",
				"command_topic":       tl + "/interval/set",
				"min":                 1,
				"max":                 3600,
				"step":                1,
				"unit_of_measurement": "s",
				"mode":                "box",
			})
		configs[fmt.Sprintf("%s/number/%s/timelapse_fps/config", dp, node)] = merge(
			common("Timelapse playback fps", "timelapse_fps"), map[string]any{
				"state_topic":   tl + "/fps",
				"command_topic": tl + "/fps/set",
				"min":           1,
				"max":           120,
				"step":          1,
				"mode":          "box",
			})
		sensors := map[string]string{
			"timelapse_phase":       "Timelapse phase",
			"timelapse_job":         "Timelapse job",
			"timelapse_frames":      "Timelapse frames",
			"timelapse_failures":    "Timelapse capture failures",
			"timelapse_last_result": "Timelapse last result",
			"timelapse_output":      "Timelapse last output",
		}
		for key, name := range sensors {
			configs[fmt.Sprintf("%s/sensor/%s/%s/config", dp, node, key)] = merge(
				common(name, key), map[string]any{
					"state_topic":    b.statusTopic(),
					"value_template": fmt.Sprintf("{{ value_json.%s }}", key),
				})
		}
	}

	if b.hooks.Telemetry != nil {
		dp := b.cfg.MQTT.DiscoveryPrefix
		// Missing keys (e.g. no vcgencmd off-Pi, no Wi-Fi on wired
		// devices) render as empty via | default instead of erroring.
		diag := func(component, name, key, template string, extra map[string]any) {
			cfg := merge(common(name, key), map[string]any{
				"state_topic":     b.telemetryTopic(),
				"value_template":  template,
				"entity_category": "diagnostic",
			})
			configs[fmt.Sprintf("%s/%s/%s/%s/config", dp, component, node, key)] = merge(cfg, extra)
		}
		sensor := func(name, key string, extra map[string]any) {
			diag("sensor", name, key,
				fmt.Sprintf("{{ value_json.%s | default('') }}", key), extra)
		}
		binary := func(name, key string, extra map[string]any) {
			diag("binary_sensor", name, key,
				fmt.Sprintf("{{ 'ON' if value_json.%s | default(false) else 'OFF' }}", key), extra)
		}
		attrs := func(keys ...string) map[string]any {
			pairs := make([]string, len(keys))
			for i, k := range keys {
				pairs[i] = fmt.Sprintf("%q: value_json.%s | default('')", k, k)
			}
			return map[string]any{
				"json_attributes_topic":    b.telemetryTopic(),
				"json_attributes_template": "{{ {" + strings.Join(pairs, ", ") + "} | tojson }}",
			}
		}

		sensor("OS version", "os_version", nil)
		sensor("CPU usage", "cpu_percent", map[string]any{
			"unit_of_measurement": "%", "state_class": "measurement",
		})
		sensor("Memory usage", "memory_percent", map[string]any{
			"unit_of_measurement": "%", "state_class": "measurement",
		})
		sensor("Memory available", "memory_available_mb", map[string]any{
			"unit_of_measurement": "MB", "device_class": "data_size",
			"state_class": "measurement",
		})
		sensor("CPU temperature", "cpu_temp_c", map[string]any{
			"unit_of_measurement": "°C", "device_class": "temperature",
			"state_class": "measurement",
		})
		sensor("Disk usage (root)", "disk_root_percent", map[string]any{
			"unit_of_measurement": "%", "state_class": "measurement",
		})
		sensor("Disk free (root)", "disk_root_free_gb", map[string]any{
			"unit_of_measurement": "GB", "device_class": "data_size",
			"state_class": "measurement",
		})
		if b.cfg.Timelapse.Enabled {
			sensor("Disk usage (capture)", "disk_capture_percent", map[string]any{
				"unit_of_measurement": "%", "state_class": "measurement",
			})
			sensor("Disk free (capture)", "disk_capture_free_gb", map[string]any{
				"unit_of_measurement": "GB", "device_class": "data_size",
				"state_class": "measurement",
			})
			binary("Capture storage low", "capture_storage_low", map[string]any{
				"device_class": "problem",
			})
		}
		sensor("Uptime", "uptime_seconds", merge(map[string]any{
			"unit_of_measurement": "s", "device_class": "duration",
		}, attrs("uptime_human")))
		sensor("Wi-Fi signal", "wifi_signal_dbm", merge(map[string]any{
			"unit_of_measurement": "dBm", "device_class": "signal_strength",
			"state_class": "measurement",
		}, attrs("wifi_link_quality", "wifi_interface")))
		binary("Undervoltage", "undervoltage", merge(map[string]any{
			"device_class": "problem",
		}, attrs("undervoltage_occurred")))
		binary("CPU throttled", "throttled", merge(map[string]any{
			"device_class": "problem",
		}, attrs("throttled_occurred", "freq_capped", "freq_capped_occurred")))
		binary("System clock synchronized", "clock_synchronized", nil)
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
