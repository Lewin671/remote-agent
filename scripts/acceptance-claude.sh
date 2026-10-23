#!/bin/sh
# Claude Code acceptance run. Like scripts/acceptance.sh it is not part of
# `go test`: it needs an authenticated claude, network access and a few
# minutes, and it spends a little of your Claude quota. It uses Haiku, the
# cheapest model Claude Code's catalog offers; when that model is retired,
# name the new one here (its ID, as `claude --model` takes it).
#
# It uses its own state directory and tmux socket, so it does not touch
# your sessions, and the user's own Claude Code configuration (the catalog
# it caches and its login). The first run in a new work directory stops at
# Claude Code's trust prompt; the script answers it the way a local user
# would.
#
# Usage: scripts/acceptance-claude.sh [work-dir]
set -u

ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=${1:-/tmp/ra-cc-acceptance-work}
STATE=$(mktemp -d /tmp/ra-cc-acc.XXXXXX)
# The daemon refuses a state directory with unmarked files, so its log lives
# next to it.
LOG="$STATE.daemon.log"
BIN=/tmp/ra-cc-acceptance-bin/remote-agent
MODEL=claude-haiku-5-5
OTHER=claude-sonnet-5-5
mkdir -p /tmp/ra-cc-acceptance-bin
export REMOTE_AGENT_HOME="$STATE"
export REMOTE_AGENT_TMUX_SOCKET="ra-cc-acc-$$"
TMUX_SOCK="$REMOTE_AGENT_TMUX_SOCKET"
CLAUDE=${REMOTE_AGENT_CLAUDE:-$(command -v claude)}
FAILS=0

mkdir -p "$WORK"
(cd "$ROOT" && go build -o "$BIN" ./cmd/remote-agent) || exit 1

cleanup() {
	[ -n "${DAEMON_PID:-}" ] && kill "$DAEMON_PID" 2>/dev/null
	tmux -L "$TMUX_SOCK" kill-server 2>/dev/null
}
trap cleanup EXIT

start_daemon() {
	"$BIN" daemon --claude "$CLAUDE" --approval-timeout "${1:-60s}" >>"$LOG" 2>&1 &
	DAEMON_PID=$!
	i=0
	while [ ! -S "$STATE/daemon.sock" ] && [ $i -lt 50 ]; do sleep 0.1; i=$((i + 1)); done
}

pass() { printf 'PASS  %s\n' "$1"; }
fail() { printf 'FAIL  %s\n' "$1"; FAILS=$((FAILS + 1)); }

# wait_for <seconds> <command...>: retry until the command succeeds.
wait_for() {
	n=$1; shift
	i=0
	while [ $i -lt "$n" ]; do
		if "$@" >/dev/null 2>&1; then return 0; fi
		sleep 1; i=$((i + 1))
	done
	return 1
}

events() { "$BIN" events "$1" --json; }
after_events() { "$BIN" events "$1" --after "$2" --json; }
short() { printf %s "$1" | cut -c1-12; }
pane() { printf 'ra-%s:' "$(printf %s "$1" | cut -c1-8)"; }
lsrow() { "$BIN" ls -a | grep "^$(short "$1")" || true; }
idle() { "$BIN" ls | grep "^$(short "$1")" | grep -q idle; }
last_seq() { events "$1" | tail -1 | sed -n 's/.*"seq":"\{0,1\}\([0-9]*\)".*/\1/p'; }
# sha <file>: its SHA-256, with whichever tool this machine has (as dist-smoke.sh).
sha() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi
}

# wait_new <seconds> <id> <seq> <pattern>: retry until an event after seq
# matches pattern. Scoping to the sequence keeps a leftover event from an
# earlier step from counting.
wait_new() {
	n=$1; id=$2; seq=$3; pat=$4
	i=0
	while [ $i -lt "$n" ]; do
		if after_events "$id" "$seq" | grep -q "$pat"; then return 0; fi
		sleep 1; i=$((i + 1))
	done
	return 1
}

# Answer Claude Code's startup trust prompt like a local user would. Wait
# for the prompt to be drawn before pressing: keys sent earlier land on the
# default "No, exit" and close the session.
settle() {
	id=$1
	i=0
	while [ $i -lt 60 ]; do
		line=$(lsrow "$id")
		case "$line" in
		*"trust folder"*)
			tmux -L "$TMUX_SOCK" send-keys -t "$(pane "$id")" Down
			sleep 1
			tmux -L "$TMUX_SOCK" send-keys -t "$(pane "$id")" Enter
			sleep 2 ;;
		*idle*) return 0 ;;
		*exited*) return 1 ;;
		esac
		sleep 1; i=$((i + 1))
	done
	return 1
}

last_approval() {
	events "$1" | grep '"approvalRequested"' | tail -1 | sed -n 's/.*"approvalId":"\([^"]*\)".*/\1/p'
}

start_daemon 60s
cd "$WORK"

# 1. A new session starts and reaches the input box.
ID=$("$BIN" new --agent claude --model "$MODEL" --permission-mode default)
if settle "$ID"; then pass "session starts and reaches the input box"; else fail "session start"; fi

# 2. Send a message; the turn's events arrive in order and the reply reads back.
seq=$(last_seq "$ID")
"$BIN" send "$ID" "Reply with exactly the word PONG and nothing else."
if wait_new 90 "$ID" "$seq" '"turnEnded"'; then
	kinds=$(after_events "$ID" "$seq" | grep -o -E '"(turnStarted|userMessage|agentMessage|turnEnded)"' | tr -d '"' | tr '\n' ' ')
	case "$kinds" in
	*"turnStarted userMessage agentMessage turnEnded"*) pass "send; events in order: $kinds" ;;
	*) fail "event order: $kinds" ;;
	esac
	if after_events "$ID" "$seq" | grep -q '"text":"PONG"'; then pass "the reply is readable from events"; else fail "reply not in events"; fi
else
	fail "no completed turn after send"
fi
wait_for 30 idle "$ID"

# 3. Approvals: allow lets the command run, deny blocks it.
target="$WORK/ra-cc-acceptance-$$"
approve_case() {
	verb=$1
	seq=$(last_seq "$ID")
	"$BIN" send "$ID" "Run exactly this shell command and nothing else: touch $target-$verb"
	if ! wait_new 90 "$ID" "$seq" '"approvalRequested"'; then fail "no approval request ($verb)"; return; fi
	aid=$(after_events "$ID" "$seq" | grep '"approvalRequested"' | tail -1 | sed -n 's/.*"approvalId":"\([^"]*\)".*/\1/p')
	case $verb in
	allow) "$BIN" approve "$ID" "$aid" || true ;;
	deny) "$BIN" deny "$ID" "$aid" -m "acceptance test" || true ;;
	esac
	wait_new 60 "$ID" "$seq" "\"approvalResolved\":{\"approvalId\":\"$aid\"" || true
	wait_for 60 idle "$ID" || true
	if [ "$verb" = allow ]; then
		if [ -e "$target-$verb" ]; then pass "approve lets the command run"; else fail "approve"; fi
	else
		if [ ! -e "$target-$verb" ]; then pass "deny blocks the command"; else fail "deny"; fi
	fi
	rm -f "$target-$verb"
}
approve_case allow
approve_case deny

# 4. A single-select question, answered from outside.
seq=$(last_seq "$ID")
"$BIN" send "$ID" "Use the AskUserQuestion tool now to ask me exactly one single-select question: which language should a hello-world script use. Use the header Language and exactly two options, Go and Python. Ask only that, nothing else."
if wait_new 120 "$ID" "$seq" '"questionAsked"'; then
	qid=$(after_events "$ID" "$seq" | grep '"questionAsked"' | tail -1 | sed -n 's/.*"questionId":"\([^"]*\)".*/\1/p')
	if "$BIN" answer "$ID" "$qid" 2 && wait_new 60 "$ID" "$seq" '"questionAnswered"'; then
		pass "single-select question answered from outside"
	else
		fail "single-select answer"
	fi
else
	fail "no single-select question asked"
fi
wait_for 60 idle "$ID" || true

# 5. A multi-select question, answered with two options.
seq=$(last_seq "$ID")
"$BIN" send "$ID" "Use the AskUserQuestion tool now to ask me exactly one multi-select question (multiSelect true): which fruits do you like. Use the header Fruit and exactly three options, Apple, Plum and Cherry. Ask only that, nothing else."
if wait_new 120 "$ID" "$seq" '"questionAsked"'; then
	ev=$(after_events "$ID" "$seq" | grep '"questionAsked"' | tail -1)
	qid=$(printf %s "$ev" | sed -n 's/.*"questionId":"\([^"]*\)".*/\1/p')
	case "$ev" in
	*'"multiSelect":true'*)
		if "$BIN" answer "$ID" "$qid" 1,2 && wait_new 60 "$ID" "$seq" '"questionAnswered"'; then
			pass "multi-select question answered from outside"
		else
			fail "multi-select answer"
		fi ;;
	*) fail "the question was not multi-select" ;;
	esac
else
	fail "no multi-select question asked"
fi
wait_for 60 idle "$ID" || true

# 6. Interrupt a running turn. A standalone `sleep` is blocked by Claude
# Code 2.1.300, so use the until-loop its own message suggests.
seq=$(last_seq "$ID")
"$BIN" send "$ID" "Run exactly this shell command and nothing else: until [ -f ra-cc-acceptance-never ]; do sleep 2; done"
if wait_new 60 "$ID" "$seq" '"toolStarted"'; then
	"$BIN" interrupt "$ID" || true
	if wait_new 30 "$ID" "$seq" '"TURN_OUTCOME_ABORTED"'; then pass "interrupt aborts the turn"; else fail "interrupt"; fi
else
	fail "the interrupt command did not start"
fi
wait_for 30 idle "$ID" || true

# 7. A local draft is never overwritten: the message waits in the daemon's queue.
tmux -L "$TMUX_SOCK" send-keys -t "$(pane "$ID")" -l "local draft"
sleep 1
seq=$(last_seq "$ID")
"$BIN" send "$ID" "Reply with exactly REMOTE-QUEUED"
sleep 3
if "$BIN" ls | grep -q "1 queued"; then pass "remote input waits while a local draft exists"; else fail "queued while draft"; fi
tmux -L "$TMUX_SOCK" send-keys -t "$(pane "$ID")" C-u
# Wait for the reply, not the queued text: that names the same words.
if wait_new 90 "$ID" "$seq" '"turnEnded"' && after_events "$ID" "$seq" | grep -q '"text":"REMOTE-QUEUED"'; then
	pass "queued input is sent once the draft is gone"
else
	fail "queued input not sent"
fi
wait_for 30 idle "$ID" || true

# 8. Switch the model and effort for this session only: the daemon records it
# and the user's own Claude Code settings are not touched (the menu's `s`).
settings="$HOME/.claude/settings.json"
before=$(sha "$settings" 2>/dev/null | awk '{print $1}')
seq=$(last_seq "$ID")
if "$BIN" model "$ID" "$OTHER" low >/dev/null 2>&1; then
	pass "switch model and effort"
else
	fail "switch model and effort"
fi
wait_new 30 "$ID" "$seq" "\"settingsChanged\":{\"model\":\"$OTHER\"" || fail "the switch is not recorded"
after=$(sha "$settings" 2>/dev/null | awk '{print $1}')
if [ "$before" = "$after" ]; then pass "the switch left settings.json alone"; else fail "settings.json changed"; fi
"$BIN" model "$ID" "$MODEL" >/dev/null 2>&1 || fail "switch back to $MODEL"
wait_for 30 idle "$ID" || true

# 9. Switch the approval mode and see it recorded.
mode_case() {
	want=$1
	if "$BIN" mode "$ID" "$want" >/dev/null 2>&1; then
		pass "switch approval mode to $want"
	else
		fail "switch approval mode to $want"
	fi
}
mode_case plan
mode_case ask

# 10. Move to Plan mode, propose a plan and approve it.
if ! wait_for 20 idle "$ID"; then fail "the session is not idle before the plan step"; fi
seq=$(last_seq "$ID")
"$BIN" mode "$ID" plan >/dev/null 2>&1 || fail "cannot enter Plan mode for the plan step"
"$BIN" send "$ID" "Plan this in one step: create a file named plan.txt in the current directory containing the word hi. Then call ExitPlanMode."
if wait_new 120 "$ID" "$seq" '"planProposed"'; then
	seq=$(last_seq "$ID")
	"$BIN" plan "$ID" implement >/dev/null 2>&1 || true
	if wait_new 60 "$ID" "$seq" '"planAnswered"'; then
		pass "plan proposed and approved"
	else
		fail "plan approval"
	fi
else
	fail "no plan proposed"
fi
# The approved plan runs in manual mode: clear any permission request it
# raises, so the session comes back to the input box.
if wait_for 30 sh -c "'$BIN' ls | grep -q '1 approval'"; then
	aid=$(last_approval "$ID")
	"$BIN" deny "$ID" "$aid" -m "acceptance cleanup" || true
fi
wait_for 90 idle "$ID" || true

# 11. Stop the session and resume it: it continues the same conversation.
seq=$(last_seq "$ID")
"$BIN" send "$ID" "Remember the codeword BANANA-77. Reply with just OK."
wait_new 90 "$ID" "$seq" '"turnEnded"' || true
"$BIN" stop "$ID" || true
if wait_for 15 sh -c "'$BIN' ls -a | grep '^$(short "$ID")' | grep -q exited"; then pass "stop ends the session"; else fail "stop"; fi
"$BIN" resume "$ID" || true
settle "$ID" || true
seq=$(last_seq "$ID")
"$BIN" send "$ID" "What codeword did I ask you to remember? Reply with just the codeword."
if wait_new 90 "$ID" "$seq" "BANANA-77"; then pass "resume continues the same conversation"; else fail "resume"; fi
wait_for 60 idle "$ID" || true

# 12. Restart the daemon: sessions come back and events continue.
before=$("$BIN" ls | grep -c . || true)
last=$(last_seq "$ID")
kill "$DAEMON_PID" 2>/dev/null
wait "$DAEMON_PID" 2>/dev/null
start_daemon 60s
after=$("$BIN" ls | grep -c . || true)
seq=$(last_seq "$ID")
"$BIN" send "$ID" "Reply with exactly AFTER-RESTART"
if [ "$before" = "$after" ] && wait_new 90 "$ID" "$seq" "AFTER-RESTART"; then
	dups=$(events "$ID" | sed -n 's/.*"seq":"\{0,1\}\([0-9]*\)".*/\1/p' | sort | uniq -d | wc -l | tr -d ' ')
	if [ "$dups" = 0 ]; then pass "restart keeps sessions; seq continues after $last"; else fail "duplicate seq after restart"; fi
else
	fail "restart"
fi

"$BIN" stop "$ID" >/dev/null 2>&1

echo
if [ "$FAILS" = 0 ]; then echo "all checks passed"; else echo "$FAILS check(s) failed; daemon log: $LOG"; exit 1; fi
