#!/bin/sh
# Puts a released version live on our own deployment. This is the only way
# to deploy it: the parts go live in an order that keeps every client
# working, and each is checked to run the release before the next starts.
#
# Usage: scripts/ship.sh [--web-only] [vX.Y.Z]   (default: the version on origin/main)
#
# --web-only updates only Cloudflare Pages, verifies its production source
# through the API and refuses a protocol change. It does not contact or restart
# the server or daemon.
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
# Relays: step 2 upgrades each relay listed as "<ssh host> <url>" per line, from
# RA_RELAYS (newline separated) or the local deployment file
# ~/.config/remote-agent/relays; without either it ships
# root@relay.remote-agent.top. RA_WEB_URL is the web client as users reach it.
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
RELAYS=${RA_RELAYS:-}
if [ -z "$RELAYS" ] && [ -r "$HOME/.config/remote-agent/relays" ]; then
	RELAYS=$(grep -v '^[[:space:]]*\(#\|$\)' "$HOME/.config/remote-agent/relays")
fi
[ -n "$RELAYS" ] || RELAYS="root@relay.remote-agent.top https://relay.remote-agent.top"
WEB_URL=${RA_WEB_URL:-https://remote-agent.top}
# The relay the official web page offers by default: the public one.
WEB_DEFAULT_SERVER=${RA_WEB_DEFAULT_SERVER:-https://relay.remote-agent.top}
SHIP_SERVER=${RA_SHIP_SERVER:-auto}
BIN=${BIN:-$HOME/bin/remote-agent}
DIR=/opt/remote-agent
MODULE=github.com/Lewin671/remote-agent

say() { printf '\n== %s\n' "$*"; }
fail() {
	echo "ship: $*" >&2
	exit 1
}

web_only=false
if [ "${1:-}" = --web-only ]; then
	web_only=true
	shift
fi
[ "$#" -le 1 ] || fail "usage: $0 [--web-only] [vX.Y.Z]"

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
cloudflare_record=
cleanup() {
	[ -z "$work" ] || git -C "$ROOT" worktree remove --force "$work"
	[ -z "$cloudflare_record" ] || rm -f "$cloudflare_record"
	rmdir "$lock"
}
trap cleanup EXIT
trap 'exit 1' INT TERM

say "release $tag ($commit)"
run=$(gh run list --workflow release.yml --commit "$commit" --event push --limit 1 --json databaseId --jq '.[0].databaseId // empty')
[ -n "$run" ] || fail "no Release workflow ran for $tag"
# Wait for the publish job only: the iOS app tests that run beside it gate
# just the App Store upload, not what ships here.
publish_result() { gh run view "$run" --json jobs --jq '.jobs[] | select(.name == "publish") | .status + " " + .conclusion'; }
while :; do
	state=$(publish_result) || state=
	case "$state" in "completed "*) break ;; esac
	[ "$(gh run view "$run" --json status --jq .status)" != completed ] || break
	sleep 20
done
if [ "$state" = "completed success" ]; then
	echo "Release workflow published $tag"
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

if [ "$web_only" = false ]; then
# ship_server HOST URL: deploys the server to one relay unless it already runs
# this release or its code is unchanged since the deployed one.
ship_server() {
	HOST=$1
	SERVER_URL=$2
	say "server $SERVER_URL"
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
		[ "$(ssh "$HOST" "$DIR/remote-agent-server --version" | built_commit)" = "$commit" ] || fail "the server at $SERVER_URL is not $tag"
		ssh "$HOST" "cd $DIR && docker compose ps --status running --quiet | grep -q ." || fail "the server's container at $SERVER_URL is not running"
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
}
# All relays move before the web and the daemon, so a protocol change never
# leaves a client newer than the relay it talks to.
# Read the list on fd 3: ssh and deploy.sh read stdin and would swallow it.
while read -r relay_host relay_url <&3; do
	[ -n "$relay_host" ] && [ -n "$relay_url" ] || fail "each relay line needs \"<ssh host> <url>\""
	ship_server "$relay_host" "$relay_url"
done 3<<RELAYS_EOF
$RELAYS
RELAYS_EOF

fi

say "web"
# Web-only deployment uses control-plane verification when public HTTP access
# is restricted. The CLI reports failed/active stages by name, successful ones
# by age; the parser accepts only a successful production/main source.
cloudflare_source() {
	(cd "$work/web" && pnpm exec wrangler pages deployment list --project-name remote-agent --environment production --json) >"$cloudflare_record" || return 1
	python3 "$ROOT/scripts/web-deployment.py" --source <"$cloudflare_record"
}
commit_peer_protocol() {
	git -C "$ROOT" cat-file -e "$1^{commit}" 2>/dev/null || fail "commit $1 not found locally"
	# Pre-split commits used `const Version`; drop that pattern after the first
	# release containing internal/relayproto is live.
	git -C "$ROOT" show "$1:internal/protocol/protocol.go" | sed -n -e 's/^const PeerVersion = //p' -e 's/^const Version = //p'
}
commit_relay_protocol() {
	git -C "$ROOT" cat-file -e "$1^{commit}" 2>/dev/null || fail "commit $1 not found locally"
	if git -C "$ROOT" cat-file -e "$1:internal/relayproto/relayproto.go" 2>/dev/null; then
		git -C "$ROOT" show "$1:internal/relayproto/relayproto.go" | sed -n 's/^const Version = //p'
	else
		# Pre-split commits had no internal/relayproto; remove this fallback after
		# the first release containing internal/relayproto is live.
		commit_peer_protocol "$1"
	fi
}
if [ "$web_only" = true ]; then
	(cd "$work/web" && pnpm install --frozen-lockfile --prefer-offline --silent)
	cloudflare_record=$(mktemp "${TMPDIR:-/tmp}/remote-agent-web.XXXXXX")
	deployed_web=$(cloudflare_source) || fail "cannot verify the deployed web source through Cloudflare"
	previous_peer=$(commit_peer_protocol "$deployed_web")
	target_peer=$(commit_peer_protocol "$commit")
	previous_relay=$(commit_relay_protocol "$deployed_web")
	target_relay=$(commit_relay_protocol "$commit")
	[ -n "$previous_peer" ] && [ "$previous_peer" = "$target_peer" ] && [ -n "$previous_relay" ] && [ "$previous_relay" = "$target_relay" ] || fail "web-only deployment cannot change protocol; coordinate a full ship"
fi
# One of the page's scripts carries the commit it was built from.
web_has() {
	if [ "$web_only" = true ]; then
		cloudflare_source >/dev/null || return 1
		python3 "$ROOT/scripts/web-deployment.py" --commit "$1" <"$cloudflare_record"
		return
	fi
	page=$(curl -fsS --retry 2 --max-time 60 "$WEB_URL/?ship=$1") || return 1
	# Which chunk carries it is the bundler's choice, so look in every script the page names.
	for js in $(printf '%s' "$page" | grep -oE '/assets/[A-Za-z0-9_-]+\.js' | sort -u); do
		curl -fsS --retry 2 --max-time 180 "$WEB_URL$js" | grep -q "$1" && return 0
	done
	return 1
}
# Our own STUN host is not in this public repository: the web build reads it
# from the environment or ~/.config/remote-agent/direct-stun (one line, a
# comma-separated list of stun: URLs). Without it the web asks the relay's own.
stun_url=${RA_DIRECT_STUN_URL:-}
if [ -z "$stun_url" ] && [ -r "$HOME/.config/remote-agent/direct-stun" ]; then
	stun_url=$(tr -d '\r\n' <"$HOME/.config/remote-agent/direct-stun")
fi

# The check above only sees the commit, so a build input that changed outside
# it (the STUN list, for one) needs RA_WEB_REDEPLOY=1 to rebuild the same tag.
if [ "${RA_WEB_REDEPLOY:-}" != 1 ] && web_has "$commit"; then
	echo "skipped: already $tag"
else
	# Reuse the already-built web core WASM from ROOT when its inputs match the
	# shipped commit and are clean/current, avoiding a redundant WASM rebuild.
	if [ "${RA_CORE_FORCE:-}" != 1 ] &&
	   [ -s "$ROOT/build/core/web/core.wasm" ] && [ -f "$ROOT/build/core/web/wasm_exec.js" ] &&
	   git -C "$ROOT" diff --quiet "$commit" -- clientcore/ go.mod go.sum scripts/core/build.sh &&
	   [ -z "$(git -C "$ROOT" status --porcelain -- clientcore/ go.mod go.sum scripts/core/build.sh)" ] &&
	   [ ! "$ROOT/scripts/core/build.sh" -nt "$ROOT/build/core/web/core.wasm" ] &&
	   [ ! "$ROOT/go.mod" -nt "$ROOT/build/core/web/core.wasm" ] &&
	   [ ! "$ROOT/go.sum" -nt "$ROOT/build/core/web/core.wasm" ] &&
	   [ -z "$(find "$ROOT/clientcore" "$ROOT/proto" -type f \( -name '*.go' -o -name '*.proto' -o -name 'go.mod' -o -name 'go.sum' \) -newer "$ROOT/build/core/web/core.wasm" 2>/dev/null | head -n 1)" ]; then
		mkdir -p "$work/build/core/web"
		cp -f "$ROOT/build/core/web/core.wasm" "$ROOT/build/core/web/wasm_exec.js" "$work/build/core/web/"
		touch "$work/build/core/web/core.wasm"
	fi
	if [ "$web_only" = true ]; then
		(cd "$work/web" && VITE_DEFAULT_SERVER=$WEB_DEFAULT_SERVER VITE_DIRECT_STUN_URL=$stun_url pnpm run deploy)
	else
		(cd "$work/web" && pnpm install --frozen-lockfile --prefer-offline --silent && VITE_DEFAULT_SERVER=$WEB_DEFAULT_SERVER VITE_DIRECT_STUN_URL=$stun_url pnpm run deploy)
	fi
	i=0
	until web_has "$commit"; do
		i=$((i + 1))
		[ $i -lt 12 ] || fail "$WEB_URL does not serve $tag"
		sleep 5
	done
	if [ "$web_only" = true ]; then
		echo "Cloudflare production points to $tag"
	else
		echo "web serves $tag"
	fi
fi

if [ "$web_only" = true ]; then
	say "shipped web $tag"
	exit 0
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
