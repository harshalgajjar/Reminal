#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# What a key typed into the viewer's terminal becomes (check.mjs): on a
# desktop every key reaches the session — a key typed twice fast ("44",
# "$(("), a key held down — and on Android the IME's double-fire (a tap
# committed through composition, then the same character again as
# insertText) is still dropped. The page as served, no host behind it.
# Needs docker; the browser runs in a Playwright container.
#
#   scripts/viewer-input-test/run.sh [PAGE_DIR]   (default: the page built from internal/client/web)
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
