#!/usr/bin/env bash
# What the viewer shows of a machine when the session shown changes:
# check.mjs drives the page as served, with no host behind it (its answers
# stood in for). Needs docker; the browser runs in a Playwright container.
# Nothing here touches the host's reminal.
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd ../.. && pwd)
PW=mcr.microsoft.com/playwright:v1.56.0-noble
docker run --rm -v "$ROOT/cloudflare/public":/t:ro -v "$PWD":/s:ro -w /e2e "$PW" \
  sh -c 'cp /s/check.mjs /e2e/t.mjs; npm i --silent --no-save playwright@1.56.0 >/dev/null 2>&1; node t.mjs'
