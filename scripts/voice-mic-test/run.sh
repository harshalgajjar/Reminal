#!/usr/bin/env bash
# The voice mic is there to tap on a desktop (the header's, 1280x800) and on a
# phone (the key bar's), opens the voice panel, and on a session whose
# reminal predates voice says so instead; and a notice that what was said was
# not typed in reaches the voice frame's conversation. check.mjs drives the
# page as served, with the real voice frame (web/voice). Needs docker; the
# browser runs in a Playwright container. Nothing here touches the host's
# reminal. SHOTS=dir keeps a screenshot per case.
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
docker run --rm -v "$PAGE":/t:ro -v "$ROOT/internal/client/web/voice":/v:ro -v "$PWD":/s:ro -w /e2e ${SHOT_ARGS[@]+"${SHOT_ARGS[@]}"} "$PW" \
  sh -c 'cp /s/check.mjs /e2e/t.mjs; npm i --silent --no-save playwright@1.56.0 >/dev/null 2>&1; node t.mjs'
