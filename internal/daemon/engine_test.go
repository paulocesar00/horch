package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellingtonsp/herdr-orch/internal/herdr"
	"github.com/ellingtonsp/herdr-orch/internal/store"
)

// fakeHerdr records calls and serves canned pane state.
type fakeHerdr struct {
	mu       sync.Mutex
	panes    map[string]*herdr.Pane
	prompts  map[string][]string
	typed    map[string][]string
	notes    []string
	named    map[string]bool // panes herdr manages (agent.prompt works)
	notReady map[string]int  // named panes that refuse prompts this many more times
}

func newFake() *fakeHerdr {
	return &fakeHerdr{panes: map[string]*herdr.Pane{}, prompts: map[string][]string{}, typed: map[string][]string{}, named: map[string]bool{}, notReady: map[string]int{}}
}

func (f *fakeHerdr) addPane(id, agent, status string, named bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := agent
	f.panes[id] = &herdr.Pane{PaneID: id, WorkspaceID: "w1", Agent: &a, AgentStatus: status}
	if named {
		n := "agent-" + strings.ReplaceAll(id, ":", "-")
		f.panes[id].Name = &n
	}
	f.named[id] = named
}

func (f *fakeHerdr) AgentPrompt(_ context.Context, target, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.panes[target]; !ok {
		return &herdr.Error{Code: "agent_not_found", Message: "no agent " + target}
	}
	if !f.named[target] || f.notReady[target] > 0 {
		if f.notReady[target] > 0 {
			f.notReady[target]--
		}
		return &herdr.Error{Code: "agent_not_ready", Message: "agent " + target + " is not an active named agent"}
	}
	f.prompts[target] = append(f.prompts[target], text)
	return nil
}

func (f *fakeHerdr) SendText(_ context.Context, pane, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.typed[pane] = append(f.typed[pane], text)
	return nil
}

func (f *fakeHerdr) PaneGet(_ context.Context, id string) (herdr.Pane, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.panes[id]
	if !ok {
		return herdr.Pane{}, &herdr.Error{Code: "pane_not_found", Message: id}
	}
	return *p, nil
}

func (f *fakeHerdr) AgentGet(ctx context.Context, target string) (herdr.Pane, error) {
	return f.PaneGet(ctx, target)
}

func (f *fakeHerdr) Notify(_ context.Context, title, body string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = append(f.notes, title)
	return nil
}

func (f *fakeHerdr) PaneTokens(context.Context, string, map[string]*string) error { return nil }

func (f *fakeHerdr) PaneRead(context.Context, string, int) (string, error) { return "transcript", nil }

func (f *fakeHerdr) promptCount(p string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.prompts[p]) + len(f.typed[p])
}

type harness struct {
	t   *testing.T
	e   *Engine
	h   *fakeHerdr
	st  *store.Store
	now time.Time
	q   []func()
	qmu sync.Mutex
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "orch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	x := &harness{t: t, h: newFake(), st: st, now: time.Unix(1_000_000, 0)}
	st.Now = func() time.Time { return x.now }
	cfg := DefaultConfig()
	x.e = NewEngine(st, x.h, cfg)
	x.e.logf = t.Logf
	x.e.deliveryLaunch = func(func()) {} // Delivery scenarios run explicitly in delivery_test.go.
	t.Cleanup(x.e.CloseDeliveries)
	x.e.promptRetry = time.Millisecond
	x.e.async = func(f func()) { x.qmu.Lock(); x.q = append(x.q, f); x.qmu.Unlock() }
	return x
}

// drain runs queued async work (notifications, explicit dispatch) until none is left.
func (x *harness) drain() {
	for {
		x.qmu.Lock()
		q := x.q
		x.q = nil
		x.qmu.Unlock()
		if len(q) == 0 {
			return
		}
		for _, f := range q {
			f()
		}
	}
}

func (x *harness) op(caller, op string, args any) (json.RawMessage, error) {
	x.t.Helper()
	b, _ := json.Marshal(args)
	out, err := x.e.Ops()[op](context.Background(), caller, b)
	x.drain()
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(out)
	return raw, nil
}

func (x *harness) mustOp(caller, op string, args any, out any) {
	x.t.Helper()
	raw, err := x.op(caller, op, args)
	if err != nil {
		x.t.Fatalf("%s: %v", op, err)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			x.t.Fatal(err)
		}
	}
}

func (x *harness) status(pane, s string) {
	x.h.mu.Lock()
	if p, ok := x.h.panes[pane]; ok {
		p.AgentStatus = s
	}
	x.h.mu.Unlock()
	x.e.HandleEvent(herdr.Event{Name: "pane.agent_status_changed", Data: mustJSON(map[string]string{"pane_id": pane, "agent_status": s})})
	x.drain()
}

func (x *harness) advance(d time.Duration) {
	x.now = x.now.Add(d)
	x.e.Tick()
	x.drain()
}

func (x *harness) task(id string) store.Task {
	t, err := x.st.GetTask(id)
	if err != nil {
		x.t.Fatal(err)
	}
	return t
}

func (x *harness) inbox(pane string) []store.Message {
	ms, err := x.st.Unread([]string{pane}, true)
	if err != nil {
		x.t.Fatal(err)
	}
	return ms
}

func wantRefusal(t *testing.T, err error, code string) {
	t.Helper()
	r, ok := AsRefusal(err)
	if !ok || r.Code != code {
		t.Fatalf("want refusal %s, got %v", code, err)
	}
}

const coord = "w1:p1"

func setupRun(x *harness, workers ...string) store.Run {
	var r store.Run
	x.mustOp(coord, "run.create", RunCreateArgs{Title: "test"}, &r)
	for _, w := range workers {
		x.h.addPane(w, "pi", "idle", true)
		x.mustOp(coord, "worker.register", WorkerRegisterArgs{Pane: w}, nil)
	}
	return r
}

func TestDispatchSettlesOnDonePlusIdle(t *testing.T) {
	x := newHarness(t)
	setupRun(x, "w1:p2")
	var task store.Task
	x.mustOp(coord, "task.create", TaskCreateArgs{Title: "A", Spec: "do A"}, &task)
	var d store.Dispatch
	x.mustOp(coord, "dispatch", DispatchArgs{Task: task.ID, To: "w1:p2"}, &d)
	if d.Status != store.DispatchDispatched || x.h.promptCount("w1:p2") != 1 {
		t.Fatalf("dispatch: %+v prompts=%d", d, x.h.promptCount("w1:p2"))
	}
	if p := x.h.prompts["w1:p2"][0]; !strings.Contains(p, "horch done --task "+task.ID) || !strings.Contains(p, "do A") {
		t.Fatalf("prompt preamble missing: %s", p)
	}
	x.status("w1:p2", "working")
	// Worker reports done while still working: not settled yet.
	x.mustOp("w1:p2", "send", SendArgs{Kind: store.KindDone, Body: "did A"}, nil)
	if got := x.task(task.ID).Status; got != store.TaskDispatched {
		t.Fatalf("settled while working: %s", got)
	}
	x.status("w1:p2", "idle")
	tk := x.task(task.ID)
	if tk.Status != store.TaskCompleted || tk.Result != "did A" {
		t.Fatalf("task: %+v", tk)
	}
	ms := x.inbox(coord)
	if len(ms) != 1 || ms[0].Kind != store.KindDone || !strings.Contains(ms[0].Body, "did A") {
		t.Fatalf("coordinator inbox: %+v", ms)
	}
}

func TestIdleWithoutReportEscalatesNeverFails(t *testing.T) {
	x := newHarness(t)
	setupRun(x, "w1:p2")
	var task store.Task
	x.mustOp(coord, "task.create", TaskCreateArgs{Title: "A"}, &task)
	x.mustOp(coord, "dispatch", DispatchArgs{Task: task.ID, To: "w1:p2"}, nil)
	x.status("w1:p2", "working")
	x.status("w1:p2", "idle")
	x.advance(2 * time.Minute)
	if len(x.inbox(coord)) != 0 {
		t.Fatal("escalated before the idle-report delay")
	}
	x.advance(90 * time.Second)
	ms := x.inbox(coord)
	if len(ms) != 1 || ms[0].Kind != store.KindEscalation || !strings.Contains(ms[0].Subject, "without reporting") ||
		!strings.Contains(ms[0].Body, "transcript") {
		t.Fatalf("want one idle escalation with the screen tail: %+v", ms)
	}
	if len(x.h.notes) != 0 {
		t.Fatal("idle report is for the coordinator, not a human notification")
	}
	// Hours idle: still dispatched, no repeat for the same idle episode.
	x.advance(3 * time.Hour)
	if x.task(task.ID).Status != store.TaskDispatched || len(x.inbox(coord)) != 0 {
		t.Fatal("silence must never fail or re-dispatch the task")
	}
	// A new working → idle episode (next loop iteration) can be reported again.
	x.status("w1:p2", "working")
	x.status("w1:p2", "idle")
	x.advance(4 * time.Minute)
	if len(x.inbox(coord)) != 1 {
		t.Fatal("new idle episode should be reported")
	}
	// Late report still settles normally.
	x.mustOp("w1:p2", "send", SendArgs{Kind: store.KindDone, Body: "loop finished"}, nil)
	if tk := x.task(task.ID); tk.Status != store.TaskCompleted || tk.Result != "loop finished" {
		t.Fatalf("late report: %+v", tk)
	}
}

func TestPerRunIdleReportDelay(t *testing.T) {
	x := newHarness(t)
	setupRun(x, "w1:p2")
	ms := int64((45 * time.Minute).Milliseconds())
	x.mustOp(coord, "run.update", RunUpdateArgs{IdleReportMS: &ms}, nil)
	var task store.Task
	x.mustOp(coord, "task.create", TaskCreateArgs{Title: "A"}, &task)
	x.mustOp(coord, "dispatch", DispatchArgs{Task: task.ID, To: "w1:p2"}, nil)
	x.status("w1:p2", "working")
	x.status("w1:p2", "idle")
	x.advance(30 * time.Minute)
	if len(x.inbox(coord)) != 0 {
		t.Fatal("reported before the run's 45m delay")
	}
	x.advance(16 * time.Minute)
	if len(x.inbox(coord)) != 1 {
		t.Fatal("not reported after the run's delay")
	}
}

func TestNudgeAndCoordinatorFail(t *testing.T) {
	x := newHarness(t)
	setupRun(x, "w1:p2")
	var task store.Task
	x.mustOp(coord, "task.create", TaskCreateArgs{Title: "A"}, &task)
	x.mustOp(coord, "dispatch", DispatchArgs{Task: task.ID, To: "w1:p2"}, nil)
	x.status("w1:p2", "working")
	_, err := x.op(coord, "dispatch.nudge", DispatchActArgs{Task: task.ID})
	wantRefusal(t, err, "worker_busy")
	x.status("w1:p2", "idle")
	var r struct {
		Nudges int `json:"nudges"`
	}
	x.mustOp(coord, "dispatch.nudge", DispatchActArgs{Task: task.ID}, &r)
	if r.Nudges != 1 || x.h.promptCount("w1:p2") != 2 {
		t.Fatalf("nudge: %+v prompts=%d", r, x.h.promptCount("w1:p2"))
	}
	if p := x.h.prompts["w1:p2"][1]; !strings.Contains(p, "done --task "+task.ID) || strings.Contains(p, "task "+task.ID+":") {
		t.Fatalf("nudge text: %s", p)
	}
	_, err = x.op(coord, "dispatch.fail", DispatchActArgs{Task: task.ID})
	wantRefusal(t, err, "bad_args")
	var st store.Settlement
	x.mustOp(coord, "dispatch.fail", DispatchActArgs{Task: task.ID, Reason: "nudged twice, no report"}, &st)
	if st.Dispatch.Status != store.DispatchFailed || st.Task.Status != store.TaskReady || st.Task.Attempts != 1 {
		t.Fatalf("fail: %+v", st)
	}
	if !strings.Contains(st.Dispatch.Outcome, "nudged twice") {
		t.Fatalf("outcome %q", st.Dispatch.Outcome)
	}
	_, err = x.op(coord, "dispatch.nudge", DispatchActArgs{Task: task.ID})
	wantRefusal(t, err, "no_active_dispatch")
}

func TestIdleBeforeWorkingDoesNotSettle(t *testing.T) {
	x := newHarness(t)
	setupRun(x, "w1:p2")
	var task store.Task
	x.mustOp(coord, "task.create", TaskCreateArgs{Title: "A"}, &task)
	x.mustOp(coord, "dispatch", DispatchArgs{Task: task.ID, To: "w1:p2"}, nil)
	x.status("w1:p2", "done") // stale completion from a previous turn
	x.advance(time.Minute)
	if x.task(task.ID).Status != store.TaskDispatched {
		t.Fatal("idle without observed work must not settle")
	}
	x.advance(time.Minute) // past UnobservedAfter
	ms := x.inbox(coord)
	if len(ms) != 1 || !strings.Contains(ms[0].Subject, "no activity") {
		t.Fatalf("want unobserved escalation, got %+v", ms)
	}
	x.advance(time.Minute)
	if len(x.inbox(coord)) != 0 {
		t.Fatal("unobserved escalation must fire once")
	}
}

func TestBlockedEscalatesOnlyWhenItPersists(t *testing.T) {
	x := newHarness(t)
	setupRun(x, "w1:p2")
	var task store.Task
	x.mustOp(coord, "task.create", TaskCreateArgs{Title: "A"}, &task)
	x.mustOp(coord, "dispatch", DispatchArgs{Task: task.ID, To: "w1:p2"}, nil)
	x.status("w1:p2", "working")
	// A blip: blocked for a few seconds, then cleared (auto mode answered it).
	x.status("w1:p2", "blocked")
	x.advance(10 * time.Second)
	x.status("w1:p2", "working")
	x.advance(30 * time.Second)
	if ms := x.inbox(coord); len(ms) != 0 || len(x.h.notes) != 0 {
		t.Fatalf("a cleared blip must not escalate: %+v", ms)
	}
	// Stuck: escalates once after BlockedAfter.
	x.status("w1:p2", "blocked")
	x.advance(10 * time.Second)
	if len(x.inbox(coord)) != 0 {
		t.Fatal("escalated before BlockedAfter")
	}
	x.advance(15 * time.Second)
	ms := x.inbox(coord)
	if len(ms) != 1 || ms[0].Kind != store.KindEscalation || !strings.Contains(ms[0].Subject, "blocked") {
		t.Fatalf("want one blocked escalation: %+v", ms)
	}
	x.advance(time.Minute)
	if len(x.inbox(coord)) != 0 {
		t.Fatal("duplicate blocked escalation")
	}
}

func TestPaneExitFailsDispatch(t *testing.T) {
	x := newHarness(t)
	setupRun(x, "w1:p2")
	var task store.Task
	x.mustOp(coord, "task.create", TaskCreateArgs{Title: "A"}, &task)
	x.mustOp(coord, "dispatch", DispatchArgs{Task: task.ID, To: "w1:p2"}, nil)
	x.e.HandleEvent(herdr.Event{Name: "pane.exited", Data: mustJSON(map[string]string{"pane_id": "w1:p2"})})
	x.drain()
	ds, _ := x.st.DispatchesForTask(task.ID)
	if ds[0].Status != store.DispatchFailed || ds[0].Outcome != "failed: pane_exited" {
		t.Fatalf("dispatch: %+v", ds[0])
	}
	w, _ := x.st.GetWorker("w1:p2")
	if w.State != store.WorkerExited {
		t.Fatalf("worker state %s", w.State)
	}
}

func TestDoneFailedAndCircuitBreaker(t *testing.T) {
	x := newHarness(t)
	setupRun(x, "w1:p2")
	max := 2
	x.mustOp(coord, "run.update", RunUpdateArgs{MaxAttempts: &max}, nil)
	var task store.Task
	x.mustOp(coord, "task.create", TaskCreateArgs{Title: "A"}, &task)
	for i := 0; i < 2; i++ {
		x.mustOp(coord, "dispatch", DispatchArgs{Task: task.ID, To: "w1:p2"}, nil)
		x.status("w1:p2", "working")
		x.mustOp("w1:p2", "send", SendArgs{Kind: store.KindDone, Failed: true, Body: "tests fail"}, nil)
		x.status("w1:p2", "idle")
	}
	ds, _ := x.st.DispatchesForTask(task.ID)
	if ds[0].Status != store.DispatchFailed || ds[1].Status != store.DispatchCircuitBroken {
		t.Fatalf("dispatches: %s %s", ds[0].Status, ds[1].Status)
	}
	if x.task(task.ID).Status != store.TaskFailed {
		t.Fatal("task should be failed")
	}
	_, err := x.op(coord, "dispatch", DispatchArgs{Task: task.ID, To: "w1:p2"})
	wantRefusal(t, err, "task_not_ready")
	ms := x.inbox(coord)
	if last := ms[len(ms)-1]; !strings.Contains(last.Subject, "circuit broken") {
		t.Fatalf("last message %+v", last)
	}
}

func TestDoneRefusals(t *testing.T) {
	x := newHarness(t)
	setupRun(x, "w1:p2", "w1:p3")
	_, err := x.op("w1:p2", "send", SendArgs{Kind: store.KindDone, Body: "?"})
	wantRefusal(t, err, "no_active_dispatch")
	var task store.Task
	x.mustOp(coord, "task.create", TaskCreateArgs{Title: "A"}, &task)
	x.mustOp(coord, "dispatch", DispatchArgs{Task: task.ID, To: "w1:p2"}, nil)
	x.status("w1:p2", "working")
	_, err = x.op("w1:p3", "send", SendArgs{Kind: store.KindDone, Task: task.ID})
	wantRefusal(t, err, "not_assignee")
	x.mustOp("w1:p2", "send", SendArgs{Kind: store.KindDone, Task: task.ID}, nil)
	_, err = x.op("w1:p2", "send", SendArgs{Kind: store.KindDone, Task: task.ID})
	wantRefusal(t, err, "already_done")
}

func TestDispatchFallsBackToTypedText(t *testing.T) {
	x := newHarness(t)
	setupRun(x)
	x.h.addPane("w1:p5", "fake", "idle", false)
	var task store.Task
	x.mustOp(coord, "task.create", TaskCreateArgs{Title: "A", Spec: "line one\nline two"}, &task)
	x.mustOp(coord, "dispatch", DispatchArgs{Task: task.ID, To: "w1:p5"}, nil)
	if len(x.h.typed["w1:p5"]) != 1 || strings.Contains(x.h.typed["w1:p5"][0], "\n") {
		t.Fatalf("typed: %q", x.h.typed["w1:p5"])
	}
	d, _ := x.st.ActiveDispatchForPane("w1:p5")
	if !strings.HasPrefix(d.Outcome, "sent via pane.send_text") {
		t.Fatalf("outcome %q", d.Outcome)
	}
}

func TestDispatchRefusesBusyAgent(t *testing.T) {
	x := newHarness(t)
	setupRun(x, "w1:p2")
	x.h.panes["w1:p2"].AgentStatus = "working"
	var task store.Task
	x.mustOp(coord, "task.create", TaskCreateArgs{Title: "A"}, &task)
	_, err := x.op(coord, "dispatch", DispatchArgs{Task: task.ID, To: "w1:p2"})
	wantRefusal(t, err, "worker_busy")
	if x.task(task.ID).Status != store.TaskReady || x.task(task.ID).Attempts != 0 {
		t.Fatal("refused dispatch must not consume the task")
	}
}

func TestDAGAndExplicitDispatchNext(t *testing.T) {
	// A → B and A → C share two persistent workers.
	x := newHarness(t)
	setupRun(x, "w1:p2", "w1:p3")
	var a, b, c store.Task
	x.mustOp(coord, "task.create", TaskCreateArgs{Title: "A"}, &a)
	x.mustOp(coord, "task.create", TaskCreateArgs{Title: "B", Deps: []string{a.ID}}, &b)
	x.mustOp(coord, "task.create", TaskCreateArgs{Title: "C", Deps: []string{a.ID}}, &c)
	x.mustOp(coord, "dispatch.next", RunRef{}, nil)
	if x.task(a.ID).Status != store.TaskDispatched {
		t.Fatalf("explicit dispatch should have sent A: %s", x.task(a.ID).Status)
	}
	worker := x.task(a.ID).AssigneePaneID
	x.status(worker, "working")
	x.mustOp(worker, "send", SendArgs{Kind: store.KindDone, Body: "A ok"}, nil)
	x.status(worker, "idle")
	x.mustOp(coord, "dispatch.next", RunRef{}, nil)
	if x.task(b.ID).Status != store.TaskDispatched {
		t.Fatalf("B should explicit dispatch after A: %s", x.task(b.ID).Status)
	}
	if x.task(c.ID).Status != store.TaskDispatched {
		t.Fatalf("C should explicit dispatch after A: %s", x.task(c.ID).Status)
	}
	for _, id := range []string{b.ID, c.ID} {
		w := x.task(id).AssigneePaneID
		x.status(w, "working")
		x.mustOp(w, "send", SendArgs{Kind: store.KindDone, Body: id + " ok"}, nil)
		x.status(w, "idle")
	}
	for _, id := range []string{a.ID, b.ID, c.ID} {
		if s := x.task(id).Status; s != store.TaskCompleted {
			t.Fatalf("%s is %s", id, s)
		}
	}
}

func TestAskReply(t *testing.T) {
	x := newHarness(t)
	setupRun(x, "w1:p2")
	done := make(chan json.RawMessage, 1)
	go func() {
		raw, err := x.op("w1:p2", "ask", AskArgs{Question: "postgres or sqlite?", TimeoutMS: 5000})
		if err != nil {
			t.Error(err)
		}
		done <- raw
	}()
	var q []store.Message
	deadline := time.Now().Add(3 * time.Second)
	for len(q) == 0 && time.Now().Before(deadline) {
		q, _ = x.st.Unread([]string{coord}, false)
		time.Sleep(10 * time.Millisecond)
	}
	if len(q) != 1 || q[0].Kind != store.KindQuestion {
		t.Fatalf("question not delivered: %+v", q)
	}
	x.mustOp(coord, "reply", ReplyArgs{ID: q[0].ID, Body: "sqlite"}, nil)
	select {
	case raw := <-done:
		if !strings.Contains(string(raw), "sqlite") {
			t.Fatalf("ask result: %s", raw)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ask did not return")
	}
}

func TestCheckWaitWakesOnMessage(t *testing.T) {
	x := newHarness(t)
	setupRun(x, "w1:p2")
	got := make(chan json.RawMessage, 1)
	go func() {
		raw, _ := x.op(coord, "check", CheckArgs{Wait: true, TimeoutMS: 5000})
		got <- raw
	}()
	time.Sleep(50 * time.Millisecond)
	x.mustOp("w1:p2", "send", SendArgs{To: "coordinator", Body: "hello"}, nil)
	select {
	case raw := <-got:
		if !strings.Contains(string(raw), "hello") {
			t.Fatalf("check: %s", raw)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("check --wait did not wake")
	}
}

func TestSendToRunBroadcasts(t *testing.T) {
	x := newHarness(t)
	setupRun(x, "w1:p2", "w1:p3")
	var ms []store.Message
	x.mustOp(coord, "send", SendArgs{To: "run", Body: "standup"}, &ms)
	if len(ms) != 2 {
		t.Fatalf("broadcast: %+v", ms)
	}
}

func TestDispatchRetriesNamedAgentNotYetReady(t *testing.T) {
	x := newHarness(t)
	setupRun(x, "w1:p2")
	x.h.notReady["w1:p2"] = 3
	var task store.Task
	x.mustOp(coord, "task.create", TaskCreateArgs{Title: "A"}, &task)
	var d store.Dispatch
	x.mustOp(coord, "dispatch", DispatchArgs{Task: task.ID, To: "w1:p2"}, &d)
	if d.Outcome != "sent via agent.prompt (after 3 retries)" || len(x.h.typed["w1:p2"]) != 0 {
		t.Fatalf("outcome %q typed=%v", d.Outcome, x.h.typed["w1:p2"])
	}
}

func TestIdleEscalationWakesCheckWait(t *testing.T) {
	x := newHarness(t)
	setupRun(x, "w1:p2")
	var task store.Task
	x.mustOp(coord, "task.create", TaskCreateArgs{Title: "A"}, &task)
	x.mustOp(coord, "dispatch", DispatchArgs{Task: task.ID, To: "w1:p2"}, nil)
	x.status("w1:p2", "working")
	x.status("w1:p2", "idle")
	got := make(chan json.RawMessage, 1)
	go func() {
		raw, _ := x.op(coord, "check", CheckArgs{Wait: true, TimeoutMS: 5000})
		got <- raw
	}()
	time.Sleep(50 * time.Millisecond)
	x.now = x.now.Add(4 * time.Minute)
	x.e.Tick()
	select {
	case raw := <-got:
		if !strings.Contains(string(raw), "without reporting") {
			t.Fatalf("check: %s", raw)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("idle escalation did not wake check --wait")
	}
}
