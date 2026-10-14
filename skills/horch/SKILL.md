---
name: horch
description: Coordinate persistent registered herdr panes with tasks, tracked dispatch, a mailbox, and blocking ask/reply.
---

# horch

Use `horch` to coordinate agents already running in herdr panes. Register each
worker with the current run. Dispatching and completing a task leave the pane
and its conversation running. horch never launches or releases teammates.

## Coordinator

```sh
horch run create --title "Refactor auth" --max-attempts 2
horch worker register --pane w1:p2 --name builder
horch worker register --pane w1:p3 --name reviewer
horch task create --title "Extract token store" --spec-file spec.md
horch task create --title "Review token store" --deps t1 --spec "Review the diff and tests."
horch dispatch --task t1 --to builder
horch check --wait --timeout 30m --json
```

Replace pane and task IDs with the actual IDs returned in your session.
Use `run use R` to resume a run, `run current` to inspect the current binding,
and `run list|show|update` to inspect or change run settings.
`task list|show|update` manages tasks, dependencies, results, and retry budgets.
`dispatch next` explicitly assigns ready tasks to idle registered workers.
Task completion never automatically dispatches more work. Use `dispatch --to NAME`
when the coordinator must choose which teammate handles each task.

Handle messages before dispatching more work:

- `done`: verify the result, then dispatch any newly ready task.
- `question`: answer with `horch reply --id mN --body "..."`.
- `escalation`: inspect with `worker read --worker NAME` and
  `dispatch show --task T`; decide whether to nudge or fail.
- `note`: read the update; late reports do not reopen completed work.

`dispatch nudge --task T` asks an idle worker to report. It refuses while the
worker is working or blocked unless explicitly forced.
`dispatch fail --task T --reason "..."` fails the current attempt.
Use `--infra` only for infrastructure failure; it refunds that attempt.
Silence alone never fails a task or causes its prompt to be resent.

## Worker

Run the exact command included in your dispatch prompt:

```sh
horch done --task t1 --body "Summary; branch/commit/PR and tests"
horch done --task t1 --failed --body "Why it failed"
horch ask --question "Which behavior is required?"
horch send --to coordinator --body "Progress update"
```

`ask` blocks until a threaded reply arrives (default two hours).
A completion report is recorded against your active dispatch or an explicit task.
Only its assignee can report. A late report becomes a note without replacing
the task's existing result. Pane exit fails an active dispatch; failure counts
against the run's circuit breaker unless the coordinator uses `--infra`.

## Mailbox

Use one consuming `check --wait` per mailbox. A second destructive consumer is
refused with `mailbox_busy`. `check --peek` and `inbox` never consume messages;
`ack --id mN` acknowledges a specific message addressed to you.
`send --to NAME --body B --wait-pickup` waits for receipt evidence.
Terminal delivery waits for an interactive idle/done agent and retries at most
once. Inspect `inbox --json` and the pane before manually resending uncertain
input. Receipt evidence is distinct from a mailbox read.

## Inspection and restart

`worker register|show|read|list` manages registration and inspection only.
`status` reports daemon health; `daemon stop` stops only the daemon. Restart it
through the herdr plugin. The CLI never launches a daemon from a worker pane.
Workers remain running and SQLite preserves tasks and mail.
Clients retry only safe operations after disconnect; uncertain dispatches are
not automatically replayed.

`config` inspects per-user preferences. `version` prints the version.
`--json` provides machine output, and commands exit nonzero on refusal.
Identity defaults to `$HERDR_PANE_ID`, otherwise `human`; `$HORCH_AS` or `--as`
can select another identity. Keep the session and caller identity from the
pinned command in your task prompt.

Run `horch --help` for the complete supported command syntax.
