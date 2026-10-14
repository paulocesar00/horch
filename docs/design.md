# horch design

horch coordinates persistent agents already running in herdr panes. It registers
workers, dispatches task specifications, tracks reports, and delivers mailbox messages.
It does not create agents, close panes, manage processes, or own Git worktrees.

## Runtime

The herdr plugin starts one daemon per session. The daemon owns the SQLite store,
serves a mode-0600 Unix socket, and follows pane status and exit events. A session
lock prevents concurrent writers. A replacement daemon takes over the socket;
clients reconnect only for operations safe to repeat. Uncertain dispatches are not
blindly replayed.

The watch set contains registered live workers and active dispatches. Status is
resynchronized after each subscription reconnect. A one-second tick checks idle,
blocked, and unobserved dispatch timers and notifies the coordinator.

## State

Runs group tasks and workers and identify a coordinator. Tasks have specifications,
dependencies, attempt counts, and results. Only tasks whose dependencies completed
become ready. The coordinator explicitly dispatches tasks; settlement never starts
another dispatch. Dispatches claim a task and a pane before submitting the prompt,
then record observed activity and the worker's completion report. Failed attempts
are limited by the run's circuit breaker; explicit infrastructure failures refund
the attempt. A task finishing does not end its worker's conversation.

Messages record sender, recipient, kind, thread, and independent read and terminal
delivery receipts. Completion reports stay associated with their dispatch;
late reports become notes and do not overwrite the settlement.

Historical migrations remain ordered and non-destructive. Legacy plan, gate,
schedule, hold, admission, and worker-lifecycle data stays in the database but no
longer controls dispatch or supervision. The shared resource lease database is
not opened. Mailbox consumer reservations are still connection-scoped in memory.

## Mailbox

`check --wait` waits for messages; `check --peek` and `inbox` do not consume them.
`ack` acknowledges a specific message. Only one destructive consumer may hold a
mailbox at a time. `ask` waits for a threaded `reply`.

Terminal delivery waits for an interactive idle/done recipient and records
submission and pickup separately. Retries are bounded. After a daemon restart,
uncertain submissions are escalated rather than resent automatically. See
[messaging.md](messaging.md) for receipt meanings and timing.

The plugin exposes only daemon startup and CLI installation. `horch --help` and
`horch guide` describe the supported commands.

Only the herdr plugin launches the daemon. A CLI with an unavailable socket
returns `daemon_not_running` instead of executing a daemon from an agent pane.
