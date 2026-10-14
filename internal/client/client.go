// Package client connects horch to the session daemon started by the herdr plugin.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/ellingtonsp/herdr-orch/internal/paths"
	"github.com/ellingtonsp/herdr-orch/internal/rpc"
)

type Client struct {
	Paths  paths.Paths
	Caller string
}

// Caller identifies who is talking: $HORCH_AS, else the herdr pane, else "human".
func Caller() string {
	if v := os.Getenv("HORCH_AS"); v != "" {
		return v
	}
	if v := os.Getenv("HERDR_PANE_ID"); v != "" {
		return v
	}
	return "human"
}

func New() (*Client, error) {
	p, err := paths.Resolve()
	if err != nil {
		return nil, err
	}
	return &Client{Paths: p, Caller: Caller()}, nil
}

// Only reads and recipient-keyed mailbox operations can be retried after disconnect.
var idempotent = map[string]bool{
	"ping": true, "check": true, "ack": true, "inbox": true,
	"run.current": true, "run.list": true, "run.show": true,
	"task.list": true, "task.show": true, "dispatch.show": true,
	"worker.show": true, "worker.list": true, "worker.read": true,
}

// Call retries safe operations once after a lost response. It never starts a daemon.
func (c *Client) Call(ctx context.Context, op string, args, out any) error {
	err := rpc.Call(ctx, c.Paths.Sock, op, c.Caller, args, out)
	if errors.Is(err, rpc.ErrLost) {
		if !idempotent[op] {
			return &rpc.Error{Code: "outcome_unknown", Message: lostAdvice(op)}
		}
		err = rpc.Call(ctx, c.Paths.Sock, op, c.Caller, args, out)
	}
	return daemonError(err)
}

func (c *Client) Stream(ctx context.Context, op string, args any, fn func(json.RawMessage) error) error {
	return daemonError(rpc.Stream(ctx, c.Paths.Sock, op, c.Caller, args, fn))
}

func daemonError(err error) error {
	if errors.Is(err, rpc.ErrUnavailable) {
		return &rpc.Error{Code: "daemon_not_running", Message: "daemon not running; start it through the herdr plugin (horch never starts a daemon)"}
	}
	return err
}

func lostAdvice(op string) string {
	msg := "the daemon disconnected while handling " + op + "; it may or may not have been applied. "
	switch op {
	case "ask":
		return msg + "Do not ask again: run `horch check --wait` to receive the reply to the question already sent (see `horch inbox`)."
	case "send":
		return msg + "Check `horch inbox` before sending again."
	case "dispatch", "dispatch.next":
		return msg + "Check `horch dispatch show --task <id>` before dispatching again."
	}
	return msg + "Check the current state (`horch run show`) before retrying."
}
