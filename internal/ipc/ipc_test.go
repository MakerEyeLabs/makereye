package ipc

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestServeAndCall(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "control.sock")

	handler := func(_ context.Context, req Request) Response {
		switch req.Command {
		case "ping":
			return Response{OK: true, Message: "pong"}
		default:
			return Response{OK: false, Error: "unknown command"}
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveErr := make(chan error, 1)
	go func() { serveErr <- Serve(ctx, sockPath, handler) }()

	waitForSocket(t, sockPath, time.Second)

	resp, err := Call(context.Background(), sockPath, "ping")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !resp.OK || resp.Message != "pong" {
		t.Errorf("resp = %+v, want OK with message pong", resp)
	}

	resp, err = Call(context.Background(), sockPath, "bogus")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp.OK {
		t.Errorf("expected error response for bogus command, got %+v", resp)
	}

	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Errorf("Serve returned error after cancel: %v", err)
		}
	case <-time.After(time.Second):
		t.Error("Serve did not return after context cancellation")
	}
}

func TestCallNoServer(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "nonexistent.sock")

	if _, err := Call(context.Background(), sockPath, "ping"); err == nil {
		t.Error("expected error calling nonexistent socket")
	}
}

func waitForSocket(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := Call(context.Background(), path, "ping"); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
