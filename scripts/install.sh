#!/bin/sh
# Builds remote-agent into $BIN (default ~/bin/remote-agent) and restarts
# the daemon service if scripts/daemon.sh installed one.
#
# Usage: scripts/install.sh
#
# The new binary replaces the old one by rename, so sessions whose PTY
# host still runs the old binary are not disturbed.
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
BIN=${BIN:-$HOME/bin/remote-agent}
mkdir -p "$(dirname "$BIN")"
tmp=$BIN.new.$$
trap 'rm -f "$tmp"' EXIT
(cd "$ROOT" && go build -o "$tmp" ./cmd/remote-agent)
mv -f "$tmp" "$BIN"
echo "installed $BIN"

case $(uname) in
Darwin) unit=$HOME/Library/LaunchAgents/com.github.lewin671.remote-agent.plist ;;
*) unit=$HOME/.config/systemd/user/remote-agent.service ;;
esac
if [ -f "$unit" ]; then
	BIN=$BIN "$ROOT/scripts/daemon.sh" restart
	echo "restarted the daemon"
elif "$BIN" ls >/dev/null 2>&1; then
	echo "a daemon started by hand is running the old binary: restart it, or run scripts/daemon.sh install to have it managed"
fi
