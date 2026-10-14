package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ellingtonsp/herdr-orch/internal/client"
	"github.com/ellingtonsp/herdr-orch/internal/rpc"
)

func TestRemovedCommandsRefuseWithoutRPC(t *testing.T) {
	old := cli
	cli = &client.Client{}
	defer func() { cli = old }()
	for _, args := range [][]string{
		{"plan"}, {"web"}, {"pr"}, {"lease"}, {"sweep"}, {"gate"}, {"schedule"},
		{"worker", "start"}, {"worker", "restart"}, {"worker", "stop"},
		{"worker", "release"}, {"worker", "retain"}, {"worker", "abandon"},
		{"dispatch", "hold"}, {"dispatch", "resume"},
		{"run", "update", "--max-load", "12"}, {"run", "update", "--min-free-mem", "15"},
		{"dispatch", "--task", "t1", "--lease", "port:3000"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			err := dispatch(context.Background(), args)
			var refusal *rpc.Error
			if !errors.As(err, &refusal) || refusal.Code != "usage" {
				t.Fatalf("removed command did not refuse locally: %v", err)
			}
		})
	}
}

func TestHelpListsOnlyCoreCommands(t *testing.T) {
	for _, cmd := range []string{"run", "send", "done", "check", "inbox", "ask", "reply", "ack", "task", "dispatch", "worker", "status", "daemon", "config", "guide", "version"} {
		if !strings.Contains(usage, "horch "+cmd) {
			t.Fatalf("missing command %s", cmd)
		}
	}
	for _, token := range []string{"horch plan", "horch web", "horch pr", "horch lease", "horch sweep", "horch gate", "horch schedule", "worker start", "worker restart", "new:", "--max-load", "--min-free-mem", "dispatch hold", "dispatch resume"} {
		if strings.Contains(usage, token) {
			t.Fatalf("removed help entry %s", token)
		}
	}
}
