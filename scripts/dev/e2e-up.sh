#!/bin/sh
# e2e-up.sh: starts a fully-configured isolated remote-agent dev environment
# (relay server, initialized dev account, running daemon, and optional Vite web server).
set -eu

repo=$(cd "$(dirname "$0")/../.." && pwd)
dev_env="$repo/ios/scripts/dev-env.sh"
pointer="${TMPDIR:-/tmp}/.ra-dev-last-dir"

die() {
	echo "e2e-up.sh: $1" >&2
	exit 1
}

# Subcommand dispatch if environment is already running
case "${1:-}" in
cli)
	shift
	dir="${RA_DEV_DIR:-}"
	if [ -z "$dir" ] && [ -f "$pointer" ]; then dir=$(cat "$pointer"); fi
	[ -n "$dir" ] && [ -d "$dir" ] || die "no running environment found; start with: $0"
	RA_DEV_DIR="$dir" exec "$dev_env" cli "$@"
	;;
status)
	dir="${RA_DEV_DIR:-}"
	if [ -z "$dir" ] && [ -f "$pointer" ]; then dir=$(cat "$pointer"); fi
	[ -n "$dir" ] && [ -d "$dir" ] || die "no running environment found; start with: $0"
	RA_DEV_DIR="$dir" exec "$dev_env" status
	;;
esac

# Options
port=""
with_web=1
custom_dir=""

while [ $# -gt 0 ]; do
	case $1 in
	--port)
		shift
		port=${1:?--port requires a port number}
		;;
	--dir)
		shift
		custom_dir=${1:?--dir requires a directory path}
		;;
	--no-web)
		with_web=0
		;;
	-h|--help)
		echo "Usage: $0 [--dir PATH] [--port PORT] [--no-web]"
		echo "       $0 cli ARGS...    (run CLI command in active environment)"
		echo "       $0 status         (show environment status)"
		exit 0
		;;
	*)
		die "unknown argument: $1"
		;;
	esac
	shift
done

# Clean polluting environment variables
unset REMOTE_AGENT_HOME REMOTE_AGENT_TMUX_SOCKET REMOTE_AGENT_SERVER REMOTE_AGENT_CODEX RA_DEV_AUTHORITY || true

# Determine directory (ensure <= 70 chars for AF_UNIX socket bounds)
if [ -n "$custom_dir" ]; then
	dir="$custom_dir"
elif [ -n "${RA_DEV_DIR:-}" ]; then
	dir="$RA_DEV_DIR"
else
	# Unique, short directory under /tmp
	rand_suffix=$(python3 -c 'import secrets; print(secrets.token_hex(4))')
	dir="/tmp/rad-$rand_suffix"
fi

if [ ${#dir} -gt 70 ]; then
	die "directory path '$dir' is too long (${#dir} chars > 70 chars); risk of AF_UNIX 104-byte overflow"
fi

echo "==> [1/3] Starting isolated relay & daemon in $dir..."
start_args="--daemon"
if [ -n "$port" ]; then
	start_args="$start_args --port $port"
fi

# Set allowed origins for web access
export RA_DEV_ALLOWED_ORIGINS="http://localhost:*,http://127.0.0.1:*"
export RA_DEV_DIR="$dir"

# Start dev-env
if ! "$dev_env" start $start_args >"$dir.start.log" 2>&1; then
	cat "$dir.start.log" >&2
	rm -f "$dir.start.log"
	die "Step 1/3 failed: relay or daemon failed to start"
fi
rm -f "$dir.start.log"

url=$(cat "$dir/url")
account_id=$(sed -n 's/.*this machine is \([0-9a-f]*\).*/\1/p' "$dir/account.log" 2>/dev/null || echo "unknown")
echo "$dir" > "$pointer"

echo "==> [2/3] Verifying relay health and daemon socket..."
if ! curl -fsS -o /dev/null "$url/healthz" 2>/dev/null; then
	"$repo/scripts/dev/e2e-down.sh" "$dir" >/dev/null 2>&1 || true
	die "Step 2/3 failed: relay /healthz did not respond at $url"
fi

if [ ! -S "$dir/home/daemon.sock" ]; then
	"$repo/scripts/dev/e2e-down.sh" "$dir" >/dev/null 2>&1 || true
	die "Step 2/3 failed: daemon socket $dir/home/daemon.sock not found"
fi

web_url=""
if [ $with_web -eq 1 ] && command -v pnpm >/dev/null 2>&1 && [ -d "$repo/web" ]; then
	echo "==> [3/3] Starting Vite web client..."
	web_log="$dir/web.log"
	web_pid=$(python3 -c '
import subprocess, os, sys
env = os.environ.copy()
env["RA_PROXY"] = sys.argv[2]
p = subprocess.Popen(["pnpm", "--dir", sys.argv[1], "exec", "vite", "--host", "127.0.0.1", "--port", "0"],
                     stdin=subprocess.DEVNULL, stdout=open(sys.argv[3], "w"), stderr=subprocess.STDOUT,
                     env=env, start_new_session=True)
print(p.pid)
' "$repo/web" "$url" "$web_log")
	echo "$web_pid" > "$dir/web.pid"

	# Wait for Vite to bind and print Local: http://127.0.0.1:<port>
	i=0
	while [ $i -lt 50 ]; do
		if [ -f "$web_log" ]; then
			found_url=$(sed -n 's/.*Local: *\(http:\/\/127\.0\.0\.1:[0-9]*\/\).*/\1/p' "$web_log" | tail -1)
			if [ -n "$found_url" ]; then
				web_url="${found_url%/}"
				if curl -fsS -o /dev/null "$web_url" 2>/dev/null; then
					echo "$web_url" > "$dir/web.url"
					break
				fi
			fi
		fi
		if ! kill -0 "$web_pid" 2>/dev/null; then
			echo "warning: web dev server exited early; see $web_log" >&2
			break
		fi
		sleep 0.1
		i=$((i + 1))
	done
else
	echo "==> [3/3] Skipping web client (disabled or pnpm unavailable)"
fi

echo
echo "============================================================"
echo "  remote-agent isolated e2e environment is UP"
echo "============================================================"
echo "  Directory:  $dir"
echo "  Relay URL:  $url"
if [ -n "$web_url" ]; then
	echo "  Web Client: $web_url"
	echo "  Demo Mode:  $web_url/?demo"
fi
echo "  Account:    $account_id"
echo "  Tmux Sock:  $(cat "$dir/tmux-socket")"
echo
echo "CLI usage:"
echo "  scripts/dev/e2e-up.sh cli ls"
echo "  scripts/dev/e2e-up.sh cli new"
echo "  # or with dev-env.sh:"
echo "  RA_DEV_DIR=$dir ios/scripts/dev-env.sh cli ls"
echo
echo "Stop & cleanup:"
echo "  scripts/dev/e2e-down.sh $dir"
echo "============================================================"
