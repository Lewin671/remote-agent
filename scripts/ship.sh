#!/bin/sh
# Puts a released version live on our own deployment. This is the only way
# to deploy it: the parts go live in an order that keeps every client
# working, and each is checked to run the release before the next starts.
#
# Usage: scripts/ship.sh [vX.Y.Z]   (default: the version on origin/main)
#
# 1. waits for the tag's Release workflow and refuses a release that failed
# 2. the server, if its code changed since the deployed one
#    (RA_SHIP_SERVER=always|never|auto, default auto)
# 3. the web client
# 4. this computer's daemon
#
# Everything is built from a clean checkout of the tag. A part already
# running the release is skipped, so after a failure run it again.
# The App is not shipped: it updates itself from the public release.
#
# RA_DEPLOY_HOST is the server's ssh host (default root@ra.remote-agent.top),
# RA_SERVER_URL the server and RA_WEB_URL the web client as users reach them.
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
HOST=${RA_DEPLOY_HOST:-root@ra.remote-agent.top}
SERVER_URL=${RA_SERVER_URL:-https://ra.remote-agent.top}
WEB_URL=${RA_WEB_URL:-https://remote-agent.top}
SHIP_SERVER=${RA_SHIP_SERVER:-auto}
BIN=${BIN:-$HOME/bin/remote-agent}
DIR=/opt/remote-agent
MODULE=github.com/Lewin671/remote-agent

say() { printf '\n== %s\n' "$*"; }
fail() {
	echo "ship: $*" >&2
	exit 1
}

case $SHIP_SERVER in
always | never | auto) ;;
*) fail "RA_SHIP_SERVER must be always, never or auto" ;;
esac

git -C "$ROOT" fetch -q origin main --tags
tag=${1:-v$(git -C "$ROOT" show origin/main:version.properties | sed -n 's/^version=//p')}
commit=$(git -C "$ROOT" rev-parse -q --verify "refs/tags/$tag^{commit}") || fail "no tag $tag"
git -C "$ROOT" merge-base --is-ancestor "$commit" origin/main || fail "$tag is not on origin/main"

# Two ships at once would interleave their parts.
lock=${TMPDIR:-/tmp}/remote-agent-ship.lock
mkdir "$lock" 2>/dev/null || fail "another ship is running (or remove $lock if none is)"
work=
cleanup() {
	[ -z "$work" ] || git -C "$ROOT" worktree remove --force "$work"
	rmdir "$lock"
}
trap cleanup EXIT
trap 'exit 1' INT TERM

say "release $tag ($commit)"
run=$(gh run list --workflow release.yml --commit "$commit" --event push --limit 1 --json databaseId --jq '.[0].databaseId // empty')
[ -n "$run" ] || fail "no Release workflow ran for $tag"
if gh run watch "$run" --exit-status >/dev/null; then
	echo "Release workflow succeeded"
# A failed run can be retried by hand for the same tag; only a run whose
# checks all passed publishes the release.
elif [ "$(gh release view "$tag" --json isDraft --jq .isDraft 2>/dev/null)" = false ]; then
	echo "Release workflow failed, but a retry published $tag"
else
	fail "the Release workflow of $tag did not succeed and no retry published it: gh run view $run"
fi

work=$(mktemp -d "${TMPDIR:-/tmp}/remote-agent-ship.XXXXXX")
rmdir "$work"
git -C "$ROOT" worktree add -q --detach "$work" "$commit"

# The commit a build reports in "name version (commit)".
built_commit() { sed -n 's/.*(\([0-9a-f]\{40\}\)).*/\1/p'; }

say "server"
deployed=$(ssh "$HOST" "$DIR/remote-agent-server --version" 2>/dev/null | built_commit) || deployed=
deploy_server=$SHIP_SERVER
if [ "$deploy_server" = auto ]; then
	deploy_server=always
	if [ "$deployed" = "$commit" ]; then
		deploy_server=never
	elif [ -n "$deployed" ] && git -C "$ROOT" cat-file -e "$deployed^{commit}" 2>/dev/null; then
		# What the server is built from: its packages in this module, the
		# module files and the deployment itself.
		inputs=$(cd "$work" && go list -deps -f '{{if .Module}}{{if .Module.Main}}{{.ImportPath}}{{end}}{{end}}' ./cmd/remote-agent-server | sed -n "s|^$MODULE/||p")
		# shellcheck disable=SC2086
		if git -C "$work" diff --quiet "$deployed" "$commit" -- $inputs go.mod go.sum deploy/Dockerfile deploy/compose.yaml deploy/deploy.sh scripts/build-go.sh; then
			deploy_server=never
		fi
	fi
fi
if [ "$deploy_server" = always ]; then
	"$work/deploy/deploy.sh" "$HOST" "$DIR"
	[ "$(ssh "$HOST" "$DIR/remote-agent-server --version" | built_commit)" = "$commit" ] || fail "the server's binary is not $tag"
	ssh "$HOST" "cd $DIR && docker compose ps --status running --quiet | grep -q ." || fail "the server's container is not running"
	i=0
	until code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$SERVER_URL/") && [ "$code" -gt 0 ] && [ "$code" -lt 500 ]; do
		i=$((i + 1))
		[ $i -lt 12 ] || fail "$SERVER_URL does not answer (last status $code)"
		sleep 5
	done
	echo "server runs $tag"
else
	echo "skipped: the server's code is unchanged since the deployed ${deployed:-?}"
fi

say "web"
# The page's main script carries the commit it was built from.
web_has() {
	page=$(curl -fsS --retry 2 --max-time 60 "$WEB_URL/?ship=$1") || return 1
	for js in $(printf '%s' "$page" | grep -oE '/assets/index-[A-Za-z0-9_-]+\.js'); do
		curl -fsS --retry 2 --max-time 180 "$WEB_URL$js" | grep -q "$1" && return 0
	done
	return 1
}
if web_has "$commit"; then
	echo "skipped: already $tag"
else
	(cd "$work/web" && pnpm install --frozen-lockfile --silent && VITE_DEFAULT_SERVER=$SERVER_URL pnpm run deploy)
	i=0
	until web_has "$commit"; do
		i=$((i + 1))
		[ $i -lt 12 ] || fail "$WEB_URL does not serve $tag"
		sleep 5
	done
	echo "web serves $tag"
fi

say "daemon"
# Field of the running daemon in "update status".
daemon_field() {
	"$BIN" update status 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["daemon"].get(sys.argv[1], ""))' "$1" 2>/dev/null
}
version=$(sed -n 's/^version=//p' "$work/version.properties")
if [ "$(daemon_field commit)" = "$commit" ] && [ "$(daemon_field version)" = "$version" ]; then
	echo "skipped: already $tag"
else
	BIN=$BIN "$work/scripts/install.sh"
	i=0
	until [ "$(daemon_field commit)" = "$commit" ] && [ "$(daemon_field version)" = "$version" ]; do
		i=$((i + 1))
		[ $i -lt 30 ] || fail "the running daemon is not $tag: $BIN update status"
		sleep 1
	done
	echo "daemon runs $tag"
fi

say "shipped $tag"
