// Package ipc implements the small control protocol MakerEye's CLI uses
// to talk to the running "makereye run" daemon: a line-delimited JSON
// request/response exchange over a Unix domain socket. It exists because
// "stream start/stop/restart", "prusa start/stop/restart", and "status"
// act on the daemon's live subsystems (go2rtc supervisor, Prusa Connect
// uploader), not a separate process the CLI can manage directly.
package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"
)

// SocketName is the control socket filename created inside the
// configured run directory.
const SocketName = "control.sock"

// Request is a single command sent from the CLI to the daemon.
type Request struct {
	Command string `json:"command"`
}

// Response is the daemon's reply to a Request.
type Response struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Status  string `json:"status,omitempty"`
	Message string `json:"message,omitempty"`
}

// Known commands.
const (
	CmdStatus        = "status"
	CmdStreamStart   = "stream-start"
	CmdStreamStop    = "stream-stop"
	CmdStreamRestart = "stream-restart"
	CmdPrusaStart    = "prusa-start"
	CmdPrusaStop     = "prusa-stop"
	CmdPrusaRestart  = "prusa-restart"
)

// Handler processes a Request and returns a Response. The daemon supplies
// one to Serve.
type Handler func(ctx context.Context, req Request) Response

// Serve listens on the Unix socket at path and handles connections with
// handler until ctx is cancelled. It removes any stale socket file left
// over from a previous run before listening.
func Serve(ctx context.Context, path string, handler Handler) error {
	_ = os.Remove(path)

	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "unix", path)
	if err != nil {
		return fmt.Errorf("listening on control socket %q: %w", path, err)
	}
	defer ln.Close()
	defer os.Remove(path)

	if err := os.Chmod(path, 0o660); err != nil {
		return fmt.Errorf("setting control socket permissions: %w", err)
	}

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accepting control connection: %w", err)
		}
		go serveConn(ctx, conn, handler)
	}
}

func serveConn(ctx context.Context, conn net.Conn, handler Handler) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		return
	}

	var req Request
	if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
		writeResponse(conn, Response{OK: false, Error: "invalid request: " + err.Error()})
		return
	}

	resp := handler(ctx, req)
	writeResponse(conn, resp)
}

func writeResponse(conn net.Conn, resp Response) {
	enc := json.NewEncoder(conn)
	_ = enc.Encode(resp)
}

// Call connects to the control socket at path, sends command, and returns
// the daemon's response.
func Call(ctx context.Context, path, command string) (Response, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return Response{}, fmt.Errorf("connecting to makereye daemon at %q: %w (is the makereye service running?)", path, err)
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	}

	enc := json.NewEncoder(conn)
	if err := enc.Encode(Request{Command: command}); err != nil {
		return Response{}, fmt.Errorf("sending command: %w", err)
	}

	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return Response{}, fmt.Errorf("reading response: %w", err)
		}
		return Response{}, fmt.Errorf("no response from daemon")
	}

	var resp Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return Response{}, fmt.Errorf("parsing response: %w", err)
	}
	return resp, nil
}
