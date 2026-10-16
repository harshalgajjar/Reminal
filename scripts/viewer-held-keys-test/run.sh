#!/usr/bin/env bash
# Keys held for a session being switched to go nowhere else (check.mjs drives
# the page as served, its host stood in for). Needs docker; the browser runs
# in a Playwright container. Nothing here touches the host's reminal.
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd ../.. && pwd)
PAGE=""
# The page as served, built from internal/client/web (scripts/viewer-page.sh).
if [ -z "$PAGE" ]; then
  PAGE=$("$ROOT/scripts/viewer-page.sh")
  trap 'rm -rf "${PAGE:?}"' EXIT
fi
PW=mcr.microsoft.com/playwright:v1.56.0-noble
docker run --rm -v "$PAGE":/t:ro -v "$PWD":/s:ro -w /e2e "$PW" \
  sh -c 'cp /s/check.mjs /e2e/t.mjs; npm i --silent --no-save playwright@1.56.0 >/dev/null 2>&1; node t.mjs'
