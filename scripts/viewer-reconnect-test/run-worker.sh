#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# checks.mjs against the Worker (cloudflare/, under wrangler dev) instead of
# the Go relay, and the Worker's own count of who is connected: in one
# Playwright container, the Worker, a real session registered with it, and
# the browser.
#
#   scripts/viewer-reconnect-test/run-worker.sh [TREE]
# Needs docker. Nothing here touches the host's reminal, or a deployed Worker.
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd ../.. && pwd)
TREE=${1:-$ROOT}
DELAY=${DELAY:-150ms} RATE=${RATE:-1600kbit}
NAME=reminal-viewer-reconnect-worker
PW=mcr.microsoft.com/playwright:v1.56.0-noble
BIN=$(mktemp -d)
trap 'docker rm -f "$NAME" >/dev/null 2>&1 || true; rm -rf "$BIN"' EXIT
docker run --rm -v "$TREE":/src:ro -v "$BIN":/out -e GOFLAGS=-buildvcs=false golang:1.26-bookworm \
  sh -c 'cp -r /src /w && cd /w && go build -o /out/reminal ./cmd/reminal'
docker run --rm --name "$NAME" --cap-add NET_ADMIN -e DELAY="$DELAY" -e RATE="$RATE" \
  -v "$TREE/cloudflare":/cf:ro -v "$TREE/internal/client/web":/web:ro -v "$BIN":/b:ro -v "$PWD":/t:ro "$PW" bash /t/worker-inside.sh
