#!/bin/sh
# P0 acceptance run against a real Codex. It is not part of `go test`
# because it needs an authenticated codex, network access and a few
# minutes, and it spends a little of your Codex quota.
#
# It uses its own state directory and tmux socket, so it does not touch
# your sessions. The first run in a new directory may stop at Codex's
# "Trust this folder?" and "Hooks need review" prompts; the script answers
# them the way a local user would.
#
# Usage: scripts/acceptance.sh [work-dir]
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=${1:-/tmp/ra-acceptance-work}
STATE=$(mktemp -d /tmp/ra-acc.XXXXXX)
BIN=/tmp/ra-acceptance-bin/remote-agent
mkdir -p /tmp/ra-acceptance-bin
export REMOTE_AGENT_HOME="$STATE"
export REMOTE_AGENT_TMUX_SOCKET="ra-acc-$$"
TMUX_SOCK="$REMOTE_AGENT_TMUX_SOCKET"
CODEX=${REMOTE_AGENT_CODEX:-$(command -v codex)}
FAILS=0

mkdir -p "$WORK"
(cd "$ROOT" && go build -o "$BIN" ./cmd/remote-agent)

cleanup() {
	[ -n "${DAEMON_PID:-}" ] && kill "$DAEMON_PID" 2>/dev/null || true
	tmux -L "$TMUX_SOCK" kill-server 2>/dev/null || true
}
trap cleanup EXIT

start_daemon() {
	"$BIN" daemon --codex "$CODEX" --approval-timeout "${1:-60s}" >>"$STATE/daemon.log" 2>&1 &
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
has_event() { events "$1" | grep -q "$2"; }
status_is() { "$BIN" ls -a | grep "^$(printf %s "$1" | cut -c1-12)" | grep -q "$2"; }
pane() { printf 'ra-%s:' "$(printf %s "$1" | cut -c1-8)"; }

# Answer startup prompts like a local user would.
settle() {
	id=$1
	i=0
	while [ $i -lt 30 ]; do
		line=$("$BIN" ls | grep "^$(printf %s "$id" | cut -c1-12)" || true)
		case "$line" in
		*"trust folder"*) tmux -L "$TMUX_SOCK" send-keys -t "$(pane "$id")" Enter ;;
		*"hooks need review"*)
			tmux -L "$TMUX_SOCK" send-keys -t "$(pane "$id")" Down
			sleep 0.3
			tmux -L "$TMUX_SOCK" send-keys -t "$(pane "$id")" Enter ;;
		*idle*) return 0 ;;
		esac
		sleep 1; i=$((i + 1))
	done
	return 1
}

last_approval() {
	events "$1" | sed -n 's/.*"approvalRequested":{"approvalId":"\([0-9a-f]*\)".*/\1/p' | tail -1
}
last_question() {
	events "$1" | sed -n 's/.*"questionAsked":{"questionId":"\([^"]*\)".*/\1/p' | tail -1
}

start_daemon 60s
cd "$WORK"

# 1. Start a session; it keeps running without a terminal attached.
ID=$("$BIN" new -c 'approvals_reviewer="user"')
if settle "$ID"; then pass "session starts and reaches the input box"; else fail "session start"; fi

# 2-3. Send a message; the turn's events arrive in order.
"$BIN" send "$ID" "Reply with exactly the word PONG and nothing else."
if wait_for 60 sh -c "'$BIN' events '$ID' --json | grep -q '\"turnEnded\"'"; then
	kinds=$(events "$ID" | grep -o -E '"(turnStarted|userMessage|agentMessage|turnEnded)"' | tr -d '"' | tr '\n' ' ')
	case "$kinds" in
	*"turnStarted userMessage agentMessage turnEnded"*) pass "send; events in order: $kinds" ;;
	*) fail "event order: $kinds" ;;
	esac
else
	fail "no completed turn after send"
fi

# 2. A local draft is never overwritten: the message waits in the queue.
tmux -L "$TMUX_SOCK" send-keys -t "$(pane "$ID")" -l "local draft"
sleep 1
"$BIN" send "$ID" "Reply with exactly REMOTE-QUEUED"
sleep 3
if "$BIN" ls | grep -q "1 queued"; then pass "remote input waits while a local draft exists"; else fail "queued while draft"; fi
tmux -L "$TMUX_SOCK" send-keys -t "$(pane "$ID")" C-c
if wait_for 60 has_event "$ID" "REMOTE-QUEUED"; then pass "queued input is sent once the draft is gone"; else fail "queued input not sent"; fi
wait_for 60 sh -c "'$BIN' ls | grep '^$(printf %s "$ID" | cut -c1-12)' | grep -q idle" || true

# 5. Interrupt a running turn.
"$BIN" send "$ID" "Run the shell command: sleep 30 ; then reply DONE"
wait_for 30 sh -c "'$BIN' ls | grep -q running" || true
sleep 3
"$BIN" interrupt "$ID"
if wait_for 20 has_event "$ID" "TURN_OUTCOME_ABORTED"; then pass "interrupt aborts the turn"; else fail "interrupt"; fi

# 4. Approvals: allow, deny, timeout.
target="$HOME/.ra-acceptance-$$"
approve_case() {
	verb=$1
	"$BIN" send "$ID" "Run exactly this shell command and nothing else: touch $target-$verb"
	if ! wait_for 60 sh -c "'$BIN' ls | grep -q '1 approval'"; then fail "no approval request ($verb)"; return; fi
	aid=$(last_approval "$ID")
	case $verb in
	approve) "$BIN" approve "$ID" "$aid" ;;
	deny) "$BIN" deny "$ID" "$aid" -m "acceptance test" ;;
	timeout) ;;
	esac
	wait_for 90 sh -c "'$BIN' events '$ID' --json | grep -q '\"approvalResolved\":{\"approvalId\":\"$aid\"'" || true
	wait_for 60 sh -c "'$BIN' ls | grep '^$(printf %s "$ID" | cut -c1-12)' | grep -q idle" || true
	if [ "$verb" = approve ]; then
		if [ -e "$target-$verb" ]; then pass "approve lets the command run"; else fail "approve"; fi
	else
		if [ ! -e "$target-$verb" ]; then pass "$verb blocks the command"; else fail "$verb"; fi
	fi
	rm -f "$target-$verb"
}
approve_case approve
approve_case deny
approve_case timeout

# 4. With auto_review on, the hook stays silent and Codex's reviewer decides.
AUTO=$("$BIN" new -c 'approvals_reviewer="auto_review"')
settle "$AUTO" || true
"$BIN" send "$AUTO" "Run exactly this shell command and nothing else: touch $target-auto"
if wait_for 120 sh -c "'$BIN' events '$AUTO' --json | grep -q '\"turnEnded\"'"; then
	if has_event "$AUTO" approvalRequested; then fail "auto_review request was forwarded"; else pass "auto_review decides without forwarding"; fi
else
	fail "auto_review turn did not finish"
fi
rm -f "$target-auto"
"$BIN" stop "$AUTO"

# 6. Questions: answer from outside.
tmux -L "$TMUX_SOCK" send-keys -t "$(pane "$ID")" BTab
sleep 1
"$BIN" send "$ID" "Before planning anything, use the request_user_input tool to ask me one question: which language (Go or Python) should a hello-world script use. Ask only that."
if wait_for 60 sh -c "'$BIN' events '$ID' --json | grep -q questionAsked"; then
	qid=$(last_question "$ID")
	"$BIN" answer "$ID" "$qid" 2
	if wait_for 30 has_event "$ID" "questionAnswered"; then pass "question answered from outside"; else fail "answer"; fi
else
	fail "no question asked"
fi
tmux -L "$TMUX_SOCK" send-keys -t "$(pane "$ID")" BTab

# 7. Restart the daemon: sessions come back and events continue.
before=$("$BIN" ls | grep -c . || true)
last=$(events "$ID" | tail -1 | sed -n 's/.*"seq":"\{0,1\}\([0-9]*\).*/\1/p')
kill "$DAEMON_PID"; wait "$DAEMON_PID" 2>/dev/null || true
start_daemon 60s
after=$("$BIN" ls | grep -c . || true)
"$BIN" send "$ID" "Reply with exactly AFTER-RESTART"
if [ "$before" = "$after" ] && wait_for 60 has_event "$ID" "AFTER-RESTART"; then
	dups=$(events "$ID" | sed -n 's/.*"seq":"\{0,1\}\([0-9]*\).*/\1/p' | sort | uniq -d | wc -l | tr -d ' ')
	if [ "$dups" = 0 ]; then pass "restart keeps sessions; seq continues after $last"; else fail "duplicate seq after restart"; fi
else
	fail "restart"
fi

"$BIN" stop "$ID"
if wait_for 10 status_is "$ID" exited; then pass "stop ends the session"; else fail "stop"; fi

echo
if [ "$FAILS" = 0 ]; then echo "all checks passed"; else echo "$FAILS check(s) failed; daemon log: $STATE/daemon.log"; exit 1; fi
