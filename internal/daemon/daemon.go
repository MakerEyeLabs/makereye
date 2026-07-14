// Package daemon implements MakerEye's long-running process: it owns the
// go2rtc supervisor and the optional Prusa Connect uploader, and serves
// the control socket the CLI uses for status and start/stop/restart
// commands.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MakerEyeLabs/makereye/internal/config"
	"github.com/MakerEyeLabs/makereye/internal/go2rtc"
	"github.com/MakerEyeLabs/makereye/internal/ipc"
	"github.com/MakerEyeLabs/makereye/internal/lighting"
	"github.com/MakerEyeLabs/makereye/internal/mqtt"
	"github.com/MakerEyeLabs/makereye/internal/prusaconnect"
)

// Daemon is the running MakerEye process: it supervises go2rtc, the
// optional Prusa Connect uploader, lighting, and MQTT bridge, and
// serves the local control socket.
type Daemon struct {
	cfg        *config.Config
	logger     *slog.Logger
	supervisor *go2rtc.Supervisor
	uploader   *prusaconnect.Uploader
	lights     *lighting.Manager
	bridge     *mqtt.Bridge
}

// New creates a Daemon for cfg.
func New(cfg *config.Config, logger *slog.Logger) *Daemon {
	if logger == nil {
		logger = slog.Default()
	}
	d := &Daemon{
		cfg:        cfg,
		logger:     logger,
		supervisor: go2rtc.NewSupervisor(cfg, logger),
		uploader:   prusaconnect.NewUploader(cfg, logger),
		lights:     lighting.NewManager(cfg, logger),
	}
	d.bridge = mqtt.NewBridge(cfg, logger, mqtt.Hooks{
		StreamStart:   d.supervisor.Start,
		StreamStop:    d.supervisor.Stop,
		StreamRestart: d.supervisor.Restart,
		PrusaStart:    d.uploader.Start,
		PrusaStop:     d.uploader.Stop,
		PrusaRestart:  d.uploader.Restart,
		LightOn:       func(_ context.Context, name string) error { return d.lights.On(name) },
		LightOff:      func(_ context.Context, name string) error { return d.lights.Off(name) },
		LightSet:      func(_ context.Context, name string, level int) error { return d.lights.Set(name, level) },
		LightStates:   d.lights.States,
		Status:        d.mqttStatus,
	})
	return d
}

// mqttStatus snapshots the subsystems for the MQTT bridge.
func (d *Daemon) mqttStatus() mqtt.Status {
	st := d.supervisor.Status()
	pst := d.uploader.Status()
	return mqtt.Status{
		StreamPhase:    string(st.Phase),
		StreamRestarts: st.RestartCount,
		PrusaPhase:     string(pst.Phase),
		PrusaUploads:   pst.UploadCount,
		PrusaFailures:  pst.FailureCount,
	}
}

// SocketPath returns the control socket path for cfg's configured run
// directory.
func SocketPath(cfg *config.Config) string {
	return filepath.Join(cfg.System.RunDir, ipc.SocketName)
}

// Run starts the go2rtc supervisor and serves the control socket until ctx
// is cancelled (SIGTERM/SIGINT), then shuts everything down cleanly.
func (d *Daemon) Run(ctx context.Context) error {
	if err := os.MkdirAll(d.cfg.System.RunDir, 0o755); err != nil {
		return fmt.Errorf("creating run directory %q: %w", d.cfg.System.RunDir, err)
	}
	if err := os.MkdirAll(d.cfg.System.StateDir, 0o755); err != nil {
		return fmt.Errorf("creating state directory %q: %w", d.cfg.System.StateDir, err)
	}

	if err := d.supervisor.Start(ctx); err != nil {
		return fmt.Errorf("starting go2rtc: %w", err)
	}

	if d.cfg.PrusaConnect.Enabled {
		if err := d.uploader.Start(ctx); err != nil {
			return fmt.Errorf("starting prusa connect uploader: %w", err)
		}
	}

	if d.cfg.Lighting.Enabled {
		if err := d.lights.Start(ctx); err != nil {
			// Advisory subsystem: log and continue.
			d.logger.Error("starting lighting", "error", err)
		}
	}

	if d.cfg.MQTT.Enabled {
		if err := d.bridge.Start(ctx); err != nil {
			// Advisory subsystem: log and continue, never block core
			// operation on the MQTT integration.
			d.logger.Error("starting mqtt bridge", "error", err)
		}
	}

	sockPath := SocketPath(d.cfg)
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- ipc.Serve(ctx, sockPath, d.handle)
	}()

	d.logger.Info("makereye started",
		"stream", d.cfg.Stream.Name,
		"control_socket", sockPath)

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		if err != nil {
			d.logger.Error("control socket stopped unexpectedly", "error", err)
		}
	}

	d.logger.Info("shutting down")
	stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := d.supervisor.Stop(stopCtx); err != nil {
		d.logger.Error("error stopping go2rtc", "error", err)
	}
	if err := d.uploader.Stop(stopCtx); err != nil {
		d.logger.Error("error stopping prusa connect uploader", "error", err)
	}
	if err := d.bridge.Stop(stopCtx); err != nil {
		d.logger.Error("error stopping mqtt bridge", "error", err)
	}
	if err := d.lights.Stop(stopCtx); err != nil {
		d.logger.Error("error stopping lighting", "error", err)
	}
	return nil
}

func (d *Daemon) handle(ctx context.Context, req ipc.Request) ipc.Response {
	switch req.Command {
	case ipc.CmdStatus:
		return d.handleStatus()
	case ipc.CmdStreamStart:
		if err := d.supervisor.Start(ctx); err != nil {
			return ipc.Response{OK: false, Error: err.Error()}
		}
		return ipc.Response{OK: true, Message: "stream started"}
	case ipc.CmdStreamStop:
		if err := d.supervisor.Stop(ctx); err != nil {
			return ipc.Response{OK: false, Error: err.Error()}
		}
		return ipc.Response{OK: true, Message: "stream stopped"}
	case ipc.CmdStreamRestart:
		if err := d.supervisor.Restart(ctx); err != nil {
			return ipc.Response{OK: false, Error: err.Error()}
		}
		return ipc.Response{OK: true, Message: "stream restarted"}
	case ipc.CmdPrusaStart:
		if !d.cfg.PrusaConnect.Enabled {
			return ipc.Response{OK: false, Error: "prusa_connect.enabled is false in config"}
		}
		if err := d.uploader.Start(ctx); err != nil {
			return ipc.Response{OK: false, Error: err.Error()}
		}
		return ipc.Response{OK: true, Message: "prusa connect uploader started"}
	case ipc.CmdPrusaStop:
		if err := d.uploader.Stop(ctx); err != nil {
			return ipc.Response{OK: false, Error: err.Error()}
		}
		return ipc.Response{OK: true, Message: "prusa connect uploader stopped"}
	case ipc.CmdPrusaRestart:
		if !d.cfg.PrusaConnect.Enabled {
			return ipc.Response{OK: false, Error: "prusa_connect.enabled is false in config"}
		}
		if err := d.uploader.Restart(ctx); err != nil {
			return ipc.Response{OK: false, Error: err.Error()}
		}
		return ipc.Response{OK: true, Message: "prusa connect uploader restarted"}
	case ipc.CmdLightSet:
		return d.handleLightSet(req)
	default:
		return ipc.Response{OK: false, Error: fmt.Sprintf("unknown command %q", req.Command)}
	}
}

func (d *Daemon) handleLightSet(req ipc.Request) ipc.Response {
	if !d.cfg.Lighting.Enabled {
		return ipc.Response{OK: false, Error: "lighting.enabled is false in config"}
	}

	var err error
	switch strings.ToLower(req.Brightness) {
	case "on":
		err = d.lights.On(req.Light)
	case "off":
		err = d.lights.Off(req.Light)
	default:
		level, convErr := strconv.Atoi(req.Brightness)
		if convErr != nil {
			return ipc.Response{OK: false, Error: fmt.Sprintf("brightness must be \"on\", \"off\", or 0-255, got %q", req.Brightness)}
		}
		err = d.lights.Set(req.Light, level)
	}
	if err != nil {
		return ipc.Response{OK: false, Error: err.Error()}
	}
	return ipc.Response{OK: true, Message: fmt.Sprintf("light %q set to %d", req.Light, d.lights.States()[req.Light])}
}

func (d *Daemon) handleStatus() ipc.Response {
	st := d.supervisor.Status()
	msg := fmt.Sprintf("go2rtc: phase=%s pid=%d restarts=%d", st.Phase, st.PID, st.RestartCount)
	if st.LastError != "" {
		msg += " last_error=" + st.LastError
	}

	pst := d.uploader.Status()
	msg += fmt.Sprintf("\nprusa_connect: phase=%s uploads=%d failures=%d", pst.Phase, pst.UploadCount, pst.FailureCount)
	if pst.LastError != "" {
		msg += " last_error=" + pst.LastError
	}

	if d.cfg.MQTT.Enabled {
		mqttState := "disconnected (retrying)"
		if d.bridge.Connected() {
			mqttState = "connected"
		}
		msg += fmt.Sprintf("\nmqtt: %s broker=%s", mqttState, d.cfg.MQTT.BrokerURL)
	} else {
		msg += "\nmqtt: disabled"
	}

	if d.cfg.Lighting.Enabled {
		states := d.lights.States()
		names := make([]string, 0, len(states))
		for name := range states {
			names = append(names, name)
		}
		sort.Strings(names)
		msg += "\nlighting:"
		for _, name := range names {
			msg += fmt.Sprintf(" %s=%d", name, states[name])
		}
	} else {
		msg += "\nlighting: disabled"
	}

	return ipc.Response{OK: true, Status: string(st.Phase), Message: msg}
}
