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
BIN=$(cd "$(dirname "$BIN")" && pwd)/$(basename "$BIN")
tmp=$BIN.new.$$
trap 'rm -f "$tmp"' EXIT
"$ROOT/scripts/build-go.sh" "$tmp" ./cmd/remote-agent
mv -f "$tmp" "$BIN"
echo "installed $BIN"

# The line of the service's unit that names the binary, as daemon.sh
# writes it, to tell whether the service runs this one.
case $(uname) in
Darwin)
	unit=$HOME/Library/LaunchAgents/com.github.lewin671.remote-agent.plist
	esc=$(printf '%s' "$BIN" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g')
	line="	<array><string>$esc</string><string>daemon</string></array>"
	;;
*)
	unit=$HOME/.config/systemd/user/remote-agent.service
	line="ExecStart=\"$BIN\" daemon"
	;;
esac
if [ -f "$unit" ] && grep -qxF "$line" "$unit"; then
	BIN=$BIN "$ROOT/scripts/daemon.sh" restart
	echo "restarted the daemon"
elif [ -f "$unit" ]; then
	# The service runs another binary: point it at this one.
	BIN=$BIN "$ROOT/scripts/daemon.sh" install
elif "$BIN" ls >/dev/null 2>&1; then
	echo "a daemon started by hand is running the old binary: restart it, or run scripts/daemon.sh install to have it managed"
fi
