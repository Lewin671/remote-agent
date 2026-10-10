#!/bin/sh
# Builds what a release distributes besides the APK, as archives in dist/:
# the daemon and the server for each platform, and the web client.
#
# Usage: scripts/build-dist.sh
#
# These are for other people's deployments, so nothing of ours goes in:
# the web client is built without a default server, and the server comes
# with the templates of deploy/releases-repo/self-host/ rather than with
# deploy/. scripts/package-release.sh scans the result before it is published.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
tag=v$(sed -n 's/^version=//p' version.properties)
mkdir -p dist
rm -f dist/remote-agent-*.tar.gz
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT

# Archives name no user of the machine that made them.
if tar --version 2>/dev/null | grep -q 'GNU tar'; then
	owner='--owner=0 --group=0 --numeric-owner'
else
	owner='--uid 0 --gid 0 --uname root --gname root'
fi
pack() {
	# shellcheck disable=SC2086
	COPYFILE_DISABLE=1 tar -C "$stage" $owner -czf "dist/$1.tar.gz" "$1"
}

for target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do
	os=${target%/*}
	arch=${target#*/}
	name=remote-agent-$tag-$os-$arch
	mkdir "$stage/$name"
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch scripts/build-go.sh "$stage/$name/remote-agent" ./cmd/remote-agent
	cp scripts/daemon.sh "$stage/$name/remote-agent-service"
	chmod 755 "$stage/$name/remote-agent-service"
	GOOS=$os GOARCH=$arch scripts/notices.py go ./cmd/remote-agent >"$stage/$name/THIRD_PARTY_NOTICES.txt"
	pack "$name"
done

for arch in arm64 amd64; do
	name=remote-agent-server-$tag-linux-$arch
	mkdir "$stage/$name"
	CGO_ENABLED=0 GOOS=linux GOARCH=$arch scripts/build-go.sh "$stage/$name/remote-agent-server" ./cmd/remote-agent-server
	for f in Dockerfile .dockerignore compose.yaml Caddyfile env.example; do
		cp "deploy/releases-repo/self-host/$f" "$stage/$name/"
	done
	GOOS=linux GOARCH=$arch scripts/notices.py go ./cmd/remote-agent-server >"$stage/$name/THIRD_PARTY_NOTICES.txt"
	pack "$name"
done

# Built last: the Go builds above call a checkout with changed files dirty.
name=remote-agent-web-$tag
# Vite reads settings from these files as well as from the environment, and
# one left by a deployment of ours would put its server in the bundle.
for f in web/.env*; do
	[ ! -e "$f" ] || {
		echo "$f would be built into the public web client; move it away first" >&2
		exit 1
	}
done
(cd web && pnpm install --frozen-lockfile --silent && env -u VITE_DEFAULT_SERVER -u VITE_SAME_ORIGIN_API RA_OFFICIAL_RELEASE=true pnpm build)
cp -R web/dist "$stage/$name"
scripts/notices.py web >"$stage/$name/licenses/THIRD_PARTY_NOTICES.txt"
pack "$name"

ls -l dist/remote-agent-*.tar.gz
