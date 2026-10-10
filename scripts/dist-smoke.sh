#!/bin/sh
# Installs from the archives in dist/ the way a stranger would, with the
# public install.sh and nothing else of this checkout, and checks that the
# result works: the command runs, the web client has no server in it, and on
# Linux the packaged relay starts, takes an account from the packaged daemon
# and builds into its image.
#
# Usage: scripts/dist-smoke.sh      (after scripts/build-dist.sh)
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
tag=v$(sed -n 's/^version=//p' "$ROOT/version.properties")
work=$(mktemp -d)
relay=
cleanup() {
	[ -z "$relay" ] || kill "$relay" 2>/dev/null || true
	rm -rf "$work" 2>/dev/null || sudo rm -rf "$work"
}
trap cleanup EXIT

# The published layout, served from disk.
mkdir -p "$work/releases/latest/download" "$work/releases/download/$tag" "$work/home"
cp "$ROOT"/dist/remote-agent-*"$tag"*.tar.gz "$work/releases/download/$tag/"
(cd "$work/releases/download/$tag" && if command -v sha256sum >/dev/null; then sha256sum ./*.tar.gz; else shasum -a 256 ./*.tar.gz; fi | sed 's|  \./|  |' >SHA256SUMS)
echo "version=${tag#v}" >"$work/releases/latest/download/latest.properties"
install() {
	(cd "$work" && HOME="$work/home" REMOTE_AGENT_RELEASES="file://$work/releases" sh "$ROOT/deploy/releases-repo/install.sh" "$@")
}

install
agent="$work/home/.local/bin/remote-agent"
"$agent" --version
[ -x "$work/home/.local/bin/remote-agent-service" ]

install web
[ -f "$work/remote-agent-web/index.html" ] && [ -f "$work/remote-agent-web/_headers" ]
# The only server a public page may name is the placeholder in its form.
if grep -rhoE 'https://[a-zA-Z0-9.-]+' "$work/remote-agent-web/assets" | grep -vE '^https://(ra\.example\.com|react\.dev)$' | grep .; then
	echo "the web client names a server" >&2
	exit 1
fi
# A second run replaces the copy; a directory of something else is refused.
install web
mkdir "$work/other" && echo keep >"$work/other/file"
if install web other 2>/dev/null; then
	echo "install.sh replaced a directory that is not the web client" >&2
	exit 1
fi

if [ "$(uname -s)" != Linux ]; then
	echo "dist-smoke: the relay is Linux only; skipped here"
	exit 0
fi

install server srv
[ -x "$work/srv/remote-agent-server" ]
[ "$(stat -c %a "$work/srv/.env")" = 600 ]
token=$(sed -n 's/^RA_SETUP_TOKEN=//p' "$work/srv/.env")
[ ${#token} -ge 24 ]
# An upgrade keeps what was edited, and works once the data directory has
# been handed to the relay's user as the first run asks.
echo '# mine' >>"$work/srv/compose.yaml"
if sudo -n true 2>/dev/null; then
	sudo chown 65534:65534 "$work/srv/data"
fi
install server srv
grep -qx '# mine' "$work/srv/compose.yaml"
[ "$(sed -n 's/^RA_SETUP_TOKEN=//p' "$work/srv/.env")" = "$token" ]

port=$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')
RA_SETUP_TOKEN=$token "$work/srv/remote-agent-server" --listen "127.0.0.1:$port" --db "$work/relay.db" >"$work/relay.log" 2>&1 &
relay=$!
i=0
until curl -fsS -o /dev/null "http://127.0.0.1:$port/healthz" 2>/dev/null; do
	i=$((i + 1))
	if [ $i -gt 50 ]; then
		cat "$work/relay.log" >&2
		exit 1
	fi
	sleep 0.1
done
# A wrong token is refused, the one install.sh made creates the account.
if RA_SETUP_TOKEN=wrong REMOTE_AGENT_HOME="$work/state-wrong" "$agent" account init --server "http://127.0.0.1:$port" >/dev/null 2>&1; then
	echo "the relay made an account without its setup token" >&2
	exit 1
fi
RA_SETUP_TOKEN=$token REMOTE_AGENT_HOME="$work/state" "$agent" account init --server "http://127.0.0.1:$port"
REMOTE_AGENT_HOME="$work/state" "$agent" account status

if command -v docker >/dev/null 2>&1; then
	sed -i 's|^RELAY_DOMAIN=.*|RELAY_DOMAIN=ra.example.com|; s|^ALLOWED_ORIGINS=.*|ALLOWED_ORIGINS=https://app.example.com|' "$work/srv/.env"
	(cd "$work/srv" && docker compose config --quiet && docker compose build --quiet remote-agent-server)
	docker run --rm remote-agent-server:local --version
	docker image rm remote-agent-server:local >/dev/null
fi
echo "dist-smoke: ok"
