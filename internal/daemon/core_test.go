package daemon

import (
	"github.com/ellingtonsp/herdr-orch/internal/store"
	"testing"
)

func TestCoreOpsExcludeRemovedFeatures(t *testing.T) {
	x := newHarness(t)
	ops := x.e.Ops()
	for _, op := range []string{"worker.start", "worker.restart", "worker.stop", "worker.release", "worker.abandon", "worker.retain", "dispatch.hold", "dispatch.resume", "gate.create", "plan.show", "schedule.add", "lease.acquire", "sweep", "board", "web.activity"} {
		if _, ok := ops[op]; ok {
			t.Fatalf("removed operation still served: %s", op)
		}
	}
	for _, op := range []string{"worker.register", "worker.show", "worker.read", "worker.list", "send", "check", "ack", "ask", "reply", "inbox", "dispatch", "dispatch.next", "dispatch.show", "dispatch.nudge", "dispatch.fail"} {
		if ops[op] == nil {
			t.Fatalf("core operation missing: %s", op)
		}
	}
	if len(x.e.Streams()) != 0 {
		t.Fatal("removed plan stream still served")
	}
}

func TestDispatchRejectsNewWorkerBeforeCreatingTask(t *testing.T) {
	x := newHarness(t)
	r := setupRun(x)
	_, err := x.op(coord, "dispatch", DispatchArgs{To: "new:codex", Spec: "Do work"})
	wantRefusal(t, err, "bad_args")
	tasks, err := x.st.ListTasks(r.ID)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("rejected dispatch created tasks: %v %v", tasks, err)
	}
}

func TestPersistentWorkerHandlesTwoTasks(t *testing.T) {
	x := newHarness(t)
	setupRun(x, "w1:p2")
	x.mustOp(coord, "worker.register", WorkerRegisterArgs{Pane: "w1:p2", Name: "builder"}, nil)
	for _, title := range []string{"first", "second"} {
		var task store.Task
		x.mustOp(coord, "task.create", TaskCreateArgs{Title: title}, &task)
		x.mustOp(coord, "dispatch", DispatchArgs{Task: task.ID, To: "builder"}, nil)
		x.status("w1:p2", "working")
		x.mustOp("w1:p2", "send", SendArgs{Kind: store.KindDone, Body: title}, nil)
		x.status("w1:p2", "idle")
		if x.task(task.ID).Status != store.TaskCompleted {
			t.Fatal("task did not complete")
		}
	}
	w, err := x.st.GetWorker("w1:p2")
	if err != nil || w.State != store.WorkerLive || x.h.promptCount(w.PaneID) != 2 {
		t.Fatalf("worker changed between tasks: %+v %v", w, err)
	}
}
