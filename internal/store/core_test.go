package store

import "testing"

func TestLegacyGatesAndLifecycleDoNotBlockCoreDispatch(t *testing.T) {
	s := open(t)
	r := must[Run](t)(s.CreateRun("core", "coord"))
	task := must[Task](t)(s.CreateTask(r.ID, "work", "", nil))
	must[Worker](t)(s.RegisterWorker(Worker{PaneID: "w1:p2", RunID: r.ID, Name: "persistent"}))
	for _, q := range []string{
		`UPDATE workers SET lifecycle='restart_failed',retained=1 WHERE pane_id='w1:p2'`,
		`INSERT INTO gates(id,run_id,task_id,question,status,created_at) VALUES('g1','r1','t1','old question','pending',1)`,
		`UPDATE tasks SET status='blocked' WHERE id='t1'`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	task, _, err := s.UpdateTask(task.ID, TaskUpdate{Status: ptr(TaskReady)})
	if err != nil || task.Status != TaskReady {
		t.Fatalf("legacy gate blocked readiness: %+v %v", task, err)
	}
	d := must[Dispatch](t)(s.BeginDispatch(task.ID, "w1:p2", "codex", "hash"))
	if d.Status != DispatchPending {
		t.Fatal(d)
	}
	must[Worker](t)(s.RegisterWorker(Worker{PaneID: "w1:p2", RunID: r.ID, Name: "persistent"}))
	var phase string
	var retained bool
	if err := s.db.QueryRow(`SELECT lifecycle,retained FROM workers WHERE pane_id='w1:p2'`).Scan(&phase, &retained); err != nil || phase != "restart_failed" || !retained {
		t.Fatalf("legacy data overwritten: %q %v %v", phase, retained, err)
	}
}
