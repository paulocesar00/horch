package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ellingtonsp/herdr-orch/internal/paths"
	"github.com/ellingtonsp/herdr-orch/internal/rpc"
	"github.com/ellingtonsp/herdr-orch/internal/store"
)

func TestStartupSyncPrecedesFirstTick(t *testing.T) {
	dir, err := os.MkdirTemp("", "hs-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	p := paths.Paths{Session: "test", Dir: dir, DB: filepath.Join(dir, "orch.db"), Sock: filepath.Join(dir, "orch.sock"), Lock: filepath.Join(dir, "orch.lock"), HerdrSock: filepath.Join(dir, "herdr.sock")}
	st, err := store.Open(p.DB)
	if err != nil {
		t.Fatal(err)
	}
	st.Now = func() time.Time { return time.Now().Add(-2 * time.Minute) }
	r, err := st.CreateRun("restart", coord)
	if err != nil {
		t.Fatal(err)
	}
	task, err := st.CreateTask(r.ID, "A", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := st.BeginDispatch(task.ID, "w1:p2", "pi", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkSent(d.ID, "sent"); err != nil {
		t.Fatal(err)
	}
	st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ln, err := net.Listen("unix", p.HerdrSock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				stop := context.AfterFunc(ctx, func() { conn.Close() })
				defer stop()
				line, _ := bufio.NewReader(conn).ReadBytes('\n')
				var req struct {
					Method string `json:"method"`
				}
				if json.Unmarshal(line, &req) != nil {
					return
				}
				var result any = map[string]any{}
				switch req.Method {
				case "ping":
					result = map[string]any{"version": "test", "protocol": 22}
				case "pane.get":
					result = map[string]any{"pane": map[string]any{"pane_id": d.PaneID, "workspace_id": "w1", "agent": "pi", "agent_status": "working"}}
				case "events.subscribe":
					<-ctx.Done()
					return
				}
				json.NewEncoder(conn).Encode(map[string]any{"result": result})
			}()
		}
	}()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, Options{Paths: p, Config: DefaultConfig(), Logf: t.Logf}) }()
	deadline := time.Now().Add(2 * time.Second)
	for rpc.Call(ctx, p.Sock, "ping", coord, nil, nil) != nil {
		if time.Now().After(deadline) {
			t.Fatal("daemon did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(1200 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(p.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, err := st.GetDispatch(d.ID)
	if err != nil || !got.SawWorking || got.UnobservedEscalated {
		t.Fatalf("tick preceded initial status sync: %+v err=%v", got, err)
	}
}
