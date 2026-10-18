#!/bin/sh
# Deploys remote-agent-server to a host that already runs Caddy in docker.
#
# Usage: deploy/deploy.sh <ssh-host> [dir]
#
# The first run creates <dir>/.env with a random RA_SETUP_TOKEN, which the
# phone app asks for when it creates the account; computers join with
# `remote-agent account join`. The Caddy site block is not added
# automatically; see docs/architecture.md.
#
# Android pushes: if ~/.config/remote-agent/fcm-service-account.json exists
# (a Firebase service account key), it is copied to <dir>/data and the
# server sends FCM; without it FCM stays off.
set -eu
HOST=$1
DIR=${2:-/opt/remote-agent}
ROOT=$(cd "$(dirname "$0")/.." && pwd)

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "$ROOT/scripts/build-go.sh" "$ROOT/deploy/remote-agent-server" ./cmd/remote-agent-server
ssh "$HOST" "mkdir -p $DIR/data && chown 65534:65534 $DIR/data && chmod 700 $DIR/data"
scp -q "$ROOT/deploy/Dockerfile" "$ROOT/deploy/compose.yaml" "$ROOT/deploy/remote-agent-server" "$HOST:$DIR/"
rm -f "$ROOT/deploy/remote-agent-server"
ssh "$HOST" "cd $DIR && if [ ! -f .env ]; then umask 077; printf 'RA_SETUP_TOKEN=%s\nALLOWED_ORIGINS=http://localhost:5173,http://localhost:4173\n' \$(head -c 24 /dev/urandom | base64 | tr '+/' '-_') > .env; echo 'created .env with a new setup token'; fi"
FCM=$HOME/.config/remote-agent/fcm-service-account.json
if [ -f "$FCM" ]; then
	scp -q "$FCM" "$HOST:$DIR/data/fcm-service-account.json"
	ssh "$HOST" "chown 65534:65534 $DIR/data/fcm-service-account.json && chmod 600 $DIR/data/fcm-service-account.json && cd $DIR && { grep -q '^RA_FCM_SERVICE_ACCOUNT=' .env 2>/dev/null || echo 'RA_FCM_SERVICE_ACCOUNT=/data/fcm-service-account.json' >> .env; }"
fi
ssh "$HOST" "cd $DIR && docker compose up -d --build && docker compose ps"
