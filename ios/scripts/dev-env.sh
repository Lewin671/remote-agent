#!/bin/sh
# A throwaway relay on this machine for working on the iOS app, with an
# account already made on it, and optionally a daemon so that the app has
# a computer to list.
#
# Everything it makes is under one directory (RA_DEV_DIR, by default
# $TMPDIR/ra-ios-dev): the two binaries built from this checkout, the
# relay's database, and state directories of its own for the daemon and
# for every extra computer that joins. It never uses ~/.remote-agent, the
# tmux server you work in, or a relay other than its own; the commands
# below refuse to run without its environment, and `stop` kills only the
# processes whose IDs it wrote down.
#
#   dev-env.sh start [--daemon] [--port N]
#       builds, starts the relay on 127.0.0.1 (a free port unless given),
#       makes the account, and prints the server URL and how to join it.
#       --daemon also runs a daemon of the account, on its own tmux socket.
#   dev-env.sh secret        prints the account secret (ra1.…), alone, on stdout
#   dev-env.sh url           prints the server URL
#   dev-env.sh launch [udid] launches the debug app
#   dev-env.sh pair          pairing is started in the phone app (protocol 10); the
#                            command line only joins, so this prints how to do that
#   dev-env.sh join CODE     joins as another computer with the code the app's 添加设备 shows
#   dev-env.sh cli ARGS…     runs remote-agent with the daemon's environment (ls, new, send, …)
#   dev-env.sh status        says what is running
#   dev-env.sh stop          stops what `start` started and removes the directory
#
# The app reaches it at http://127.0.0.1:<port>: a simulator shares this
# machine's loopback, and debug builds allow plain HTTP to local addresses.
set -eu

repo=$(cd "$(dirname "$0")/../.." && pwd)
tmp=${TMPDIR:-/tmp}
dir=${RA_DEV_DIR:-${tmp%/}/ra-ios-dev}
dir=${dir%/}
bin=$dir/bin
bundle=io.github.lewin671.remoteagent.dev

die() {
	echo "dev-env.sh: $1" >&2
	exit 1
}

# The harness's own environment for remote-agent: its state directory and
# tmux socket, never the user's.
agent() {
	home=$1
	shift
	REMOTE_AGENT_HOME="$home" REMOTE_AGENT_TMUX_SOCKET="$(cat "$dir/tmux-socket")" "$bin/remote-agent" "$@"
}

running() {
	[ -f "$dir/$1.pid" ] && kill -0 "$(cat "$dir/$1.pid")" 2>/dev/null
}

need_started() {
	[ -f "$dir/url" ] && running server || die "not running; start it with: $0 start"
}

# Stops a process this script started: only if the ID still belongs to a
# binary under the harness's directory.
stop_one() {
	[ -f "$dir/$1.pid" ] || return 0
	pid=$(cat "$dir/$1.pid")
	if kill -0 "$pid" 2>/dev/null; then
		case $(ps -p "$pid" -o command= 2>/dev/null) in
		"$bin"/*) kill "$pid" 2>/dev/null || true ;;
		*) echo "dev-env.sh: process $pid is not the $1 started here; left alone" >&2 ;;
		esac
	fi
	rm -f "$dir/$1.pid"
}

start() {
	daemon=0
	port=
	while [ $# -gt 0 ]; do
		case $1 in
		--daemon) daemon=1 ;;
		--port)
			shift
			port=${1:?--port needs a number}
			;;
		*) die "unknown option for start: $1" ;;
		esac
		shift
	done
	if running server; then die "already running at $(cat "$dir/url"); stop it first: $0 stop"; fi
	command -v go >/dev/null 2>&1 || die "go is not installed"
	mkdir -p "$bin" "$dir/server" "$dir/home"
	chmod 700 "$dir"
	(cd "$repo" && go build -o "$bin/remote-agent-server" ./cmd/remote-agent-server && go build -o "$bin/remote-agent" ./cmd/remote-agent) \
		|| die "the build failed"
	if [ -z "$port" ]; then
		port=$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')
	fi
	url=http://127.0.0.1:$port
	token=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
	echo "ra-ios-dev-$port" >"$dir/tmux-socket"

	RA_SETUP_TOKEN=$token "$bin/remote-agent-server" --listen "127.0.0.1:$port" --db "$dir/server/relay.db" --allowed-origins "${RA_DEV_ALLOWED_ORIGINS:-}" \
		>"$dir/server.log" 2>&1 &
	echo $! >"$dir/server.pid"
	i=0
	until curl -fsS -o /dev/null "$url/healthz" 2>/dev/null; do
		i=$((i + 1))
		if [ $i -gt 50 ] || ! running server; then
			cat "$dir/server.log" >&2
			stop_one server
			die "the relay did not come up"
		fi
		sleep 0.1
	done
	echo "$url" >"$dir/url"

	RA_SETUP_TOKEN=$token RA_DEV_AUTHORITY=1 agent "$dir/home" account dev-init --server "$url" >"$dir/account.log" 2>&1 || {
		cat "$dir/account.log" >&2
		stop_one server
		die "the account could not be made"
	}

	if [ $daemon = 1 ]; then
		command -v tmux >/dev/null 2>&1 || die "tmux is not installed; the daemon needs it"
		# The daemon wants a codex to start sessions with. Without one the
		# computer still shows up in the app; its sessions cannot start.
		codex=$(command -v codex || true)
		[ -x "$codex" ] || codex=/usr/bin/true
		# exec: the ID written down is the daemon's own.
		(cd "$dir" && REMOTE_AGENT_HOME="$dir/home" REMOTE_AGENT_TMUX_SOCKET="$(cat "$dir/tmux-socket")" \
			exec "$bin/remote-agent" daemon --codex "$codex" >"$dir/daemon.log" 2>&1) &
		echo $! >"$dir/daemon.pid"
		i=0
		until [ -S "$dir/home/daemon.sock" ]; do
			i=$((i + 1))
			if [ $i -gt 100 ] || ! running daemon; then
				cat "$dir/daemon.log" >&2
				die "the daemon did not come up; the relay is still running ($0 stop)"
			fi
			sleep 0.1
		done
	fi

	echo "Relay:   $url"
	echo "Account: $(sed -n 's/.*this machine is \([0-9a-f]*\).*/\1/p' "$dir/account.log")  (the computer in $dir/home)"
	if [ $daemon = 1 ]; then echo "Daemon:  running, tmux socket $(cat "$dir/tmux-socket")"; fi
	echo
	echo "Join from the app (debug build):"
	echo "  $0 launch                 # launches it on the booted simulator; then use pair"
	echo "  $0 secret                 # offline test-account backup only; cannot enroll a device"
	echo "  $0 pair                   # a pairing code; then launch with -joinServer $url -joinCode <code>"
	echo "Stop:    $0 stop"
}

case ${1:-} in
start)
	shift
	start "$@"
	;;
secret)
	need_started
	agent "$dir/home" account secret 2>/dev/null
	;;
url)
	need_started
	cat "$dir/url"
	;;
launch)
	need_started
	udid=${2:-booted}
	xcrun simctl terminate "$udid" "$bundle" >/dev/null 2>&1 || true
	xcrun simctl launch "$udid" "$bundle"
    die "pairing is started in the phone app; this test account has no phone yet (the app is protocol 9 until P4-P6)"
	;;
pair)
	need_started
	die "pairing is started in the phone app; this test account has no phone yet (the app is protocol 9 until P4-P6)"
	;;
join)
	need_started
	code=${2:?usage: $0 join CODE}
	# Each computer that joins has a state directory of its own.
	n=1
	while [ -e "$dir/joined-$n" ]; do n=$((n + 1)); done
	mkdir -p "$dir/joined-$n"
	agent "$dir/joined-$n" account join --server "$(cat "$dir/url")" --code "$code"
	;;
cli)
	need_started
	shift
	agent "$dir/home" "$@"
	;;
status)
	if running server; then
		echo "relay   running at $(cat "$dir/url") (pid $(cat "$dir/server.pid"))"
	else
		echo "relay   not running"
	fi
	if running daemon; then
		echo "daemon  running (pid $(cat "$dir/daemon.pid"), tmux socket $(cat "$dir/tmux-socket"))"
	else
		echo "daemon  not running"
	fi
	;;
stop)
	[ -d "$dir" ] || exit 0
	stop_one daemon
	stop_one server
	# Sessions the daemon started live in the harness's own tmux server.
	if [ -f "$dir/tmux-socket" ] && command -v tmux >/dev/null 2>&1; then
		tmux -L "$(cat "$dir/tmux-socket")" kill-server 2>/dev/null || true
	fi
	# Only a directory this script made is removed.
	if [ -d "$bin" ] && [ -d "$dir/server" ]; then rm -rf "$dir"; fi
	;;
*)
	sed -n '2,/^set -eu/p' "$0" | sed -e '$d' -e 's/^# \{0,1\}//'
	exit 2
	;;
esac
