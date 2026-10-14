package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/ellingtonsp/herdr-orch/internal/herdr"
	"github.com/ellingtonsp/herdr-orch/internal/store"
)

type changingPromptHerdr struct {
	*fakeHerdr
	status string
}

func (h *changingPromptHerdr) AgentPrompt(_ context.Context, pane, _ string) error {
	h.mu.Lock()
	h.panes[pane].AgentStatus = h.status
	h.mu.Unlock()
	return &herdr.Error{Code: "agent_not_found", Message: "no named agent"}
}

func TestDispatchFallbackRefusesPaneThatBecameBusy(t *testing.T) {
	for _, status := range []string{"working", "blocked"} {
		t.Run(status, func(t *testing.T) {
			x := newHarness(t)
			setupRun(x)
			x.h.addPane("w1:p2", "pi", "idle", false)
			x.e.h = &changingPromptHerdr{fakeHerdr: x.h, status: status}
			var task store.Task
			x.mustOp(coord, "task.create", TaskCreateArgs{Title: "A"}, &task)
			_, err := x.op(coord, "dispatch", DispatchArgs{Task: task.ID, To: "w1:p2"})
			if err == nil || x.h.promptCount("w1:p2") != 0 {
				t.Fatalf("typed into %s pane: err=%v prompts=%d", status, err, x.h.promptCount("w1:p2"))
			}
			if got := x.task(task.ID); got.Status != store.TaskReady || got.Attempts != 0 {
				t.Fatalf("rejected prompt consumed an attempt: %+v", got)
			}
		})
	}
}

func TestDispatchRefusesUnknownOrNoninteractivePane(t *testing.T) {
	for _, status := range []string{"unknown", "starting", "idle"} {
		t.Run(status, func(t *testing.T) {
			x := newHarness(t)
			setupRun(x, "w1:p2")
			x.h.mu.Lock()
			x.h.panes["w1:p2"].AgentStatus = status
			if status == "idle" {
				ready := false
				x.h.panes["w1:p2"].InteractiveReady = &ready
			}
			x.h.mu.Unlock()
			var task store.Task
			x.mustOp(coord, "task.create", TaskCreateArgs{Title: "A"}, &task)
			_, err := x.op(coord, "dispatch", DispatchArgs{Task: task.ID, To: "w1:p2"})
			if err == nil || x.h.promptCount("w1:p2") != 0 {
				t.Fatalf("prompted unready pane: err=%v prompts=%d", err, x.h.promptCount("w1:p2"))
			}
			if got := x.task(task.ID); got.Status != store.TaskReady || got.Attempts != 0 {
				t.Fatalf("unready pane consumed task: %+v", got)
			}
		})
	}
}

type snapshotEventHerdr struct {
	*fakeHerdr
	engine      *Engine
	eventStatus string
}

func (h *snapshotEventHerdr) PaneGet(ctx context.Context, pane string) (herdr.Pane, error) {
	p, err := h.fakeHerdr.PaneGet(ctx, pane)
	h.engine.setStatus(pane, h.eventStatus)
	return p, err
}

func TestSyncStatusesDoesNotOverwriteNewerEvent(t *testing.T) {
	x := newHarness(t)
	setupRun(x, "w1:p2")
	var task store.Task
	x.mustOp(coord, "task.create", TaskCreateArgs{Title: "A"}, &task)
	x.mustOp(coord, "dispatch", DispatchArgs{Task: task.ID, To: "w1:p2"}, nil)
	x.e.h = &snapshotEventHerdr{fakeHerdr: x.h, engine: x.e, eventStatus: "working"}
	x.e.SyncStatuses(context.Background())
	x.mustOp("w1:p2", "send", SendArgs{Kind: store.KindDone, Task: task.ID}, nil)
	x.e.Tick()
	if got := x.task(task.ID).Status; got != store.TaskDispatched {
		t.Fatalf("stale idle snapshot overwrote working event: %s", got)
	}
	x.status("w1:p2", "idle")
	x.advance(time.Second)
	if got := x.task(task.ID).Status; got != store.TaskCompleted {
		t.Fatalf("fresh idle did not settle: %s", got)
	}
}

func TestDispatchDoesNotOverwriteNewerWorkingEvent(t *testing.T) {
	x := newHarness(t)
	setupRun(x, "w1:p2")
	x.e.h = &snapshotEventHerdr{fakeHerdr: x.h, engine: x.e, eventStatus: "working"}
	var task store.Task
	x.mustOp(coord, "task.create", TaskCreateArgs{Title: "A"}, &task)
	_, err := x.op(coord, "dispatch", DispatchArgs{Task: task.ID, To: "w1:p2"})
	if err == nil || x.h.promptCount("w1:p2") != 0 {
		t.Fatalf("stale idle snapshot allowed dispatch: err=%v prompts=%d", err, x.h.promptCount("w1:p2"))
	}
	if got := x.task(task.ID); got.Status != store.TaskReady || got.Attempts != 0 {
		t.Fatalf("stale snapshot consumed task: %+v", got)
	}
}

func TestDispatchRefusesObservedBusyDespiteIdleSnapshot(t *testing.T) {
	for _, status := range []string{"working", "blocked"} {
		t.Run(status, func(t *testing.T) {
			x := newHarness(t)
			setupRun(x, "w1:p2")
			x.e.setStatus("w1:p2", status)
			var task store.Task
			x.mustOp(coord, "task.create", TaskCreateArgs{Title: "A"}, &task)
			_, err := x.op(coord, "dispatch", DispatchArgs{Task: task.ID, To: "w1:p2"})
			if err == nil || x.h.promptCount("w1:p2") != 0 {
				t.Fatalf("idle snapshot bypassed observed %s: err=%v", status, err)
			}
			if got := x.task(task.ID); got.Status != store.TaskReady || got.Attempts != 0 {
				t.Fatalf("busy observation consumed task: %+v", got)
			}
		})
	}
}

type reportingPromptHerdr struct {
	*fakeHerdr
	engine *Engine
}

func (h *reportingPromptHerdr) AgentPrompt(ctx context.Context, pane, text string) error {
	h.engine.setStatus(pane, "idle")
	if _, err := h.engine.done(pane, SendArgs{Body: "finished"}); err != nil {
		return err
	}
	return h.fakeHerdr.AgentPrompt(ctx, pane, text)
}

func TestDoneDoesNotUseIdleObservedBeforePromptReturns(t *testing.T) {
	x := newHarness(t)
	setupRun(x, "w1:p2")
	x.e.h = &reportingPromptHerdr{fakeHerdr: x.h, engine: x.e}
	var task store.Task
	x.mustOp(coord, "task.create", TaskCreateArgs{Title: "A"}, &task)
	x.mustOp(coord, "dispatch", DispatchArgs{Task: task.ID, To: "w1:p2"}, nil)
	x.e.Tick()
	if got := x.task(task.ID).Status; got != store.TaskDispatched {
		t.Fatalf("idle from before prompt submission settled task: %s", got)
	}
	x.status("w1:p2", "idle")
	x.advance(time.Second)
	if got := x.task(task.ID).Status; got != store.TaskCompleted {
		t.Fatalf("fresh idle did not settle: %s", got)
	}
}
