#!/usr/bin/env bash
# The same small tasks through reminal's browser tools and through
# Playwright MCP, in Docker only: time, text returned, calls. Median of RUNS
# (default 5). reminal drives a windowed Chromium with its extension (as a
# person's browser is); Playwright MCP drives its own headless Chromium.
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd ../../.. && pwd)
OUT=$(mktemp -d)
trap 'rm -rf "${OUT:?}"' EXIT
SHOTS="${SHOTS:-$PWD/out}"; mkdir -p "$SHOTS"
docker image inspect reminal-browser-rig:2 >/dev/null 2>&1 || docker build -q -t reminal-browser-rig:2 .. >/dev/null
docker run --rm -v "$ROOT":/src:ro -v "$OUT":/out -v reminal-browser-rig-gomod:/go/pkg/mod -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false golang:1.26-bookworm \
  sh -c 'mkdir -p /w && cp -r /src/. /w/ && cd /w && go build -o /out/reminal ./cmd/reminal'
docker run --rm --shm-size=1g -v "$ROOT":/r:ro -v "$OUT":/out:ro -v "$SHOTS":/s -e RUNS="${RUNS:-5}" -e PWMCP="${PWMCP:-0.0.86}" \
  reminal-browser-rig:2 bash /r/scripts/browser-test/bench/rig.sh
