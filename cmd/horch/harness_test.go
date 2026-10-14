package main

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ellingtonsp/herdr-orch/internal/client"
	"github.com/ellingtonsp/herdr-orch/internal/daemon"
	"github.com/ellingtonsp/herdr-orch/internal/herdr"
	"github.com/ellingtonsp/herdr-orch/internal/store"
)

// nopHerdr supplies badges and notifications for mailbox CLI tests.
type nopHerdr struct{ daemon.Herdr }

// Mailbox CLI tests have a busy recipient; they consume through check rather
// than starting a real agent delivery turn.
func (nopHerdr) PaneGet(_ context.Context, pane string) (herdr.Pane, error) {
	agent := "synthetic"
	return herdr.Pane{PaneID: pane, WorkspaceID: "w1", Agent: &agent, AgentStatus: "working"}, nil
}

func (nopHerdr) PaneTokens(context.Context, string, map[string]*string) error { return nil }
func (nopHerdr) Notify(context.Context, string, string, bool) error           { return nil }

// startDaemon serves a real engine on the socket horch resolves for a throwaway session.
func startDaemon(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	for _, k := range []string{"HERDR_PLUGIN_STATE_DIR", "HERDR_SESSION", "HORCH_AS", "HERDR_PANE_ID", "HORCH_PRINCIPAL"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("HORCH_STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(dir, "sessions", "horch-cli-test", "herdr.sock"))
	t.Setenv("HORCH_DAEMON_BIN", "/nonexistent") // never spawn a real daemon
	c, err := client.New()
	if err != nil {
		t.Fatal(err)
	}
	cli = c
	st, err := store.Open(c.Paths.DB)
	if err != nil {
		t.Fatal(err)
	}
	cfg := daemon.DefaultConfig()
	e := daemon.NewEngine(st, nopHerdr{}, cfg)
	_ = os.Remove(c.Paths.Sock)
	ln, err := net.Listen("unix", c.Paths.Sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go daemon.Serve(ctx, ln, e)
	t.Cleanup(func() { cancel(); ln.Close(); e.CloseDeliveries(); st.Close(); os.Remove(c.Paths.Sock) })
}

// horch runs one command line as caller and returns its stdout.
func horch(t *testing.T, caller string, args ...string) (string, error) {
	t.Helper()
	cli.Caller = caller
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	err := dispatch(context.Background(), args)
	w.Close()
	os.Stdout = old
	return <-done, err
}

func mustHorch(t *testing.T, caller string, args ...string) string {
	t.Helper()
	out, err := horch(t, caller, args...)
	if err != nil {
		t.Fatalf("horch %s: %v", strings.Join(args, " "), err)
	}
	return out
}
