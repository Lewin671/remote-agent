#!/bin/sh
# Installs or upgrades remote-agent from the published releases.
#
#   install.sh                 the daemon and command for this computer
#   install.sh server [DIR]    the relay server's files, for a VPS (default DIR: ./remote-agent-server)
#   install.sh web [DIR]       the web client's static files (default DIR: ./remote-agent-web)
#
# Piped from the network:
#   curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent-releases/main/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/Lewin671/remote-agent-releases/main/install.sh | sh -s -- server
# Or download it, read it, and run it: it needs no root and asks for none.
#
# REMOTE_AGENT_VERSION picks a release (such as v0.32.0) instead of the latest;
# BIN_DIR is where the command goes (default ~/.local/bin).
# Every download is checked against the release's SHA256SUMS.
set -eu

RELEASES=${REMOTE_AGENT_RELEASES:-https://github.com/Lewin671/remote-agent-releases/releases}

say() { printf '%s\n' "$*"; }
die() {
	printf 'install.sh: %s\n' "$*" >&2
	exit 1
}

platform() {
	case $(uname -s) in
	Darwin) os=darwin ;;
	Linux) os=linux ;;
	*) die "unsupported system $(uname -s): remote-agent runs on macOS and Linux" ;;
	esac
	case $(uname -m) in
	arm64 | aarch64) arch=arm64 ;;
	x86_64 | amd64) arch=amd64 ;;
	*) die "unsupported processor $(uname -m)" ;;
	esac
}

latest() {
	if [ -n "${REMOTE_AGENT_VERSION:-}" ]; then
		tag=v${REMOTE_AGENT_VERSION#v}
	else
		tag=v$(curl -fsSL "$RELEASES/latest/download/latest.properties" | sed -n 's/^version=//p')
	fi
	printf '%s' "$tag" | grep -Eqx 'v[0-9]+\.[0-9]+\.[0-9]+' || die "could not find the latest release at $RELEASES"
}

# fetch NAME downloads the release's archive NAME into $tmp, checks it and unpacks it there.
fetch() {
	say "Downloading $1 ($tag)"
	curl -fsSL -o "$tmp/$1.tar.gz" "$RELEASES/download/$tag/$1.tar.gz" || die "$tag has no $1.tar.gz"
	curl -fsSL -o "$tmp/SHA256SUMS" "$RELEASES/download/$tag/SHA256SUMS" || die "$tag has no SHA256SUMS"
	want=$(awk -v f="$1.tar.gz" '$2 == f {print $1}' "$tmp/SHA256SUMS")
	if command -v sha256sum >/dev/null 2>&1; then
		got=$(sha256sum "$tmp/$1.tar.gz" | awk '{print $1}')
	else
		got=$(shasum -a 256 "$tmp/$1.tar.gz" | awk '{print $1}')
	fi
	[ -n "$want" ] && [ "$want" = "$got" ] || die "checksum of $1.tar.gz does not match SHA256SUMS"
	# Everything in the archive is under its one directory.
	if tar -tzf "$tmp/$1.tar.gz" | grep -Ev "^$1(/|\$)" | grep -q . || tar -tzf "$tmp/$1.tar.gz" | grep -Eq '(^|/)\.\.(/|$)'; then
		die "$1.tar.gz holds files outside $1/"
	fi
	tar -xzf "$tmp/$1.tar.gz" -C "$tmp"
}

# Replaced by rename: sessions started by the old binary keep running it.
put() {
	cp "$1" "$2.new.$$"
	chmod 755 "$2.new.$$"
	mv -f "$2.new.$$" "$2"
}

install_daemon() {
	platform
	latest
	name=remote-agent-$tag-$os-$arch
	fetch "$name"
	dir=${BIN_DIR:-$HOME/.local/bin}
	mkdir -p "$dir"
	dir=$(cd "$dir" && pwd)
	put "$tmp/$name/remote-agent" "$dir/remote-agent"
	put "$tmp/$name/remote-agent-service" "$dir/remote-agent-service"
	mkdir -p "${XDG_DATA_HOME:-$HOME/.local/share}/remote-agent"
	cp "$tmp/$name/THIRD_PARTY_NOTICES.txt" "${XDG_DATA_HOME:-$HOME/.local/share}/remote-agent/"
	say "Installed $("$dir/remote-agent" --version) in $dir"

	if [ "$os" = darwin ]; then
		unit=$HOME/Library/LaunchAgents/com.github.lewin671.remote-agent.plist
	else
		unit=$HOME/.config/systemd/user/remote-agent.service
	fi
	if [ -f "$unit" ] && grep -qF "$dir/remote-agent" "$unit"; then
		BIN=$dir/remote-agent "$dir/remote-agent-service" restart
		say "Restarted the background service. Sessions in tmux were not interrupted."
	elif [ -f "$unit" ]; then
		say "The background service runs another binary. To switch it to this one:"
		say "  BIN=$dir/remote-agent $dir/remote-agent-service install"
	else
		say ""
		say "Next:"
		say "  remote-agent-service install        run the daemon in the background, now and at login"
		say "  remote-agent account init --server https://ra.example.com"
		say "                                      connect to your relay (see the self-hosting guide)"
		say "  remote-agent codex | claude         start a session here"
	fi
	case ":$PATH:" in
	*":$dir:"*) ;;
	*) say "" && say "$dir is not in your PATH. Add it, for example: export PATH=\"$dir:\$PATH\"" ;;
	esac
	command -v tmux >/dev/null 2>&1 || say "tmux was not found: remote-agent needs tmux 3.2 or newer."
}

install_server() {
	[ "$(uname -s)" = Linux ] || die "the relay server runs on Linux; run this on your VPS"
	platform
	latest
	name=remote-agent-server-$tag-linux-$arch
	fetch "$name"
	dir=${1:-remote-agent-server}
	mkdir -p "$dir" || die "cannot create $dir; create it first and make it yours: sudo mkdir -p $dir && sudo chown \"\$(id -u)\" $dir"
	[ -w "$dir" ] || die "$dir is not writable by you; make it yours: sudo chown \"\$(id -u)\" $dir"
	dir=$(cd "$dir" && pwd)
	put "$tmp/$name/remote-agent-server" "$dir/remote-agent-server"
	# Yours to edit once they are there: an upgrade replaces the binary only.
	for f in Dockerfile .dockerignore compose.yaml Caddyfile env.example; do
		[ -f "$dir/$f" ] || cp "$tmp/$name/$f" "$dir/$f"
	done
	cp "$tmp/$name/THIRD_PARTY_NOTICES.txt" "$dir/"
	# The relay runs as an unprivileged user (65534) and writes only here.
	# Set up once: afterwards the directory is that user's, not ours to change.
	if [ ! -d "$dir/data" ]; then
		mkdir "$dir/data"
		chmod 700 "$dir/data"
		chown 65534:65534 "$dir/data" 2>/dev/null || say "Could not hand $dir/data to user 65534; run: sudo chown 65534:65534 $dir/data"
	fi
	if [ -f "$dir/.env" ]; then
		say "Upgraded the relay in $dir to $tag. Apply it with:"
		say "  cd $dir && docker compose up -d --build"
		return
	fi
	token=$(head -c 24 /dev/urandom | base64 | tr '+/' '-_')
	(umask 077 && sed "s|^RA_SETUP_TOKEN=.*|RA_SETUP_TOKEN=$token|" "$dir/env.example" >"$dir/.env")
	say "The relay's files are in $dir ($tag), with a new setup token in .env."
	say ""
	say "Next:"
	say "  1. Point your relay domain's DNS record at this machine."
	say "  2. Set RELAY_DOMAIN and ALLOWED_ORIGINS in $dir/.env"
	say "  3. cd $dir && docker compose up -d --build"
	say "  4. On your computer: remote-agent account init --server https://<your relay domain>"
	say "     It asks for the setup token: grep RA_SETUP_TOKEN $dir/.env"
}

install_web() {
	latest
	name=remote-agent-web-$tag
	fetch "$name"
	dir=${1:-remote-agent-web}
	# Only an earlier copy of the web client is replaced, never another directory.
	if [ -e "$dir" ]; then
		[ -f "$dir/manifest.webmanifest" ] && [ -f "$dir/sw.js" ] || die "$dir exists and is not a copy of the web client; choose another directory"
		rm -rf "$dir"
	fi
	mkdir -p "$(dirname "$dir")"
	mv "$tmp/$name" "$dir"
	say "The web client ($tag) is in $dir: static files for any HTTPS host."
	say "With Cloudflare Pages:"
	say "  npx wrangler pages deploy $dir --project-name <your project>"
}

main() {
	command -v curl >/dev/null 2>&1 || die "curl is required"
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	case ${1:-daemon} in
	daemon) install_daemon ;;
	server) install_server "${2:-}" ;;
	web) install_web "${2:-}" ;;
	*) die "usage: install.sh [daemon | server [DIR] | web [DIR]]" ;;
	esac
}

# Nothing runs until the whole script has arrived.
main "$@"
