// horch is the herdr-orch command line: agents and humans use it to talk to the daemon.
// Every command takes --json and exits non-zero on refusal.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ellingtonsp/herdr-orch/internal/client"
	"github.com/ellingtonsp/herdr-orch/internal/daemon"
	"github.com/ellingtonsp/herdr-orch/internal/rpc"
	"github.com/ellingtonsp/herdr-orch/internal/store"
	"github.com/ellingtonsp/herdr-orch/skills"
)

const usage = `horch — persistent pane orchestration (mailbox, tasks, dispatch, workers)

  horch run create --title T [--max-attempts N]
  horch run use <run> | current | list | show [--run R]
  horch run update [--run R] [--status S] [--coordinator me|PANE]
                   [--max-attempts N] [--idle-report 10m]

  horch send --to coordinator|run|human|PANE|NAME [--subject S] --body B
             [--kind note|question|escalation] [--wait-pickup] [--timeout 5m]
  horch done [--task T] --body B [--failed]
  horch check [--wait] [--timeout 10m] [--peek]
  horch inbox [--all] [--limit N]
  horch ask [--to coordinator] --question Q [--timeout 30m]
  horch reply --id MSG --body B
  horch ack --id MSG

  horch task create --title T [--spec S | --spec-file F] [--deps a,b]
  horch task list | show --id T | update --id T [--status S] [--deps a,b] [--reset-attempts] ...
  horch dispatch --task T --to PANE|NAME
  horch dispatch --spec S --to PANE|NAME
  horch dispatch show --task T | --id D
  horch dispatch next [--run R]
  horch dispatch nudge --task T [--text S] [--force]
  horch dispatch fail --task T --reason R [--infra]

  horch worker register [--pane PANE] [--name N] [--run R] [--worktree DIR]
  horch worker show|read --worker PANE|NAME [--lines N]
  horch worker list [--all]

  horch status
  horch daemon stop
  horch config
  horch guide
  horch version

Global: --json (machine output), --as PANE (default $HERDR_PANE_ID).
`

var (
	jsonOut bool
	cli     *client.Client
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(usage)
		return
	}
	if args[0] == "version" || args[0] == "--version" {
		fmt.Println(daemon.Version)
		return
	}
	if args[0] == "guide" {
		fmt.Print(skills.Horch)
		return
	}
	var err error
	cli, err = client.New()
	if err != nil {
		fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := dispatch(ctx, args); err != nil {
		fail(err)
	}
}

func fail(err error) {
	var re *rpc.Error
	code, msg := "error", err.Error()
	if errors.As(err, &re) {
		code, msg = re.Code, re.Message
	}
	if jsonOut {
		b, _ := json.Marshal(map[string]any{"ok": false, "error": map[string]string{"code": code, "message": msg}})
		fmt.Println(string(b))
	} else {
		fmt.Fprintf(os.Stderr, "horch: %s: %s\n", code, msg)
	}
	if code == "usage" {
		os.Exit(2)
	}
	os.Exit(1)
}

func usageErr(format string, a ...any) error {
	return &rpc.Error{Code: "usage", Message: fmt.Sprintf(format, a...)}
}

// fs builds a flag set with the global flags.
func fs(name string) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.BoolVar(&jsonOut, "json", false, "")
	f.Func("as", "", func(s string) error { cli.Caller = s; return nil })
	return f
}

// parse parses flags, allowing one leading positional argument (e.g. `run use r1`).
func parse(f *flag.FlagSet, args []string) (string, error) {
	pos := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		pos, args = args[0], args[1:]
	}
	if err := f.Parse(args); err != nil {
		return "", usageErr("%s: %v", f.Name(), err)
	}
	if pos == "" && f.NArg() > 0 {
		pos = f.Arg(0)
	}
	return pos, nil
}

func call(ctx context.Context, op string, args any) (json.RawMessage, error) {
	var out json.RawMessage
	err := cli.Call(ctx, op, args, &out)
	return out, err
}

func emit(raw json.RawMessage, human func(json.RawMessage)) {
	if jsonOut {
		fmt.Println(string(raw))
		return
	}
	human(raw)
}

func run(ctx context.Context, op string, args any, human func(json.RawMessage)) error {
	raw, err := call(ctx, op, args)
	if err != nil {
		return err
	}
	if human == nil {
		human = printJSON
	}
	emit(raw, human)
	return nil
}

func dispatch(ctx context.Context, args []string) error {
	cmd, rest := args[0], args[1:]
	sub := ""
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		sub = rest[0]
	}
	switch cmd {
	case "run":
		return cmdRun(ctx, sub, tail(rest))
	case "send":
		return cmdSend(ctx, rest)
	case "done":
		return cmdDone(ctx, rest)
	case "check":
		return cmdCheck(ctx, rest)
	case "ack":
		return cmdAck(ctx, rest)
	case "ask":
		return cmdAsk(ctx, rest)
	case "reply":
		return cmdReply(ctx, rest)
	case "inbox":
		return cmdInbox(ctx, rest)
	case "task":
		return cmdTask(ctx, sub, tail(rest))
	case "dispatch":
		if sub == "show" || sub == "next" || sub == "nudge" || sub == "fail" {
			return cmdDispatchSub(ctx, sub, tail(rest))
		}
		if sub != "" {
			return usageErr("unknown dispatch subcommand %q", sub)
		}
		return cmdDispatch(ctx, rest)
	case "worker":
		return cmdWorker(ctx, sub, tail(rest))
	case "config":
		return cmdConfig(rest)
	case "status":
		f := fs("status")
		if _, err := parse(f, rest); err != nil {
			return err
		}
		return run(ctx, "ping", nil, nil)
	case "daemon":
		f := fs("daemon")
		if _, err := parse(f, tail(rest)); err != nil {
			return err
		}
		if sub != "stop" {
			return usageErr("usage: horch daemon stop")
		}
		err := rpc.Call(ctx, cli.Paths.Sock, "shutdown", cli.Caller, nil, nil)
		if errors.Is(err, rpc.ErrUnavailable) {
			fmt.Println("daemon not running")
			return nil
		}
		if err == nil {
			fmt.Println("daemon stopping")
		}
		return err
	}
	return usageErr("unknown command %q (horch --help)", cmd)
}

func tail(a []string) []string {
	if len(a) > 0 && !strings.HasPrefix(a[0], "-") {
		return a[1:]
	}
	return a
}

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func durMS(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, usageErr("bad duration %q: %v", s, err)
	}
	return d.Milliseconds(), nil
}

func strPtr(f *flag.FlagSet, name string, v string) *string {
	set := false
	f.Visit(func(fl *flag.Flag) {
		if fl.Name == name {
			set = true
		}
	})
	if !set {
		return nil
	}
	return &v
}

// ---------- run ----------

func cmdRun(ctx context.Context, sub string, args []string) error {
	f := fs("run " + sub)
	runID := f.String("run", "", "")
	title := f.String("title", "", "")
	maxA := f.Int("max-attempts", 0, "")
	status := f.String("status", "", "")
	coord := f.String("coordinator", "", "")
	idleReport := f.String("idle-report", "", "")
	pos, err := parse(f, args)
	if err != nil {
		return err
	}
	if pos != "" && *runID == "" {
		*runID = pos
	}
	switch sub {
	case "create":
		if *title == "" {
			*title = pos
		}
		return run(ctx, "run.create", daemon.RunCreateArgs{Title: *title, MaxAttempts: *maxA, Coordinator: *coord}, printRun)
	case "use":
		return run(ctx, "run.use", daemon.RunRef{Run: *runID}, printRun)
	case "current":
		return run(ctx, "run.current", nil, printRun)
	case "list":
		return run(ctx, "run.list", nil, printRuns)
	case "show", "":
		return run(ctx, "run.show", daemon.RunRef{Run: *runID}, printRunShow)
	case "update":
		a := daemon.RunUpdateArgs{Run: *runID, Status: strPtr(f, "status", *status), Coordinator: strPtr(f, "coordinator", *coord), Title: strPtr(f, "title", *title)}
		if *maxA > 0 {
			a.MaxAttempts = maxA
		}
		if *idleReport != "" {
			ms, err := durMS(*idleReport)
			if err != nil {
				return err
			}
			a.IdleReportMS = &ms
		}
		return run(ctx, "run.update", a, printRun)
	}
	return usageErr("unknown run subcommand %q", sub)
}

// ---------- messages ----------

func cmdSend(ctx context.Context, args []string) error {
	f := fs("send")
	a := daemon.SendArgs{}
	f.BoolVar(&a.WaitPickup, "wait-pickup", false, "")
	timeout := f.String("timeout", "", "")
	f.StringVar(&a.Run, "run", "", "")
	f.StringVar(&a.To, "to", "", "")
	f.StringVar(&a.Subject, "subject", "", "")
	f.StringVar(&a.Body, "body", "", "")
	f.StringVar(&a.Kind, "kind", "note", "")
	f.StringVar(&a.Task, "task", "", "")
	f.BoolVar(&a.Failed, "failed", false, "")
	if _, err := parse(f, args); err != nil {
		return err
	}
	ms, err := durMS(*timeout)
	if err != nil {
		return err
	}
	a.TimeoutMS = ms
	if a.Kind == store.KindDone && a.To == "" {
		a.To = store.Daemon
	}
	if a.Body == "-" {
		b, _ := io.ReadAll(os.Stdin)
		a.Body = string(b)
	}
	return run(ctx, "send", a, printSent)
}

func cmdDone(ctx context.Context, args []string) error {
	f := fs("done")
	a := daemon.SendArgs{Kind: store.KindDone, To: store.Daemon}
	f.StringVar(&a.Task, "task", "", "")
	f.StringVar(&a.Body, "body", "", "")
	f.StringVar(&a.Subject, "subject", "", "")
	f.BoolVar(&a.Failed, "failed", false, "")
	if _, err := parse(f, args); err != nil {
		return err
	}
	if a.Body == "-" {
		b, _ := io.ReadAll(os.Stdin)
		a.Body = string(b)
	}
	return run(ctx, "send", a, func(raw json.RawMessage) {
		var r struct {
			Dispatch store.Dispatch `json:"dispatch"`
			Late     bool           `json:"late"`
		}
		_ = json.Unmarshal(raw, &r)
		if r.Late {
			fmt.Printf("recorded late note for task %s (dispatch %s remains %s)\n", r.Dispatch.TaskID, r.Dispatch.ID, r.Dispatch.Status)
			return
		}
		fmt.Printf("reported %s for task %s (dispatch %s is %s)\n", map[bool]string{true: "failure", false: "done"}[a.Failed], r.Dispatch.TaskID, r.Dispatch.ID, r.Dispatch.Status)
	})
}

func cmdCheck(ctx context.Context, args []string) error {
	f := fs("check")
	wait := f.Bool("wait", false, "")
	peek := f.Bool("peek", false, "")
	timeout := f.String("timeout", "", "")
	if _, err := parse(f, args); err != nil {
		return err
	}
	ms, err := durMS(*timeout)
	if err != nil {
		return err
	}
	return run(ctx, "check", daemon.CheckArgs{Wait: *wait, TimeoutMS: ms, Peek: *peek, Consumer: consumerLabel()}, printMessages)
}

// consumerLabel names this process in mailbox lease refusals.
func consumerLabel() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "localhost"
	}
	return fmt.Sprintf("%s:%d", host, os.Getpid())
}

func cmdAck(ctx context.Context, args []string) error {
	f := fs("ack")
	a := daemon.AckArgs{Consumer: consumerLabel()}
	f.StringVar(&a.ID, "id", "", "")
	if _, err := parse(f, args); err != nil {
		return err
	}
	return run(ctx, "ack", a, func(raw json.RawMessage) {
		var m store.Message
		_ = json.Unmarshal(raw, &m)
		fmt.Printf("acknowledged %s\n", m.ID)
	})
}

func cmdAsk(ctx context.Context, args []string) error {
	f := fs("ask")
	a := daemon.AskArgs{}
	f.StringVar(&a.Run, "run", "", "")
	f.StringVar(&a.To, "to", "coordinator", "")
	f.StringVar(&a.Question, "question", "", "")
	f.StringVar(&a.Task, "task", "", "")
	timeout := f.String("timeout", "", "")
	if _, err := parse(f, args); err != nil {
		return err
	}
	var err error
	if a.TimeoutMS, err = durMS(*timeout); err != nil {
		return err
	}
	return run(ctx, "ask", a, func(raw json.RawMessage) {
		var r struct {
			Reply store.Message `json:"reply"`
		}
		_ = json.Unmarshal(raw, &r)
		fmt.Println(r.Reply.Body)
	})
}

func cmdReply(ctx context.Context, args []string) error {
	f := fs("reply")
	a := daemon.ReplyArgs{}
	f.StringVar(&a.ID, "id", "", "")
	f.StringVar(&a.Body, "body", "", "")
	pos, err := parse(f, args)
	if err != nil {
		return err
	}
	if a.ID == "" {
		a.ID = pos
	}
	return run(ctx, "reply", a, func(raw json.RawMessage) {
		var m store.Message
		_ = json.Unmarshal(raw, &m)
		fmt.Printf("replied %s → %s\n", m.ID, m.To)
	})
}

func cmdInbox(ctx context.Context, args []string) error {
	f := fs("inbox")
	a := daemon.InboxArgs{}
	f.IntVar(&a.Limit, "limit", 30, "")
	f.BoolVar(&a.All, "all", false, "")
	f.StringVar(&a.Run, "run", "", "")
	if _, err := parse(f, args); err != nil {
		return err
	}
	return run(ctx, "inbox", a, printMessages)
}

// ---------- tasks ----------

func cmdTask(ctx context.Context, sub string, args []string) error {
	f := fs("task " + sub)
	runID := f.String("run", "", "")
	id := f.String("id", "", "")
	title := f.String("title", "", "")
	spec := f.String("spec", "", "")
	specFile := f.String("spec-file", "", "")
	deps := f.String("deps", "", "")
	status := f.String("status", "", "")
	result := f.String("result", "", "")
	reset := f.Bool("reset-attempts", false, "")
	pos, err := parse(f, args)
	if err != nil {
		return err
	}
	if *id == "" {
		*id = pos
	}
	if *specFile != "" {
		b, err := os.ReadFile(*specFile)
		if err != nil {
			return err
		}
		*spec = string(b)
	}
	switch sub {
	case "create":
		return run(ctx, "task.create", daemon.TaskCreateArgs{Run: *runID, Title: *title, Spec: *spec, Deps: splitList(*deps)}, printTask)
	case "list", "":
		return run(ctx, "task.list", daemon.RunRef{Run: *runID}, printTasks)
	case "show":
		return run(ctx, "task.show", daemon.TaskRef{ID: *id}, nil)
	case "update":
		a := daemon.TaskUpdateArgs{ID: *id, Title: strPtr(f, "title", *title), Status: strPtr(f, "status", *status), Result: strPtr(f, "result", *result), ResetAttempts: *reset}
		if s := strPtr(f, "spec", *spec); s != nil || *specFile != "" {
			a.Spec = spec
		}
		if strPtr(f, "deps", *deps) != nil {
			d := splitList(*deps)
			if d == nil {
				d = []string{}
			}
			a.Deps = &d
		}
		return run(ctx, "task.update", a, printTask)
	}
	return usageErr("unknown task subcommand %q", sub)
}

// ---------- dispatch ----------

func cmdDispatch(ctx context.Context, args []string) error {
	f := fs("dispatch")
	a := daemon.DispatchArgs{}
	f.StringVar(&a.Run, "run", "", "")
	f.StringVar(&a.Task, "task", "", "")
	f.StringVar(&a.To, "to", "", "")
	f.StringVar(&a.Title, "title", "", "")
	f.StringVar(&a.Spec, "spec", "", "")
	if _, err := parse(f, args); err != nil {
		return err
	}
	return run(ctx, "dispatch", a, printDispatch)
}

func cmdDispatchSub(ctx context.Context, sub string, args []string) error {
	f := fs("dispatch " + sub)
	infra := f.Bool("infra", false, "")
	task := f.String("task", "", "")
	id := f.String("id", "", "")
	runID := f.String("run", "", "")
	reason := f.String("reason", "", "")
	text := f.String("text", "", "")
	force := f.Bool("force", false, "")
	pos, err := parse(f, args)
	if err != nil {
		return err
	}
	if *task == "" {
		*task = pos
	}
	switch sub {
	case "next":
		return run(ctx, "dispatch.next", daemon.RunRef{Run: *runID}, printDispatches)
	case "nudge":
		return run(ctx, "dispatch.nudge", daemon.DispatchActArgs{Task: *task, Text: *text, Force: *force}, func(raw json.RawMessage) {
			var r struct {
				Dispatch store.Dispatch `json:"dispatch"`
				Nudges   int            `json:"nudges"`
			}
			_ = json.Unmarshal(raw, &r)
			fmt.Printf("nudged %s on task %s (nudge %d)\n", r.Dispatch.PaneID, r.Dispatch.TaskID, r.Nudges)
		})
	case "fail":
		return run(ctx, "dispatch.fail", daemon.DispatchActArgs{Task: *task, Reason: *reason, Infra: *infra}, func(raw json.RawMessage) {
			var st store.Settlement
			_ = json.Unmarshal(raw, &st)
			fmt.Printf("dispatch %s %s; task %s is %s (attempt %d)\n", st.Dispatch.ID, st.Dispatch.Status, st.Task.ID, st.Task.Status, st.Task.Attempts)
		})
	}
	return run(ctx, "dispatch.show", daemon.DispatchShowArgs{Task: *task, ID: *id}, nil)
}

// ---------- workers ----------

func cmdWorker(ctx context.Context, sub string, args []string) error {
	f := fs("worker " + sub)
	runID := f.String("run", "", "")
	worker := f.String("worker", "", "")
	name := f.String("name", "", "")
	worktree := f.String("worktree", "", "")
	cwd := f.String("cwd", "", "")
	pane := f.String("pane", "", "")
	lines := f.Int("lines", 0, "")
	all := f.Bool("all", false, "")
	pos, err := parse(f, args)
	if err != nil {
		return err
	}
	if *worker == "" {
		*worker = pos
	}
	ref := daemon.WorkerRef{Worker: *worker, Lines: *lines}
	switch sub {
	case "register":
		if *pane == "" {
			*pane = pos
		}
		return run(ctx, "worker.register", daemon.WorkerRegisterArgs{Run: *runID, Pane: *pane, Name: *name, Worktree: or(*worktree, *cwd)}, printWorker)
	case "list", "":
		return run(ctx, "worker.list", daemon.WorkerListArgs{Run: *runID, All: *all}, printWorkers)
	case "read":
		return run(ctx, "worker.read", ref, func(raw json.RawMessage) {
			var r struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(raw, &r)
			fmt.Println(r.Text)
		})
	case "show":
		return run(ctx, "worker."+sub, ref, nil)
	}
	return usageErr("unknown worker subcommand %q", sub)
}
