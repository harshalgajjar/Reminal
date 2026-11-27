#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# The order of the session dock: check.mjs drives the page as served, in two
# tabs of one browser, with no host behind it. The sessions are in the order
# the person moved between them; one that connects again (in this tab, or in
# another tab left on it) does not move. Needs docker; the browser runs in a
# Playwright container. Nothing here touches the host's reminal.
#
#   run.sh [REF]   REF: the page of that git ref instead of this tree's
#                  (a control: origin/main before this must fail).
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd ../.. && pwd)
SRC=$ROOT
TMP=""
if [ -n "${1:-}" ]; then
  TMP=$(mktemp -d)
  git -C "$ROOT" archive "$1" | tar -x -C "$TMP"
  SRC=$TMP
fi
PAGE=$("$SRC/scripts/viewer-page.sh")
trap '"$ROOT/scripts/rm-tmp.sh" "$PAGE"; [ -z "$TMP" ] || "$ROOT/scripts/rm-tmp.sh" "$TMP"' EXIT
PW=mcr.microsoft.com/playwright:v1.56.0-noble
docker run --rm --cpus 2 -v "$PAGE":/t:ro -v "$PWD":/s:ro -w /e2e "$PW" \
  sh -c 'cp /s/check.mjs /e2e/t.mjs; npm i --silent --no-save playwright@1.56.0 >/dev/null 2>&1; node t.mjs'
