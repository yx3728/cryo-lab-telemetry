#!/usr/bin/env bash
# Deploy the stack to the EC2 box.
#
#   1. builds the React bundle locally (Caddy serves it statically)
#   2. rsyncs the repo to the box (excluding dev junk and secrets)
#   3. brings the stack up with docker compose
#
# Secrets are NOT shipped: create deploy/.env on the box once (see
# deploy/.env.example below in this script's comments / the repo .env.example).
#
# Usage:  ./deploy/deploy.sh            # core stack
#         MOCKFEED=1 ./deploy/deploy.sh # also run the demo mock producer
set -euo pipefail

HOST="${HOST:-ubuntu@3.220.132.187}"
KEY="${KEY:-$HOME/.ssh/lab-monitor-key.pem}"
REMOTE="${REMOTE:-/home/ubuntu/lab-monitor}"
SSH_OPTS=(-i "$KEY" -o StrictHostKeyChecking=accept-new)
REPO="$(cd "$(dirname "$0")/.." && pwd)"

echo ">> building web bundle"
( cd "$REPO/web" && npm ci && npm run build )

echo ">> syncing repo to $HOST:$REMOTE"
rsync -az --delete -e "ssh ${SSH_OPTS[*]}" \
  --exclude '.git' --exclude 'node_modules' --exclude 'collector/.venv' \
  --exclude 'collector/buffer' --exclude '*.pem' --exclude '.env' \
  --exclude 'server/bin' \
  "$REPO/" "$HOST:$REMOTE/"

PROFILE=""
[ "${MOCKFEED:-0}" = "1" ] && PROFILE="--profile mockfeed"

echo ">> docker compose up on the box"
ssh "${SSH_OPTS[@]}" "$HOST" \
  "cd $REMOTE/deploy && docker compose $PROFILE up -d --build"

echo ">> done. Verify: https://3.220.132.187.sslip.io  and  /healthz /metrics"
