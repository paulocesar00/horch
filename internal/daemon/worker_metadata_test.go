package daemon

import (
	"github.com/ellingtonsp/herdr-orch/internal/store"
	"strings"
	"testing"
	"time"
)

func TestDispatchMetadataSurvivesWorkerReregistration(t *testing.T) {
	x := newHarness(t)
	r := setupRun(x, "w1:p2")
	x.h.addPane("w1:p2", "codex", "idle", true)
	_, err := x.st.RegisterWorker(store.Worker{PaneID: "w1:p2", RunID: r.ID, Agent: "codex", Account: "account-a", Profile: "profile-a", Model: "model-a"})
	if err != nil {
		t.Fatal(err)
	}
	var task store.Task
	x.mustOp(coord, "task.create", TaskCreateArgs{Title: "work"}, &task)
	x.mustOp(coord, "dispatch", DispatchArgs{Task: task.ID, To: "w1:p2"}, nil)
	x.status("w1:p2", "working")
	x.mustOp("w1:p2", "send", SendArgs{Kind: store.KindDone, Task: task.ID, Body: "summary"}, nil)
	x.status("w1:p2", "idle")
	x.advance(time.Second)
	check := func(kind string) {
		t.Helper()
		messages := x.inbox(coord)
		if len(messages) != 1 || messages[0].Kind != kind {
			t.Fatalf("messages: %+v", messages)
		}
		for _, token := range []string{`account="account-a"`, `profile="profile-a"`, `requested model="model-a"`, `agent="codex"`} {
			if !strings.Contains(messages[0].Body, token) {
				t.Fatalf("missing %s in %q", token, messages[0].Body)
			}
		}
	}
	check(store.KindDone)
	if _, err := x.st.RegisterWorker(store.Worker{PaneID: "w1:p2", RunID: r.ID, Model: "model-b"}); err != nil {
		t.Fatal(err)
	}
	x.mustOp("w1:p2", "send", SendArgs{Kind: store.KindDone, Task: task.ID, Body: "late summary"}, nil)
	check(store.KindNote)
}
