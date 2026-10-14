package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ellingtonsp/herdr-orch/internal/paths"
	"github.com/ellingtonsp/herdr-orch/internal/rpc"
)

// fakeDaemon answers ping, and for every other op reads the request then drops the
// connection without replying (as a daemon that restarts mid-request would).
func fakeDaemon(t *testing.T) (sock string, seen func(op string) int) {
	t.Helper()
	// Unix socket paths are limited to ~104 bytes on macOS; t.TempDir() is often longer.
	dir, err := os.MkdirTemp("/tmp", "horch-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock = filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	counts := map[string]int{}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				line, _ := bufio.NewReader(conn).ReadBytes('\n')
				var req rpc.Request
				_ = json.Unmarshal(line, &req)
				mu.Lock()
				counts[req.Op]++
				mu.Unlock()
				if req.Op == "ping" {
					_, _ = conn.Write([]byte(`{"ok":true,"result":{}}` + "\n"))
				}
			}()
		}
	}()
	return sock, func(op string) int { mu.Lock(); defer mu.Unlock(); return counts[op] }
}

func TestLostRequestIsNotResentUnlessIdempotent(t *testing.T) {
	sock, seen := fakeDaemon(t)
	c := &Client{Paths: paths.Paths{Sock: sock}, Caller: "w1:p2"}

	err := c.Call(context.Background(), "ask", map[string]string{"question": "?"}, nil)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != "outcome_unknown" {
		t.Fatalf("ask after a dropped connection: want outcome_unknown, got %v", err)
	}
	if n := seen("ask"); n != 1 {
		t.Fatalf("ask was sent %d times; a lost ask must not be re-sent", n)
	}

	_ = c.Call(context.Background(), "check", nil, nil)
	if n := seen("check"); n != 2 {
		t.Fatalf("check was sent %d times; an idempotent op should be retried once", n)
	}
}

func TestUnavailableDaemonNeverLaunchesFromPane(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "spawned")
	bin := filepath.Join(dir, "daemon")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HORCH_DAEMON_BIN", bin)
	t.Setenv("HERDR_PANE_ID", "w1:developer")
	c := &Client{Paths: paths.Paths{Sock: filepath.Join(dir, "missing.sock"), Dir: dir, Log: filepath.Join(dir, "daemon.log")}, Caller: Caller()}
	for _, err := range []error{
		c.Call(context.Background(), "ping", nil, nil),
		c.Stream(context.Background(), "removed.stream", nil, func(json.RawMessage) error { return nil }),
	} {
		var refusal *rpc.Error
		if !errors.As(err, &refusal) || refusal.Code != "daemon_not_running" {
			t.Fatalf("missing daemon: %v", err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("client launched daemon: %v", err)
	}
	if _, err := os.Stat(c.Paths.Log); !os.IsNotExist(err) {
		t.Fatalf("client opened daemon log: %v", err)
	}
}
