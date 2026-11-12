#!/bin/sh
# e2e-down.sh: stops and cleans up an isolated remote-agent environment
# (kills web server, stops daemon, stops relay, kills tmux server, removes dir).
set -eu

repo=$(cd "$(dirname "$0")/../.." && pwd)
dev_env="$repo/ios/scripts/dev-env.sh"
pointer="${TMPDIR:-/tmp}/.ra-dev-last-dir"

dir="${1:-}"
if [ -z "$dir" ] && [ -n "${RA_DEV_DIR:-}" ]; then
	dir="$RA_DEV_DIR"
fi
if [ -z "$dir" ] && [ -f "$pointer" ]; then
	dir=$(cat "$pointer" 2>/dev/null || true)
fi

if [ -z "$dir" ] || [ ! -d "$dir" ]; then
	echo "e2e-down.sh: no active environment directory specified or found" >&2
	exit 0
fi

echo "==> Stopping isolated environment in $dir..."

# 1. Stop web dev server if running
if [ -f "$dir/web.pid" ]; then
	web_pid=$(cat "$dir/web.pid" 2>/dev/null || true)
	if [ -n "$web_pid" ] && kill -0 "$web_pid" 2>/dev/null; then
		kill "$web_pid" 2>/dev/null || true
	fi
	rm -f "$dir/web.pid"
fi

# 2. Stop relay and daemon via dev-env.sh
if [ -x "$dev_env" ]; then
	RA_DEV_DIR="$dir" "$dev_env" stop >/dev/null 2>&1 || true
fi

# 3. Clean up directory if still present
if [ -d "$dir" ]; then
	rm -rf "$dir"
fi

# 4. Clean up pointer file if it matches
if [ -f "$pointer" ]; then
	last_dir=$(cat "$pointer" 2>/dev/null || true)
	if [ "$last_dir" = "$dir" ]; then
		rm -f "$pointer"
	fi
fi

echo "==> Environment at $dir stopped and cleaned up."
