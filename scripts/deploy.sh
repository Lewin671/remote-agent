#!/bin/sh
# Deploys the server, the web client, or both.
#
# Usage: scripts/deploy.sh [server|web|all]   (default: all)
#
# RA_DEPLOY_HOST is the server's ssh host (default root@ra.remote-agent.top);
# VITE_DEFAULT_SERVER is the server the web client offers by default.
# Deploy the server first: the web client may rely on what it adds.
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
HOST=${RA_DEPLOY_HOST:-root@ra.remote-agent.top}
what=${1:-all}

case $what in
server | web | all) ;;
*)
	echo "usage: $0 [server|web|all]" >&2
	exit 2
	;;
esac
if [ "$what" != web ]; then
	"$ROOT/deploy/deploy.sh" "$HOST"
fi
if [ "$what" != server ]; then
	(cd "$ROOT/web" && pnpm install --frozen-lockfile --silent && pnpm run deploy)
fi
