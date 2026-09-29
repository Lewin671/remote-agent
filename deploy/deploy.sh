#!/bin/sh
# Deploys remote-agent-server to a host that already runs Caddy in docker.
#
# Usage: deploy/deploy.sh <ssh-host> [dir]
#
# The first run creates <dir>/.env with a random RA_SETUP_TOKEN, which
# `remote-agent account init` asks for. The Caddy site block is not added
# automatically; see docs/architecture.md.
set -eu
HOST=$1
DIR=${2:-/opt/remote-agent}
ROOT=$(cd "$(dirname "$0")/.." && pwd)

(cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags=-s -o deploy/remote-agent-server ./cmd/remote-agent-server)
ssh "$HOST" "mkdir -p $DIR/data && chown 65534:65534 $DIR/data && chmod 700 $DIR/data"
scp -q "$ROOT/deploy/Dockerfile" "$ROOT/deploy/compose.yaml" "$ROOT/deploy/remote-agent-server" "$HOST:$DIR/"
rm -f "$ROOT/deploy/remote-agent-server"
ssh "$HOST" "cd $DIR && if [ ! -f .env ]; then umask 077; printf 'RA_SETUP_TOKEN=%s\nALLOWED_ORIGINS=http://localhost:5173,http://localhost:4173\n' \$(head -c 24 /dev/urandom | base64 | tr '+/' '-_') > .env; echo 'created .env with a new setup token'; fi && docker compose up -d --build && docker compose ps"
