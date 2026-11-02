#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# check.mjs: a session whose relay link a middlebox resets every few seconds
# says so in its terminal once, and that it is back once, not a pair of lines
# for every drop; a single drop that comes straight back says nothing. A real
# session, a real relay, and a proxy between them that resets the socket. In
# Docker.
#
#   run.sh [REF]   REF: build reminal from that git ref instead of this tree
#                  (a control: origin/main must fail the flapping case).
set -e
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT=$(CDPATH= cd -- "$DIR/../.." && pwd)
V=${REMINAL_VOLUME_PREFIX:-reminal}
B=$(mktemp -d)
IMG=reminal-reconnect-flap-$$
trap 'docker rmi -f "$IMG" >/dev/null 2>&1 || true; "$ROOT/scripts/rm-tmp.sh" "$B"' EXIT
SRC=$ROOT
if [ -n "${1:-}" ]; then
  mkdir "$B/src"
  git -C "$ROOT" archive "$1" | tar -x -C "$B/src"
  SRC=$B/src
fi
docker run --rm -v "$SRC":/src:ro -v "$B":/out -v "$V-gocache":/root/.cache/go-build -v "$V-gomod":/go/pkg/mod \
    -e GOFLAGS=-buildvcs=false -e CGO_ENABLED=0 -w /src golang:1.26-bookworm \
    go build -o /out/reminal ./cmd/reminal
cp "$DIR/check.mjs" "$B/"
cat > "$B/Dockerfile" <<'DOCKER'
FROM node:22-bookworm
COPY reminal /usr/local/bin/
COPY check.mjs /e2e/
ENV HOME=/root SHELL=/bin/sh
CMD ["node", "/e2e/check.mjs"]
DOCKER
docker build -q -t "$IMG" "$B" >/dev/null
docker run --rm "$IMG"
