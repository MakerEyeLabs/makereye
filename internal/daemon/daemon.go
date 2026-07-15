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
	"github.com/MakerEyeLabs/makereye/internal/snapshot"
	"github.com/MakerEyeLabs/makereye/internal/timelapse"
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
	lapse      *timelapse.Manager
	bridge     *mqtt.Bridge
}

// New creates a Daemon for cfg.
func New(cfg *config.Config, logger *slog.Logger) *Daemon {
	if logger == nil {
		logger = slog.Default()
	}
	source := snapshot.NewGo2rtcSource(cfg)
	d := &Daemon{
		cfg:        cfg,
		logger:     logger,
		supervisor: go2rtc.NewSupervisor(cfg, logger),
		uploader:   prusaconnect.NewUploader(cfg, source, logger),
		lights:     lighting.NewManager(cfg, logger),
	}
	d.lapse = timelapse.NewManager(cfg, source, timelapse.LightHooks{
		States: d.lights.States,
		Set:    d.lights.Set,
	}, logger)
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
		TimelapseStart: func(_ context.Context, name string, interval, fps int) error {
			_, err := d.lapse.StartJob(timelapse.JobParams{
				Name: name, IntervalSeconds: interval, PlaybackFPS: fps,
			})
			return err
		},
		TimelapseStop: func(_ context.Context) error {
			_, err := d.lapse.StopJob()
			return err
		},
		TimelapseRenderLast: func(_ context.Context) error {
			last, ok := d.lapse.LastJob()
			if !ok {
				return fmt.Errorf("no timelapse jobs exist")
			}
			return d.lapse.Render(last.ID)
		},
		Status: d.mqttStatus,
	})
	// Push MQTT state promptly on timelapse phase transitions.
	d.lapse.SetOnChange(d.bridge.PublishState)
	return d
}

// mqttStatus snapshots the subsystems for the MQTT bridge.
func (d *Daemon) mqttStatus() mqtt.Status {
	st := d.supervisor.Status()
	pst := d.uploader.Status()
	s := mqtt.Status{
		StreamPhase:    string(st.Phase),
		StreamRestarts: st.RestartCount,
		PrusaPhase:     string(pst.Phase),
		PrusaUploads:   pst.UploadCount,
		PrusaFailures:  pst.FailureCount,
		TimelapsePhase: "idle",
	}
	// Newest error across subsystems, so one HA sensor answers "what
	// went wrong last" for the whole device.
	newestMsg, newestAt := "", time.Time{}
	consider := func(origin, msg string, at time.Time) {
		if msg != "" && at.After(newestAt) {
			newestMsg, newestAt = origin+": "+msg, at
		}
	}
	consider("stream", st.LastError, st.LastErrorAt)
	consider("prusa_connect", pst.LastError, pst.LastErrorAt)

	if d.cfg.Timelapse.Enabled {
		job, ok := d.lapse.Active()
		if !ok {
			job, ok = d.lapse.LastJob()
		}
		if ok {
			s.TimelapsePhase = string(job.Phase)
			s.TimelapseJob = job.Name
			s.TimelapseFrames = job.FrameCount
			s.TimelapseFailures = job.FailureCount
			switch {
			case job.Phase == timelapse.PhaseComplete:
				s.TimelapseLastResult = "complete"
				s.TimelapseOutput = job.OutputPath()
			case job.LastError != "":
				s.TimelapseLastResult = job.LastError
			}
			consider("timelapse", job.LastError, job.LastErrorAt)
		}
	}

	if newestMsg != "" {
		s.LastError = newestMsg
		s.LastErrorAt = newestAt.Format(time.RFC3339)
	}
	return s
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

	if d.cfg.Timelapse.Enabled {
		if err := d.lapse.Start(ctx); err != nil {
			// Advisory subsystem: log and continue.
			d.logger.Error("starting timelapse", "error", err)
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
	if err := d.lapse.Stop(stopCtx); err != nil {
		d.logger.Error("error stopping timelapse", "error", err)
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
	case ipc.CmdTimelapseStart, ipc.CmdTimelapseStop, ipc.CmdTimelapseStatus,
		ipc.CmdTimelapseList, ipc.CmdTimelapseRender:
		return d.handleTimelapse(req)
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

func (d *Daemon) handleTimelapse(req ipc.Request) ipc.Response {
	if !d.cfg.Timelapse.Enabled {
		return ipc.Response{OK: false, Error: "timelapse.enabled is false in config"}
	}

	switch req.Command {
	case ipc.CmdTimelapseStart:
		job, err := d.lapse.StartJob(timelapse.JobParams{
			Name:            req.TimelapseName,
			IntervalSeconds: req.TimelapseInterval,
			PlaybackFPS:     req.TimelapseFPS,
			Light:           req.TimelapseLight,
			LightBrightness: req.TimelapseLightVal,
		})
		if err != nil {
			return ipc.Response{OK: false, Error: err.Error()}
		}
		return ipc.Response{OK: true, Message: fmt.Sprintf(
			"timelapse started: id=%s name=%q interval=%ds fps=%d",
			job.ID, job.Name, job.IntervalSeconds, job.PlaybackFPS)}

	case ipc.CmdTimelapseStop:
		job, err := d.lapse.StopJob()
		if err != nil {
			return ipc.Response{OK: false, Error: err.Error()}
		}
		if job.ID == "" {
			return ipc.Response{OK: true, Message: "no timelapse job to stop"}
		}
		msg := fmt.Sprintf("timelapse stopped: id=%s frames=%d", job.ID, job.FrameCount)
		if d.cfg.Timelapse.AutoRender && job.FrameCount > 0 {
			msg += " (auto-render started)"
		}
		return ipc.Response{OK: true, Message: msg}

	case ipc.CmdTimelapseStatus:
		if job, ok := d.lapse.Active(); ok {
			return ipc.Response{OK: true, Status: string(job.Phase),
				Message: formatJob(job)}
		}
		if job, ok := d.lapse.LastJob(); ok {
			return ipc.Response{OK: true, Status: string(job.Phase),
				Message: "no active job; most recent:\n" + formatJob(job)}
		}
		return ipc.Response{OK: true, Status: "idle", Message: "no timelapse jobs"}

	case ipc.CmdTimelapseList:
		jobs := d.lapse.List()
		if len(jobs) == 0 {
			return ipc.Response{OK: true, Message: "no timelapse jobs"}
		}
		var b strings.Builder
		for _, j := range jobs {
			fmt.Fprintln(&b, formatJob(j))
		}
		return ipc.Response{OK: true, Message: strings.TrimRight(b.String(), "\n")}

	case ipc.CmdTimelapseRender:
		if req.TimelapseJobID == "" {
			return ipc.Response{OK: false, Error: "timelapse render requires a job id (see: makereye timelapse list)"}
		}
		if err := d.lapse.Render(req.TimelapseJobID); err != nil {
			return ipc.Response{OK: false, Error: err.Error()}
		}
		return ipc.Response{OK: true, Message: "render started for job " + req.TimelapseJobID}
	}
	return ipc.Response{OK: false, Error: "unhandled timelapse command"}
}

// formatJob renders one job as a single status line.
func formatJob(j timelapse.Job) string {
	line := fmt.Sprintf("%s  %-14s name=%q frames=%d failures=%d interval=%ds fps=%d",
		j.ID, j.Phase, j.Name, j.FrameCount, j.FailureCount, j.IntervalSeconds, j.PlaybackFPS)
	if j.Phase == timelapse.PhaseComplete && j.OutputPath() != "" {
		line += " output=" + j.OutputPath()
	}
	if j.LastError != "" {
		line += " last_error=" + j.LastError
	}
	return line
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

	if d.cfg.Timelapse.Enabled {
		if job, ok := d.lapse.Active(); ok {
			msg += fmt.Sprintf("\ntimelapse: %s job=%s name=%q frames=%d failures=%d",
				job.Phase, job.ID, job.Name, job.FrameCount, job.FailureCount)
		} else if job, ok := d.lapse.LastJob(); ok {
			msg += fmt.Sprintf("\ntimelapse: idle (last: %s %s frames=%d)",
				job.ID, job.Phase, job.FrameCount)
		} else {
			msg += "\ntimelapse: idle"
		}
	} else {
		msg += "\ntimelapse: disabled"
	}

	return ipc.Response{OK: true, Status: string(st.Phase), Message: msg}
}
