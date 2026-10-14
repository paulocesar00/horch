package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ellingtonsp/herdr-orch/internal/store"
)

func TestCheckAcknowledgesOnlyAfterStdoutSucceeds(t *testing.T) {
	for _, format := range []string{"human", "json"} {
		t.Run(format, func(t *testing.T) {
			startDaemon(t)
			mustHorch(t, "w1:p1", "run", "create", "--title", "mail")
			mustHorch(t, "w1:p1", "send", "--to", "human", "--body", "keep this message")
			cli.Caller = "human"
			args := []string{"check"}
			if format == "json" {
				args = append(args, "--json")
			}
			closed, err := os.CreateTemp(t.TempDir(), "closed-stdout")
			if err != nil {
				t.Fatal(err)
			}
			closed.Close()
			old := os.Stdout
			os.Stdout = closed
			err = dispatch(context.Background(), args)
			os.Stdout = old
			if err == nil {
				t.Fatal("check ignored failed stdout")
			}
			var msgs []store.Message
			raw := mustHorch(t, "human", "check", "--peek", "--json")
			if err := json.Unmarshal([]byte(raw), &msgs); err != nil || len(msgs) != 1 || msgs[0].ReadAt != 0 {
				t.Fatalf("failed stdout consumed mail: %s err=%v", raw, err)
			}
			if out := mustHorch(t, "human", args...); !strings.Contains(out, "keep this message") {
				t.Fatalf("check output: %s", out)
			}
			if out := mustHorch(t, "human", "check", "--peek", "--json"); strings.TrimSpace(out) != "[]" {
				t.Fatalf("successful stdout did not acknowledge mail: %s", out)
			}
		})
	}
}
