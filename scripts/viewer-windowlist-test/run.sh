#!/usr/bin/env bash
# The viewer's window-list waiters (nextWindowList) against a host that cannot
# list windows: check.mjs drives the page as served, its host's answers stood
# in for. Needs docker; the browser runs in a Playwright container. Nothing
# here touches the host's reminal.
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd ../.. && pwd)
PW=mcr.microsoft.com/playwright:v1.56.0-noble
docker run --rm -v "$ROOT/cloudflare/public":/t:ro -v "$PWD":/s:ro -w /e2e "$PW" \
  sh -c 'cp /s/check.mjs /e2e/t.mjs; npm i --silent --no-save playwright@1.56.0 >/dev/null 2>&1; node t.mjs'
