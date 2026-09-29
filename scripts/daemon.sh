#!/bin/sh
# Runs the remote-agent daemon in the background as a user service
# (launchd on macOS, systemd --user on Linux), restarted if it exits.
#
# Usage: scripts/daemon.sh install|uninstall|start|stop|restart|status|log
#
# install  writes the service for the binary at $BIN (default: the
#          remote-agent in PATH, else ~/bin/remote-agent) and starts it
# restart  restarts it, e.g. after installing a new binary
# log      follows its log
#
# The service gets the PATH of the shell that ran install, so that it
# finds codex and tmux, and REMOTE_AGENT_HOME, REMOTE_AGENT_TMUX_SOCKET,
# REMOTE_AGENT_CODEX and CODEX_HOME if set; run install again after moving them. Variables
# Codex needs, such as proxies, go in ~/.remote-agent/codex.env. Sessions
# keep running in tmux while the daemon restarts.
set -eu

LABEL=com.github.lewin671.remote-agent
STATE=${REMOTE_AGENT_HOME:-$HOME/.remote-agent}
LOG=$STATE/daemon.log
BIN=${BIN:-$(command -v remote-agent || echo "$HOME/bin/remote-agent")}

case $(uname) in
Darwin)
	UNIT=$HOME/Library/LaunchAgents/$LABEL.plist
	DOMAIN=gui/$(id -u)
	;;
Linux)
	UNIT=$HOME/.config/systemd/user/remote-agent.service
	;;
*)
	echo "unsupported system: $(uname)" >&2
	exit 1
	;;
esac

xml() { printf '%s' "$1" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g'; }

installed() {
	if [ ! -f "$UNIT" ]; then
		echo "the daemon service is not installed; run: $0 install" >&2
		exit 1
	fi
}

install() {
	if [ ! -x "$BIN" ]; then
		echo "no remote-agent binary at $BIN; build it first (scripts/install.sh) or set BIN" >&2
		exit 1
	fi
	BIN=$(cd "$(dirname "$BIN")" && pwd)/$(basename "$BIN")
	# A daemon started by hand holds the lock; the service would keep
	# failing to start next to it.
	if [ ! -f "$UNIT" ] && "$BIN" ls >/dev/null 2>&1; then
		echo "a daemon is already running (started by hand?); stop it first, then run install again" >&2
		exit 1
	fi
	mkdir -p "$STATE" "$(dirname "$UNIT")"
	if [ "$(uname)" = Darwin ]; then
		extra=
		for v in REMOTE_AGENT_HOME REMOTE_AGENT_TMUX_SOCKET REMOTE_AGENT_CODEX CODEX_HOME; do
			eval "val=\${$v:-}"
			[ -n "$val" ] && extra="$extra<key>$v</key><string>$(xml "$val")</string>"
		done
		cat >"$UNIT" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>$LABEL</string>
	<key>ProgramArguments</key>
	<array><string>$(xml "$BIN")</string><string>daemon</string></array>
	<key>EnvironmentVariables</key>
	<dict><key>PATH</key><string>$(xml "$PATH")</string>$extra</dict>
	<key>WorkingDirectory</key><string>$(xml "$HOME")</string>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>ThrottleInterval</key><integer>10</integer>
	<key>StandardOutPath</key><string>$(xml "$LOG")</string>
	<key>StandardErrorPath</key><string>$(xml "$LOG")</string>
</dict>
</plist>
EOF
		launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
		launchctl bootstrap "$DOMAIN" "$UNIT"
	else
		extra=
		for v in REMOTE_AGENT_HOME REMOTE_AGENT_TMUX_SOCKET REMOTE_AGENT_CODEX CODEX_HOME; do
			eval "val=\${$v:-}"
			[ -n "$val" ] && extra="$extra
Environment=\"$v=$val\""
		done
		cat >"$UNIT" <<EOF
[Unit]
Description=remote-agent daemon

[Service]
ExecStart="$BIN" daemon
Environment="PATH=$PATH"$extra
WorkingDirectory=$HOME
Restart=always
RestartSec=10
StandardOutput=append:$LOG
StandardError=append:$LOG

[Install]
WantedBy=default.target
EOF
		systemctl --user daemon-reload
		systemctl --user enable remote-agent.service
		systemctl --user restart remote-agent.service
	fi
	echo "installed $UNIT for $BIN; log: $LOG"
}

cmd=${1:-status}
case $cmd in
install) install ;;
uninstall)
	if [ "$(uname)" = Darwin ]; then
		launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
	else
		systemctl --user disable --now remote-agent.service 2>/dev/null || true
	fi
	rm -f "$UNIT"
	echo "removed $UNIT"
	;;
start)
	installed
	if [ "$(uname)" = Darwin ]; then
		launchctl bootstrap "$DOMAIN" "$UNIT" 2>/dev/null || launchctl kickstart "$DOMAIN/$LABEL"
	else
		systemctl --user start remote-agent.service
	fi
	;;
stop)
	installed
	if [ "$(uname)" = Darwin ]; then
		# KeepAlive would start it again; unloading stops it until start.
		launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
	else
		systemctl --user stop remote-agent.service
	fi
	;;
restart)
	installed
	if [ "$(uname)" = Darwin ]; then
		launchctl kickstart -k "$DOMAIN/$LABEL" 2>/dev/null || launchctl bootstrap "$DOMAIN" "$UNIT"
	else
		systemctl --user restart remote-agent.service
	fi
	;;
status)
	installed
	if [ "$(uname)" = Darwin ]; then
		launchctl print "$DOMAIN/$LABEL" 2>/dev/null | grep -E '^\s*(state|pid|last exit code) =' || echo "not running"
	else
		systemctl --user --no-pager status remote-agent.service || true
	fi
	tail -n 5 "$LOG" 2>/dev/null || true
	;;
log) tail -n 50 -f "$LOG" ;;
*)
	echo "usage: $0 install|uninstall|start|stop|restart|status|log" >&2
	exit 2
	;;
esac
