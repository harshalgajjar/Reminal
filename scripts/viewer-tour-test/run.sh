#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# The first-visit tour never takes the keyboard from the terminal (check.mjs):
# opened while the terminal has it, what is typed — Enter and the arrows too
# — goes on to the session, and the card answers its keys once clicked; and
# it does not open while someone is typing into the terminal, only in a
# pause. The page as served, no host behind it. Needs docker; the browser
# runs in a Playwright container.
#
#   scripts/viewer-tour-test/run.sh [PAGE_DIR]   (default: the page built from internal/client/web)
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd ../.. && pwd)
PAGE=${1:-}
# The page as served, built from internal/client/web (scripts/viewer-page.sh).
if [ -z "$PAGE" ]; then
  PAGE=$("$ROOT/scripts/viewer-page.sh")
  trap 'rm -rf "${PAGE:?}"' EXIT
fi
PW=mcr.microsoft.com/playwright:v1.56.0-noble
docker run --rm -v "$PAGE":/t:ro -v "$PWD":/s:ro -w /e2e "$PW" \
  sh -c 'cp /s/check.mjs /e2e/t.mjs; npm i --silent --no-save playwright@1.56.0 >/dev/null 2>&1; node t.mjs'
