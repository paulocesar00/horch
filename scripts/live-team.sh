#!/usr/bin/env bash
# Live check of how the team uses horch: two persistent, REGISTERED agent panes
# (Codex via `codex --profile apollo`, Claude haiku) in an isolated herdr session,
# with this script as the coordinator identity. Spends real agent turns.
#   1+2. dispatch -> `horch done` -> `check --wait`; second task to the same pane recalls the file
#   3. mid-task `horch ask`; the reply comes from the coordinator identity
#   4. dispatch to a busy pane is refused or queued, never typed over
#   5. `done` sent while still working: settles once, only after idle
#   6. daemon killed right after dispatch: no re-send, coordinator told, pane usable after
# A failed check does not stop the run; the script exits nonzero if any check failed.
# KEEP=1 keeps the session and temp dir.
set -uo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
BIN=${BIN:-$ROOT/bin}
SESSION=${HORCH_IT_SESSION:-horch-live-team}
SOCK=$HOME/.config/herdr/sessions/$SESSION/herdr.sock
WORK=$(mktemp -d "${TMPDIR:-/tmp}/horch-live-team.XXXXXX")
STATE=$HOME/.local/state/herdr/plugins/herdr-orch/sessions/$SESSION
CODEX_CMD='codex --profile apollo'
CLAUDE_CMD='claude --model haiku --permission-mode auto'

unset HERDR_PANE_ID HERDR_TAB_ID HERDR_WORKSPACE_ID HERDR_SESSION HERDR_PLUGIN_STATE_DIR HORCH_AS
export HERDR_SOCKET_PATH=$SOCK PATH=$BIN:$PATH
export HORCH_CONFIG=$WORK/horch.toml   # never the real ~/.config/horch
export HORCH_IDLE_REPORT_AFTER=10m HORCH_UNOBSERVED_AFTER=10m
cat >"$HORCH_CONFIG" <<'TOML'
default_project = "live"
[owner]
principal = "live-owner"
[projects.live]
repo = "example/live"
TOML

pass=0 fail=0
ok()   { pass=$((pass + 1)); printf '  \033[32m✓\033[0m %s\n' "$*"; }
bad()  { fail=$((fail + 1)); printf '  \033[31m✗ %s\033[0m\n' "$*"; }
note() { printf '    · %s\n' "$*" >&2; }
step() { printf '\n\033[1m%s\033[0m\n' "$*"; }
j()    { jq -r "$@"; }
check() { local label=$1; shift; if "$@"; then ok "$label"; else bad "$label"; fi; }
summary() { printf '\n%d passed, %d failed\n' "$pass" "$fail"; [ "$fail" -eq 0 ]; exit $?; }
abort() { bad "$*"; summary; }

cleanup() {
  set +e
  if [ "${KEEP:-}" = 1 ]; then echo "kept session $SESSION, work dir $WORK"; return; fi
  horch daemon stop >/dev/null 2>&1
  herdr session stop "$SESSION" >/dev/null 2>&1; herdr session delete "$SESSION" >/dev/null 2>&1
  rm -rf "$WORK"
}
trap cleanup EXIT

# Start an agent pane in its own temp git repo, answer its folder-trust prompt, wait for idle.
start_agent() { # start_agent <name> <command> -> pane id
  local name=$1 cmd=$2 p screen
  git init -q "$WORK/$name"
  git -C "$WORK/$name" -c user.email=t@t -c user.name=t commit -q --allow-empty -m init
  p=$(herdr pane split "$COORD" --direction right --cwd "$WORK/$name" --env "PATH=$PATH" --env "HERDR_SOCKET_PATH=$SOCK" --env "HORCH_CONFIG=$HORCH_CONFIG" --no-focus | j .result.pane.pane_id)
  sleep 0.5
  herdr pane run "$p" "$cmd" >/dev/null
  for _ in $(seq 90); do
    screen=$(herdr pane read "$p" --source visible 2>/dev/null || true)
    if grep -q "Yes, I trust this folder" <<<"$screen"; then
      herdr pane send-keys "$p" down enter >/dev/null; note "$name: answered claude's folder-trust prompt"; sleep 3
    elif grep -q "Trust and continue\|Yes, continue" <<<"$screen"; then
      herdr pane send-keys "$p" enter >/dev/null; note "$name: answered codex's folder-trust prompt"; sleep 3
    elif [ "$(herdr pane get "$p" | j .result.pane.agent_status)" = idle ]; then
      echo "$p"; return 0
    fi
    sleep 1.5
  done
  return 1
}

# wait_event <kind> [seconds]: block on check --wait until a message of that kind arrives;
# prints it as JSON. Every message seen is appended to $WORK/seen.ndjson.
wait_event() {
  local kind=$1 deadline=$(( $(date +%s) + ${2:-300} )) out left
  while left=$(( deadline - $(date +%s) )); [ "$left" -gt 0 ]; do
    out=$(horch check --wait --timeout "${left}s" --json)
    echo "$out" | j -c '.[]' >>"$WORK/seen.ndjson"
    echo "$out" | j -c --arg k "$kind" '[.[] | select(.kind==$k)] | .[0] // empty' | grep . && return 0
  done
  return 1
}
seen_kind() { jq -r --arg k "$1" 'select(.kind==$k) | .kind' "$WORK/seen.ndjson" | wc -l; }
screen() { herdr pane read "$1" --source recent --lines 300 2>/dev/null; }
pane_status() { herdr pane get "$1" | j .result.pane.agent_status; }
wait_idle() { for _ in $(seq 120); do [ "$(pane_status "$1")" = idle ] && return 0; sleep 2; done; return 1; }
dispatch() { # dispatch <title> <spec> <pane> -> task id (dispatch result goes to stderr)
  local t
  t=$(horch task create --title "$1" --spec "$2" --json | j .id) || return 1
  horch dispatch --task "$t" --to "$3" --json >&2 || return 1
  echo "$t"
}
tstatus() { horch task show --id "$1" --json | j .task.status; }

step "setup: isolated herdr session '$SESSION'"
for c in codex claude jq git; do command -v "$c" >/dev/null || abort "$c not installed"; done
horch daemon stop >/dev/null 2>&1 || true
herdr session stop "$SESSION" >/dev/null 2>&1 || true
herdr session delete "$SESSION" >/dev/null 2>&1 || true
rm -rf "$STATE"
env -u HERDR_ENV -u HERDR_SOCKET_PATH herdr --session "$SESSION" server >"$WORK/server.log" 2>&1 &
for _ in $(seq 50); do [ -S "$SOCK" ] && herdr status server >/dev/null 2>&1 && break; sleep 0.2; done
herdr status server >/dev/null 2>&1 || abort "herdr test server did not start"
ok "herdr server up ($SESSION)"
WS=$(herdr workspace create --cwd "$WORK" --label live-team | j .result.workspace.workspace_id)
COORD=$(herdr pane list --workspace "$WS" | j '.result.panes[0].pane_id')
export HORCH_AS=$COORD
: >"$WORK/seen.ndjson"

step "start and register the two agent panes (once)"
CODEX=$(start_agent codex "$CODEX_CMD") || abort "codex never became idle"
CLAUDE=$(start_agent claude "$CLAUDE_CMD") || abort "claude never became idle"
ok "codex pane $CODEX and claude pane $CLAUDE idle"
horch run create --title "live team" --json >/dev/null
reg=$(horch worker register --pane "$CODEX" --name codex --json 2>&1 && horch worker register --pane "$CLAUDE" --name claude --json 2>&1) \
  && ok "both panes registered with worker register (coordinator $COORD)" || abort "worker register failed: $reg"
horch check --json >/dev/null

HOWTO='Report with the horch done command given below, with --body "<text>".'

step "1+2. Codex: dispatch, done body, then the same pane recalls the file"
FILE=live-note-$RANDOM.txt
TA=$(dispatch "create file" "Create a file named $FILE in the current directory containing the text hello. Do not commit it. $HOWTO Put the file name in the body." "$CODEX") || abort "dispatch A failed"
if msg=$(wait_event done 300); then
  body=$(echo "$msg" | j .body); note "done body: ${body:0:160}"
  check "check --wait returned task A's completion naming $FILE" grep -q "$FILE" <<<"$body"
  check "task A is completed" test "$(tstatus "$TA")" = completed
else bad "check --wait never returned task A's completion"; fi
TB=$(dispatch "recall" "What file did you create in the previous task? $HOWTO Name the file in the body." "$CODEX") || abort "dispatch B failed"
if msg=$(wait_event done 300); then
  body=$(echo "$msg" | j .body); note "done body: ${body:0:160}"
  check "task B names $FILE (conversation persisted)" grep -q "$FILE" <<<"$body"
else bad "no done message for task B"; fi

step "3. mid-task ask; the reply comes from the coordinator identity"
TC=$(dispatch "ask first" "Before anything else run: horch ask --question \"Which color: red or blue?\" (it blocks until the coordinator replies). Then $HOWTO Put the color from the reply in the body." "$CLAUDE") || abort "dispatch C failed"
if msg=$(wait_event question 300); then
  QID=$(echo "$msg" | j .id)
  ok "check --wait surfaced the ask: $(echo "$msg" | j .body | head -c 100)"
  note "ask from=$(echo "$msg" | j .from) to=$(echo "$msg" | j .to)"
  # A pane that is not the coordinator must not be able to answer the coordinator's ask.
  out=$(HORCH_AS=$CODEX horch reply --id "$QID" --body "red" --json 2>&1); rc=$?
  note "reply as non-coordinator $CODEX: rc=$rc ${out:0:160}"
  check "reply from a non-coordinator identity is refused" test "$rc" -ne 0
  out=$(HORCH_AS=$COORD horch reply --id "$QID" --body "blue" --json 2>&1) && ok "reply as coordinator $COORD accepted" || bad "coordinator reply failed: $out"
  if msg=$(wait_event done 300); then
    body=$(echo "$msg" | j .body); note "done body: ${body:0:160}"
    check "claude's done body carries the coordinator's answer (blue)" grep -qi blue <<<"$body"
    check "task C is completed" test "$(tstatus "$TC")" = completed
  else bad "task C never completed after the reply"; fi
else bad "coordinator never saw the ask"; fi
wait_idle "$CLAUDE"

step "4. dispatch to a busy pane is refused or queued, never typed over"
TOKEN=BUSYPROBE$RANDOM
TD=$(dispatch "slow" "Run the shell command: sleep 60. Then $HOWTO Body: slow-done." "$CLAUDE") || abort "dispatch D failed"
for _ in $(seq 60); do [ "$(pane_status "$CLAUDE")" = working ] && break; sleep 1; done
check "claude pane is working" test "$(pane_status "$CLAUDE")" = working
TE=$(horch task create --title "busy probe" --spec "$TOKEN: finish immediately with horch done." --json | j .id)
out=$(horch dispatch --task "$TE" --to "$CLAUDE" --json 2>&1); rc=$?
note "dispatch while busy: rc=$rc ${out:0:200}"
sleep 4
if [ "$(pane_status "$CLAUDE")" = working ] && grep -q "$TOKEN" <<<"$(herdr pane read "$CLAUDE" --source visible)"; then
  bad "BUG: busy pane was typed over (probe prompt on screen while the first task runs)"
else
  ok "probe prompt not typed into the busy pane (rc=$rc)"
fi
note "probe dispatch status: $(horch dispatch show --task "$TE" --json 2>/dev/null | j '.[0].status // "none"')"
wait_event done 200 >/dev/null || bad "slow task never reported"
sleep 5; horch task update --id "$TE" --status cancelled >/dev/null 2>&1 || true
wait_idle "$CLAUDE"

step "5. done while still working: settles once, only after idle"
TG=$(dispatch "early done" "First run the horch done command below with --body early-report. Then, without stopping, run the shell command sleep 40 and reply with the word finished." "$CLAUDE") || abort "dispatch G failed"
if msg=$(wait_event done 120); then
  st=$(pane_status "$CLAUDE"); ts=$(tstatus "$TG")
  note "right after the done message: pane=$st task=$ts"
  if [ "$st" = working ]; then
    check "task not settled while the pane is still working" test "$ts" != completed
    TX=$(horch task create --title "second while working" --spec "reply ok via horch done" --json | j .id)
    out=$(horch dispatch --task "$TX" --to "$CLAUDE" --json 2>&1); rc=$?
    note "dispatch during the post-done work: rc=$rc ${out:0:160}"
    check "no second prompt typed while working" test "$(herdr pane read "$CLAUDE" --source visible | grep -c 'second while working')" = 0
    horch task update --id "$TX" --status cancelled >/dev/null 2>&1 || true
  else
    note "pane was already idle when done arrived; settle-after-idle not exercised"
  fi
  for _ in $(seq 60); do [ "$(tstatus "$TG")" = completed ] && break; sleep 2; done
  check "task settles completed after idle" test "$(tstatus "$TG")" = completed
  check "exactly one dispatch row for the task" test "$(horch dispatch show --task "$TG" --json | j length)" = 1
else bad "early done never reached the coordinator"; fi
wait_idle "$CLAUDE"

step "6. daemon killed right after dispatch: no re-send, coordinator told, pane usable after"
TOKEN2=ONCEONLY$RANDOM
TF=$(horch task create --title "survive restart" --spec "$TOKEN2: run the shell command sleep 30, then $HOWTO Body: survived." --json | j .id)
horch dispatch --task "$TF" --to "$CLAUDE" --json >/dev/null || abort "dispatch F failed"
OLD=$(horch status --json | j .pid)
kill -9 "$OLD"                           # no clean shutdown, as after a WSL suspend
sleep 1
NEW=$(horch status --json | j .pid)      # the CLI starts a fresh daemon
check "daemon restarted ($OLD -> $NEW)" test "$OLD" != "$NEW"
sleep 5
rows=$(horch dispatch show --task "$TF" --json | j length)
st=$(horch dispatch show --task "$TF" --json | j '.[-1].status')
note "dispatch rows=$rows status=$st task=$(tstatus "$TF")"
check "one dispatch row, no re-dispatch" test "$rows" = 1
if wait_event done 120 >/dev/null; then
  check "prompt reached the pane exactly once" test "$(screen "$CLAUDE" | grep -c "\[horch dispatch .*survive restart")" = 1
  check "task completed after the restart" test "$(tstatus "$TF")" = completed
else
  note "no done after restart; dispatch=$st task=$(tstatus "$TF")"
  check "coordinator got an undelivered/unknown escalation (silent loss is a bug)" test "$(seen_kind escalation)" -ge 1
  check "state not reported completed" test "$(tstatus "$TF")" != completed
fi
wait_idle "$CLAUDE"
dispatch "after restart" "$HOWTO Body: alive." "$CLAUDE" >/dev/null || bad "dispatch after restart refused"
if wait_event done 120 >/dev/null; then ok "pane takes a new dispatch after the restart"; else bad "pane did not complete a dispatch after the restart"; fi

summary
