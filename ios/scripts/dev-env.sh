#!/bin/sh
# A throwaway relay on this machine for working on the clients, with an
# account already made on it, and optionally a daemon so that the app has
# a computer to list. Or, as protocol 10 has it, a relay without an account:
# the Android phone app (the admin phone) makes it with the setup token and
# shows a pairing code under 设置 › 添加设备, and this computer joins with it.
#
# Everything it makes is under one directory (RA_DEV_DIR, by default
# $TMPDIR/ra-ios-dev): the two binaries built from this checkout, the
# relay's database, and state directories of its own for the daemon and
# for every extra computer that joins. It never uses ~/.remote-agent, the
# tmux server you work in, or a relay other than its own; the commands
# below refuse to run without its environment, and `stop` kills only the
# processes whose IDs it wrote down.
#
#   dev-env.sh start [--daemon | --no-account] [--port N]
#       builds, starts the relay on 127.0.0.1 (a free port unless given),
#       makes the account, and prints the server URL and how to join it.
#       --daemon also runs a daemon of the account, on its own tmux socket.
#       --no-account makes none: the Android phone app makes it with the
#       setup token (`token`), then this computer joins (`join CODE --daemon`).
#   dev-env.sh secret        prints the account secret (ra1.…), alone, on stdout
#   dev-env.sh url           prints the server URL
#   dev-env.sh token         prints the relay's setup token, for making the account in the Android app
#   dev-env.sh launch [udid] launches the debug app
#   dev-env.sh pair          says how pairing goes: it is started on the Android admin phone
#   dev-env.sh join CODE [--daemon]
#       joins with the code the Android app's 设置 › 添加设备 shows: as this
#       computer when `start --no-account` made none, else as another one;
#       --daemon then runs its daemon, on a tmux socket of its own
#   dev-env.sh cli ARGS…     runs remote-agent with the daemon's environment (ls, new, send, …)
#   dev-env.sh status        says what is running
#   dev-env.sh stop          stops what `start` and `join` started and removes the directory
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

# Runs a daemon for the computer in $1, writing its ID to $2.pid, on the
# tmux socket named in the file $3.
run_daemon() {
	home=$1 name=$2 socket=$3
	command -v tmux >/dev/null 2>&1 || die "tmux is not installed; the daemon needs it"
	# The daemon wants a codex to start sessions with. Without one the
	# computer still shows up in the app; its sessions cannot start.
	codex=$(command -v codex || true)
	[ -x "$codex" ] || codex=/usr/bin/true
	# exec: the ID written down is the daemon's own.
	(cd "$dir" && REMOTE_AGENT_HOME="$home" REMOTE_AGENT_TMUX_SOCKET="$(cat "$socket")" \
		exec "$bin/remote-agent" daemon --codex "$codex" >"$dir/$name.log" 2>&1) &
	echo $! >"$dir/$name.pid"
	i=0
	until [ -S "$home/daemon.sock" ]; do
		i=$((i + 1))
		if [ $i -gt 100 ] || ! running "$name"; then
			cat "$dir/$name.log" >&2
			die "the daemon did not come up; the relay is still running ($0 stop)"
		fi
		sleep 0.1
	done
}

start() {
	daemon=0
	account=1
	port=
	while [ $# -gt 0 ]; do
		case $1 in
		--daemon) daemon=1 ;;
		--no-account) account=0 ;;
		--port)
			shift
			port=${1:?--port needs a number}
			;;
		*) die "unknown option for start: $1" ;;
		esac
		shift
	done
	[ $daemon = 1 ] && [ $account = 0 ] && die "--daemon needs the account; with --no-account run the daemon with: $0 join CODE --daemon"
	if running server; then die "already running at $(cat "$dir/url"); stop it first: $0 stop"; fi
	command -v go >/dev/null 2>&1 || die "go is not installed"
	mkdir -p "$bin" "$dir/server" "$dir/home"
	chmod 700 "$dir"
	(cd "$repo" && go build -o "$bin/remote-agent-server" ./cmd/remote-agent-server && go build -tags dev -o "$bin/remote-agent" ./cmd/remote-agent) \
		|| die "the build failed"
	if [ -z "$port" ]; then
		port=$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')
	fi
	url=http://127.0.0.1:$port
	token=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
	# Kept for the Android app, which makes the account with it.
	(umask 077 && echo "$token" >"$dir/setup-token")
	echo "ra-ios-dev-$port" >"$dir/tmux-socket"

	# A few accounts, so the phone can make its own beside the one made here.
	RA_SETUP_TOKEN=$token "$bin/remote-agent-server" --listen "127.0.0.1:$port" --db "$dir/server/relay.db" --max-accounts 10 --allowed-origins "${RA_DEV_ALLOWED_ORIGINS:-}" \
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

	if [ $account = 1 ]; then
		RA_SETUP_TOKEN=$token RA_DEV_AUTHORITY=1 agent "$dir/home" account dev-init --server "$url" >"$dir/account.log" 2>&1 || {
			cat "$dir/account.log" >&2
			stop_one server
			die "the account could not be made"
		}
		touch "$dir/home/.dev-account"
	fi

	if [ $daemon = 1 ]; then run_daemon "$dir/home" daemon "$dir/tmux-socket"; fi

	echo "Relay:   $url"
	if [ $account = 1 ]; then
		echo "Account: $(sed -n 's/.*this machine is \([0-9a-f]*\).*/\1/p' "$dir/account.log")  (the computer in $dir/home)"
	else
		echo "Account: none yet; setup token $token ($0 token)"
	fi
	if [ $daemon = 1 ]; then echo "Daemon:  running, tmux socket $(cat "$dir/tmux-socket")"; fi
	echo
	echo "Pairing starts on the Android admin phone (protocol 10):"
	if [ $account = 0 ]; then
		echo "  1. in the Android debug app, make the account on $url with the setup token above"
		echo "     (an emulator reaches this machine as 10.0.2.2: adb reverse tcp:$port tcp:$port keeps 127.0.0.1)"
		echo "  2. 设置 › 添加设备 there shows a pairing code"
		echo "  3. $0 join CODE --daemon   # this computer joins and its daemon runs"
	else
		echo "  this account was made here, so it has no admin phone and cannot pair; for that, start with --no-account"
	fi
	echo "  the iOS app joins with a code from the admin phone: $0 launch, then -joinServer $url -joinCode CODE"
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
token)
	need_started
	cat "$dir/setup-token"
	;;
launch)
	need_started
	udid=${2:-booted}
	xcrun simctl terminate "$udid" "$bundle" >/dev/null 2>&1 || true
	xcrun simctl launch "$udid" "$bundle"
	echo "The app joins with a pairing code from the Android admin phone (设置 › 添加设备):" >&2
	echo "  xcrun simctl launch $udid $bundle -joinServer $(cat "$dir/url") -joinCode CODE" >&2
	;;
pair)
	need_started
	die "pairing is started on the Android admin phone (protocol 10): make the account there with \`$0 token\` (start --no-account), open 设置 › 添加设备 for a code, then: $0 join CODE --daemon"
	;;
join)
	need_started
	code=${2:?usage: $0 join CODE [--daemon]}
	withdaemon=0
	if [ "${3:-}" = --daemon ]; then withdaemon=1; fi
	if [ ! -e "$dir/home/.dev-account" ]; then
		# start --no-account: this is the computer that joins.
		agent "$dir/home" account join --server "$(cat "$dir/url")" --code "$code"
		touch "$dir/home/.dev-account"
		if [ $withdaemon = 1 ]; then run_daemon "$dir/home" daemon "$dir/tmux-socket"; fi
	else
		# Each other computer that joins has a state directory and tmux socket of its own.
		n=1
		while [ -e "$dir/joined-$n" ]; do n=$((n + 1)); done
		mkdir -p "$dir/joined-$n"
		agent "$dir/joined-$n" account join --server "$(cat "$dir/url")" --code "$code"
		echo "$(cat "$dir/tmux-socket")-$n" >"$dir/joined-$n/tmux-socket"
		if [ $withdaemon = 1 ]; then run_daemon "$dir/joined-$n" "daemon-joined-$n" "$dir/joined-$n/tmux-socket"; fi
	fi
	if [ $withdaemon = 1 ]; then echo "Daemon:  running"; fi
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
	for f in "$dir"/daemon-joined-*.pid; do
		[ -f "$f" ] || continue
		name=$(basename "$f" .pid)
		if running "$name"; then echo "$name  running (pid $(cat "$f"))"; else echo "$name  not running"; fi
	done
	;;
stop)
	[ -d "$dir" ] || exit 0
	for f in "$dir"/daemon-joined-*.pid; do
		if [ -f "$f" ]; then stop_one "$(basename "$f" .pid)"; fi
	done
	stop_one daemon
	stop_one server
	# Sessions the daemons started live in the harness's own tmux servers.
	if command -v tmux >/dev/null 2>&1; then
		for f in "$dir/tmux-socket" "$dir"/joined-*/tmux-socket; do
			if [ -f "$f" ]; then tmux -L "$(cat "$f")" kill-server 2>/dev/null || true; fi
		done
	fi
	# Only a directory this script made is removed.
	if [ -d "$bin" ] && [ -d "$dir/server" ]; then rm -rf "$dir"; fi
	;;
*)
	sed -n '2,/^set -eu/p' "$0" | sed -e '$d' -e 's/^# \{0,1\}//'
	exit 2
	;;
esac
