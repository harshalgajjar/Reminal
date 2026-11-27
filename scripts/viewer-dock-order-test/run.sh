#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# The order of the session dock. Two runs, each in Docker with one browser in
# a Playwright container; nothing here touches the host's reminal:
#
#   check.mjs  the page as served, two tabs, no host behind it: the order as
#              the person moves, a session connecting again (in this tab or
#              another), a page loaded again, forget and kill, named sessions,
#              history of a page of before, a first PIN connection.
#   real.mjs   the real page on this tree's reminal as relay with three
#              sessions, a phone's screen: moves by the dock's own gesture
#              and by a Recents row, the other tab's connection dropped and
#              made again, the screen off and on, pages loaded again.
#
#   run.sh [REF]   REF: that git ref's page and reminal instead of this tree's
#                  (a control: origin/main before this must fail).
#   ONLY=page|real run one of the two.
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
PAGE=""
OUT=$(mktemp -d)
trap '"$ROOT/scripts/rm-tmp.sh" "$PAGE" "$OUT" "$TMP"' EXIT
PW=mcr.microsoft.com/playwright:v1.56.0-noble
rc=0
if [ "${ONLY:-page}" = page ]; then
  PAGE=$("$SRC/scripts/viewer-page.sh")
  echo "== check.mjs: the page as served, two tabs"
  docker run --rm --cpus 2 -v "$PAGE":/t:ro -v "$PWD":/s:ro -w /e2e "$PW" \
    sh -c 'cp /s/check.mjs /e2e/t.mjs; npm i --silent --no-save playwright@1.56.0 >/dev/null 2>&1; node t.mjs' || rc=1
fi
if [ "${ONLY:-real}" = real ]; then
  echo "== real.mjs: a relay, three sessions, a phone's screen"
  docker run --rm --cpus 2 -v "$SRC":/src:ro -v "$OUT":/out -v reminal-gocache:/root/.cache/go-build -v reminal-gomod:/go/pkg/mod -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false golang:1.26-bookworm \
    sh -c 'mkdir -p /w && cp -r /src/. /w/ && cd /w && go build -o /out/reminal ./cmd/reminal'
  docker run --rm --cpus 2 --shm-size=1g -v "$OUT":/out:ro -v "$PWD":/s:ro -w /e2e "$PW" bash /s/inside.sh || rc=1
fi
exit $rc
