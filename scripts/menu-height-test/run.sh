#!/usr/bin/env bash
# Every header menu (Machines, Windows, Apps, Host) ends inside the window
# however long its list, and scrolls to its last row: on a desktop, a short
# desktop window and a phone. check.mjs drives the page as served. Needs
# docker; the browser runs in a Playwright container. Nothing here touches the
# host's reminal. SHOTS=dir keeps a screenshot per menu and size.
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd ../.. && pwd)
PAGE="${PAGE:-}"
# The page as served, built from internal/client/web (scripts/viewer-page.sh);
# PAGE=dir runs against a page built elsewhere (a control from an older tree).
if [ -z "$PAGE" ]; then
  PAGE=$("$ROOT/scripts/viewer-page.sh")
  trap '"$ROOT/scripts/rm-tmp.sh" "$PAGE"' EXIT
fi
PW=mcr.microsoft.com/playwright:v1.56.0-noble
SHOT_ARGS=()
[ -n "${SHOTS:-}" ] && SHOT_ARGS=(-v "$SHOTS":/shots -e SHOTS=/shots)
docker run --rm -v "$PAGE":/t:ro -v "$PWD":/s:ro -w /e2e ${SHOT_ARGS[@]+"${SHOT_ARGS[@]}"} "$PW" \
  sh -c 'cp /s/check.mjs /e2e/t.mjs; npm i --silent --no-save playwright@1.56.0 >/dev/null 2>&1; node t.mjs'
