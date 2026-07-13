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
	"time"

	"github.com/MakerEyeLabs/makereye/internal/config"
	"github.com/MakerEyeLabs/makereye/internal/go2rtc"
	"github.com/MakerEyeLabs/makereye/internal/ipc"
	"github.com/MakerEyeLabs/makereye/internal/prusaconnect"
)

// Daemon is the running MakerEye process: it supervises go2rtc and the
// optional Prusa Connect uploader, and serves the local control socket.
type Daemon struct {
	cfg        *config.Config
	logger     *slog.Logger
	supervisor *go2rtc.Supervisor
	uploader   *prusaconnect.Uploader
}

// New creates a Daemon for cfg.
func New(cfg *config.Config, logger *slog.Logger) *Daemon {
	if logger == nil {
		logger = slog.Default()
	}
	return &Daemon{
		cfg:        cfg,
		logger:     logger,
		supervisor: go2rtc.NewSupervisor(cfg, logger),
		uploader:   prusaconnect.NewUploader(cfg, logger),
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
	default:
		return ipc.Response{OK: false, Error: fmt.Sprintf("unknown command %q", req.Command)}
	}
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

	return ipc.Response{OK: true, Status: string(st.Phase), Message: msg}
}
