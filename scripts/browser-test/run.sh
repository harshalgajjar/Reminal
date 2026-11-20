#!/usr/bin/env bash
# The browser feature end to end, in Docker only (never the host's Chrome or
# profile): reminal's extension loaded in Playwright's Chromium on Xvfb, with
# a throwaway HOME, reminal's daemon and a session whose stand-in agent uses
# the browser_* tools. check.mjs answers the consent window as the person
# would (Deny first: the control; then Allow) and checks each tool and rule.
#   GIF=1  record the run to $SHOTS/browser.gif (SHOTS defaults to ./out)
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd ../.. && pwd)
OUT=$(mktemp -d)
trap 'rm -rf "${OUT:?}"' EXIT
SHOTS="${SHOTS:-$PWD/out}"; mkdir -p "$SHOTS"
docker image inspect reminal-browser-rig:1 >/dev/null 2>&1 || docker build -q -t reminal-browser-rig:1 . >/dev/null
docker run --rm -v "$ROOT":/src:ro -v "$OUT":/out -v reminal-browser-rig-gomod:/go/pkg/mod -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false golang:1.26-bookworm \
  sh -c 'mkdir -p /w && cp -r /src/. /w/ && cd /w && go build -o /out/reminal ./cmd/reminal'
docker run --rm --shm-size=1g -v "$ROOT":/r:ro -v "$OUT":/out:ro -v "$SHOTS":/s -e GIF="${GIF:-}" -e GIF_SPEED="${GIF_SPEED:-1.6}" -e MODE="${MODE:-full}" \
  reminal-browser-rig:1 bash /r/scripts/browser-test/rig.sh
