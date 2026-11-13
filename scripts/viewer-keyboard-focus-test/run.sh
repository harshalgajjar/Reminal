#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# A touch screen's keyboard comes up only when asked (check.mjs): a tap on a
# remote window whose keyboard is off, a reconnect, and a keyboard put away
# by hand all leave no input holding focus, while a window's keyboard turned
# on and a desktop's reconnect still take it. Needs docker; the browser runs
# in a Playwright container.
#
#   scripts/viewer-keyboard-focus-test/run.sh [PAGE_DIR]
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
