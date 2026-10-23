#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# A tapped notification (check.mjs): with a page open, the service worker
# tells it where the notification points and focuses it, and the page hands
# that to whatever took it (onNotificationURL), or stays as it is; with none
# open, it opens there. Needs docker; the browser runs in a Playwright
# container.
#
#   scripts/viewer-notification-click-test/run.sh [PAGE_DIR]   (default: the page built from internal/client/web)
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd ../.. && pwd)
PAGE=${1:-}
if [ -z "$PAGE" ]; then
  PAGE=$("$ROOT/scripts/viewer-page.sh")
  trap '"$ROOT/scripts/rm-tmp.sh" "$PAGE"' EXIT
fi
PW=mcr.microsoft.com/playwright:v1.56.0-noble
docker run --rm -v "$PAGE":/t:ro -v "$PWD":/s:ro -w /e2e "$PW" \
  sh -c 'cp /s/check.mjs /e2e/t.mjs; npm i --silent --no-save playwright@1.56.0 >/dev/null 2>&1; node t.mjs'
