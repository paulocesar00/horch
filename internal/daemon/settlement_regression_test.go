package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/ellingtonsp/herdr-orch/internal/store"
)

func TestDoneWaitsForObservedIdleAfterRestart(t *testing.T) {
	for _, status := range []string{"", "unknown", "starting", "working", "blocked"} {
		name := status
		if name == "" {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			x := newHarness(t)
			setupRun(x, "w1:p2")
			var task store.Task
			x.mustOp(coord, "task.create", TaskCreateArgs{Title: "A"}, &task)
			x.mustOp(coord, "dispatch", DispatchArgs{Task: task.ID, To: "w1:p2"}, nil)
			x.e.mu.Lock()
			x.e.status["w1:p2"] = status
			x.e.mu.Unlock()
			x.mustOp("w1:p2", "send", SendArgs{Kind: store.KindDone, Task: task.ID, Body: "finished"}, nil)
			x.e.Tick()
			if got := x.task(task.ID).Status; got != store.TaskDispatched {
				t.Fatalf("settled with status %q: %s", status, got)
			}
			x.status("w1:p2", "idle")
			x.advance(time.Second)
			if got := x.task(task.ID); got.Status != store.TaskCompleted || got.Result != "finished" {
				t.Fatalf("idle did not settle the reported result: %+v", got)
			}
			if got := x.inbox(coord); len(got) != 1 || got[0].Kind != store.KindDone {
				t.Fatalf("want one completion: %+v", got)
			}
		})
	}
}

func TestEmptyStatusClearsIdleSettlementEvidence(t *testing.T) {
	x := newHarness(t)
	setupRun(x, "w1:p2")
	var task store.Task
	x.mustOp(coord, "task.create", TaskCreateArgs{Title: "A"}, &task)
	x.mustOp(coord, "dispatch", DispatchArgs{Task: task.ID, To: "w1:p2"}, nil)
	x.status("w1:p2", "")
	x.mustOp("w1:p2", "send", SendArgs{Kind: store.KindDone, Task: task.ID}, nil)
	x.e.Tick()
	if got := x.task(task.ID).Status; got != store.TaskDispatched {
		t.Fatalf("settled using idle from before the empty status: %s", got)
	}
	x.status("w1:p2", "done")
	x.advance(time.Second)
	if got := x.task(task.ID).Status; got != store.TaskCompleted {
		t.Fatalf("observed done did not settle: %s", got)
	}
}

func TestDoneDoesNotUseIdleFromBeforeDispatch(t *testing.T) {
	x := newHarness(t)
	setupRun(x, "w1:p2")
	x.status("w1:p2", "idle")
	var task store.Task
	x.mustOp(coord, "task.create", TaskCreateArgs{Title: "A"}, &task)
	x.mustOp(coord, "dispatch", DispatchArgs{Task: task.ID, To: "w1:p2"}, nil)
	x.mustOp("w1:p2", "send", SendArgs{Kind: store.KindDone, Task: task.ID}, nil)
	x.e.Tick()
	if got := x.task(task.ID).Status; got != store.TaskDispatched {
		t.Fatalf("pre-dispatch idle settled task: %s", got)
	}
	x.status("w1:p2", "idle")
	x.advance(time.Second)
	if got := x.task(task.ID).Status; got != store.TaskCompleted {
		t.Fatalf("post-dispatch idle did not settle: %s", got)
	}
}

func TestDoneRequiresOneFullIdleTick(t *testing.T) {
	for _, reportFirst := range []bool{false, true} {
		name := "idle-before-report"
		if reportFirst {
			name = "report-before-idle"
		}
		t.Run(name, func(t *testing.T) {
			x := newHarness(t)
			setupRun(x, "w1:p2")
			var task store.Task
			x.mustOp(coord, "task.create", TaskCreateArgs{Title: "A"}, &task)
			x.mustOp(coord, "dispatch", DispatchArgs{Task: task.ID, To: "w1:p2"}, nil)
			x.status("w1:p2", "working")
			if !reportFirst {
				x.status("w1:p2", "idle")
			}
			x.mustOp("w1:p2", "send", SendArgs{Kind: store.KindDone, Task: task.ID}, nil)
			if reportFirst {
				x.status("w1:p2", "idle")
			}
			x.advance(999 * time.Millisecond)
			if got := x.task(task.ID).Status; got != store.TaskDispatched {
				t.Fatalf("settled before one full idle tick: %s", got)
			}
			x.status("w1:p2", "working")
			x.advance(time.Second)
			if got := x.task(task.ID).Status; got != store.TaskDispatched {
				t.Fatalf("settled after work resumed: %s", got)
			}
			x.status("w1:p2", "idle")
			x.advance(time.Second)
			if got := x.task(task.ID).Status; got != store.TaskCompleted {
				t.Fatalf("stable idle did not settle: %s", got)
			}
		})
	}
}

func TestStartupFencesPendingDispatchWithoutResending(t *testing.T) {
	x := newHarness(t)
	setupRun(x, "w1:p2")
	var task store.Task
	x.mustOp(coord, "task.create", TaskCreateArgs{Title: "A"}, &task)
	d, err := x.st.BeginDispatch(task.ID, "w1:p2", "pi", "hash")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		e := NewEngine(x.st, x.h, DefaultConfig())
		e.Tick()
		e.CloseDeliveries()
	}
	got, err := x.st.GetDispatch(d.ID)
	if err != nil || got.Active() || !strings.Contains(got.Outcome, "outcome_unknown") {
		t.Fatalf("orphan dispatch: %+v err=%v", got, err)
	}
	if got := x.task(task.ID); got.Status != store.TaskReady || got.Attempts != 0 {
		t.Fatalf("uncertain submission consumed attempt: %+v", got)
	}
	if x.h.promptCount(d.PaneID) != 0 {
		t.Fatal("restart replayed an uncertain prompt")
	}
	msgs := x.inbox(coord)
	if len(msgs) != 1 || msgs[0].Kind != store.KindEscalation || !strings.Contains(msgs[0].Body, "before resending") {
		t.Fatalf("want one recovery warning: %+v", msgs)
	}
}

func TestUnknownDoneOutcomeDoesNotSettle(t *testing.T) {
	for _, outcome := range []string{"", "completed", "unknown"} {
		name := outcome
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			x := newHarness(t)
			setupRun(x, "w1:p2")
			var task store.Task
			x.mustOp(coord, "task.create", TaskCreateArgs{Title: "A"}, &task)
			var d store.Dispatch
			x.mustOp(coord, "dispatch", DispatchArgs{Task: task.ID, To: "w1:p2"}, &d)
			m, err := x.st.InsertMessage(store.Message{From: d.PaneID, To: store.Daemon, Kind: store.KindDone})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := x.st.RecordDone(d.ID, m.ID, outcome); err != nil {
				t.Fatal(err)
			}
			x.status(d.PaneID, "idle")
			x.advance(2 * time.Second)
			if got := x.task(task.ID).Status; got != store.TaskDispatched {
				t.Fatalf("unknown outcome %q settled: %s", outcome, got)
			}
		})
	}
}
