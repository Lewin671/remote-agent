#!/bin/sh
# Real agy acceptance on a throwaway relay, account, state and tmux socket.
# --preflight performs no model requests. Full runs spend a little quota.
# RA_AGY_MODEL defaults to gemini-3.8-flash; effort is always low.
# Commands travel through an encrypted synthetic client, not the product UI.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
RA_DEV_DIR=$(mktemp -d "/tmp/ra-agy-acc.XXXXXX")
export RA_DEV_DIR
cleanup() { "$ROOT/ios/scripts/dev-env.sh" stop >/dev/null 2>&1 || true; }
trap cleanup EXIT
trap 'exit 130' INT TERM
"$ROOT/ios/scripts/dev-env.sh" start --no-account >"$RA_DEV_DIR/start.log" 2>&1
cd "$ROOT"
go run ./scripts/acceptance-agy "$@"
