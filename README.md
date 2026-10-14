# horch

horch dispatches tasks to persistent agents already running in herdr panes.
It provides tasks with dependencies, tracked completion reports, a mailbox,
blocking questions and replies, and terminal delivery receipts. Finishing a task
leaves the worker and its conversation running.

## Build and link

Requirements: Go 1.26+, macOS or Linux, and a supported herdr server.
The daemon checks the server's socket protocol before starting.

```sh
git clone https://github.com/paulocesar00/horch
cd horch
make build
herdr plugin link .
herdr plugin action invoke herdr-orch.install-cli
horch status
```

The plugin ID and daemon binary remain `herdr-orch`. The command-line binary is
`horch`. The plugin manifest contains daemon startup and CLI installation only.

## Coordinator

Start agents through herdr or your normal launcher before registering them.
Replace the example pane IDs with IDs from your current session.

```sh
horch run create --title "Refactor auth" --max-attempts 2
horch worker register --pane w1:p2 --name builder
horch worker register --pane w1:p3 --name reviewer
horch task create --title "Extract token store" --spec-file spec.md
horch task create --title "Review token store" --deps t1 --spec "Review the change and tests."
horch dispatch --task t1 --to builder
horch check --wait --timeout 30m
horch dispatch --task t2 --to reviewer
```

The task prompt includes the exact `horch done` invocation, pinned to the session
and worker identity. The coordinator receives completion and escalation messages.
A finished task does not spawn, restart, stop, release, or replace an agent.
Optional `run update --auto on` and `dispatch next` assign ready tasks to idle
registered workers in that run, oldest task first.

## Worker

```sh
horch ask --question "Keep the legacy cookie path?"
horch send --to coordinator --body "Tests are running."
horch done --task t1 --body "Extracted token store; branch auth, commit abc123, PR #42"
# Or report failure:
horch done --task t1 --failed --body "The required DB fixture is unavailable."
```

Answer a question with `horch reply --id mN --body "..."`.
Use `worker read --worker builder` and `dispatch show --task t1` to inspect a
worker. An idle worker without a report produces an escalation; silence alone
does not fail or resend a task. `dispatch nudge` asks it to report.
`dispatch fail --reason "..."` is an explicit coordinator failure;
`--infra` refunds the attempt for an infrastructure failure.

## Messages and restart

`send --wait-pickup` waits for receipt evidence. `inbox --json` shows delivery and
pickup timestamps. Keep one consuming `check --wait` per mailbox. Use
`check --peek` followed by `ack --id mN` when acknowledgement must follow processing.
A late completion report is stored without changing the closed dispatch.

`daemon stop` stops only the daemon. The next client command starts it again;
workers remain running. SQLite keeps task, report, and receipt history. Inspect
the pane and inbox before retrying an operation whose result is unknown.
Historical schema migrations are retained without dropping legacy tables.

`config` inspects the per-user preferences file at `~/.config/horch/config.toml`
(or `$HORCH_CONFIG`). It does not launch agents or enforce resource limits.
`--as PANE` and `$HORCH_AS` override the caller identity; otherwise the CLI uses
`$HERDR_PANE_ID`, falling back to `human`.

## Commands and verification

`horch --help` lists all supported commands. `horch guide` prints the agent guide.
See [design](docs/design.md) and [message delivery](docs/messaging.md).

```sh
go build ./...
go vet ./...
go test ./...
go test -race -count=1 ./...
```

The live integration lane maintains `scripts/integration.sh` against isolated
herdr sessions. It requires installed agents and spends agent turns.

[MIT license](LICENSE)
