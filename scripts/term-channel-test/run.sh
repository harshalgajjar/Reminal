#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# The terminal on the direct channel, end to end, in Docker only: this
# tree's reminal as relay and session, and a headless Chromium as the viewer
# whose leg to the relay goes through proxy.mjs (DELAY ms each way, so a
# round trip to the relay costs 2 x DELAY) while WebRTC between the two is
# direct. check.mjs times key to echo and types a long known line fast:
#   - with the channel, an echo comes in well under one relay round trip;
#   - without it (WebRTC taken away: the control), it takes the round trip;
#   - either way every character typed is on the screen once, in order,
#     also when the channel goes away in the middle of the typing.
# Nothing here touches the host's reminal.
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd ../.. && pwd)
OUT=$(mktemp -d)
trap '"$ROOT/scripts/rm-tmp.sh" "$OUT"' EXIT
PW=mcr.microsoft.com/playwright:v1.56.0-noble
docker run --rm --cpus 2 -v "$ROOT":/src:ro -v "$OUT":/out -v reminal-gocache:/root/.cache/go-build -v reminal-gomod:/go/pkg/mod -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false golang:1.26-bookworm \
  sh -c 'mkdir -p /w && cp -r /src/. /w/ && cd /w && go build -o /out/reminal ./cmd/reminal'
docker run --rm --cpus 2 --shm-size=1g -v "$OUT":/out:ro -v "$PWD":/s:ro -e DELAY="${DELAY:-150}" -w /e2e "$PW" bash /s/inside.sh
