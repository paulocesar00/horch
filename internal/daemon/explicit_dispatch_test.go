package daemon

import (
	"github.com/ellingtonsp/herdr-orch/internal/store"
	"testing"
)

func TestCompletionLeavesDependentReadyForExplicitDispatch(t *testing.T) {
	x := newHarness(t)
	setupRun(x, "w1:p2")
	var first, dependent store.Task
	x.mustOp(coord, "task.create", TaskCreateArgs{Title: "first"}, &first)
	x.mustOp(coord, "task.create", TaskCreateArgs{Title: "dependent", Deps: []string{first.ID}}, &dependent)
	x.mustOp(coord, "dispatch", DispatchArgs{Task: first.ID, To: "w1:p2"}, nil)
	x.status("w1:p2", "working")
	x.mustOp("w1:p2", "send", SendArgs{Kind: store.KindDone, Body: "finished"}, nil)
	x.status("w1:p2", "idle")
	x.e.Tick()
	x.drain()
	if x.task(dependent.ID).Status != store.TaskReady || x.h.promptCount("w1:p2") != 1 {
		t.Fatal("completion automatically dispatched the dependent")
	}
}
