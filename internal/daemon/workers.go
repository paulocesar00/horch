package daemon

import (
	"context"
	"encoding/json"
	"github.com/ellingtonsp/herdr-orch/internal/store"
	"path/filepath"
)

type WorkerRegisterArgs struct {
	Run      string `json:"run,omitempty"`
	Pane     string `json:"pane"`
	Name     string `json:"name,omitempty"`
	Worktree string `json:"worktree,omitempty"`
}

// opWorkerRegister adopts an existing agent pane as a worker of the run.
func (e *Engine) opWorkerRegister(ctx context.Context, caller string, raw json.RawMessage) (any, error) {
	a, err := decode[WorkerRegisterArgs](raw)
	if err != nil {
		return nil, err
	}
	if a.Pane == "" || a.Pane == "me" {
		a.Pane = caller
	}
	run, err := e.runFor(caller, a.Run)
	if err != nil {
		return nil, err
	}
	p, err := e.h.PaneGet(ctx, a.Pane)
	if err != nil {
		return nil, refusal("unknown_pane", "pane %s: %v", a.Pane, err)
	}
	agent := ""
	if p.Agent != nil {
		agent = *p.Agent
	}
	if a.Worktree == "" && p.Cwd != nil {
		a.Worktree = *p.Cwd
	}
	if a.Worktree != "" {
		a.Worktree, err = filepath.Abs(a.Worktree)
		if err != nil {
			return nil, refusal("bad_cwd", "%v", err)
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	w, err := e.st.RegisterWorker(store.Worker{PaneID: a.Pane, RunID: run.ID, Name: a.Name, Agent: agent, Worktree: a.Worktree})
	if err == nil {
		e.status[a.Pane] = p.AgentStatus
		e.recomputeWatch()
		e.notify.broadcast()
	}
	return w, err
}

type WorkerRef struct {
	Worker string `json:"worker"`
	Lines  int    `json:"lines,omitempty"`
}

func (e *Engine) findWorker(caller, ref string) (store.Worker, error) {
	if ref == "" || ref == "me" {
		ref = caller
	}
	return e.st.FindWorker(ref)
}

func (e *Engine) opWorkerShow(ctx context.Context, caller string, raw json.RawMessage) (any, error) {
	a, err := decode[WorkerRef](raw)
	if err != nil {
		return nil, err
	}
	w, err := e.findWorker(caller, a.Worker)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"worker": w}
	if d, err := e.st.ActiveDispatchForPane(w.PaneID); err == nil {
		out["dispatch"] = d
	}
	if p, err := e.h.PaneGet(ctx, w.PaneID); err == nil {
		out["live_status"] = p.AgentStatus
	} else {
		out["live_status"] = "gone"
	}
	return out, nil
}

func (e *Engine) opWorkerRead(ctx context.Context, caller string, raw json.RawMessage) (any, error) {
	a, err := decode[WorkerRef](raw)
	if err != nil {
		return nil, err
	}
	pane := a.Worker
	if w, err := e.findWorker(caller, a.Worker); err == nil {
		pane = w.PaneID
	} else if !isPane(pane) {
		return nil, err
	}
	if a.Lines <= 0 {
		a.Lines = 120
	}
	text, err := e.h.PaneRead(ctx, pane, a.Lines)
	if err != nil {
		return nil, err
	}
	return map[string]any{"pane_id": pane, "text": text}, nil
}

type WorkerListArgs struct {
	Run string `json:"run,omitempty"`
	All bool   `json:"all,omitempty"`
}

func (e *Engine) opWorkerList(_ context.Context, _ string, raw json.RawMessage) (any, error) {
	a, err := decode[WorkerListArgs](raw)
	if err != nil {
		return nil, err
	}
	return e.st.ListWorkers(a.Run, !a.All)
}
