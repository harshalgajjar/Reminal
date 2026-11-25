#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# The viewer page when the terminal's library does not load: a browser skips
# a script it could not fetch and runs the next one all the same. check.mjs
# holds that one request back and asks that the page says so in words, with
# no error but its own; with nothing held back, that it loads as ever; and
# with every host but its own out of reach, that it still loads, its code
# being all its own. Needs docker; the browser runs in a Playwright
# container. Nothing here touches the host's reminal. PAGE=dir runs against
# a page built elsewhere (a control).
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd ../.. && pwd)
PAGE="${PAGE:-}"
if [ -z "$PAGE" ]; then
  PAGE=$("$ROOT/scripts/viewer-page.sh")
  trap '"$ROOT/scripts/rm-tmp.sh" "$PAGE"' EXIT
fi
PW=mcr.microsoft.com/playwright:v1.56.0-noble
docker run --rm --cpus 2 -v "$PAGE":/t:ro -v "$PWD":/s:ro -w /e2e "$PW" \
  sh -c 'cp /s/check.mjs /e2e/t.mjs; npm i --silent --no-save playwright@1.56.0 >/dev/null 2>&1; node t.mjs'
