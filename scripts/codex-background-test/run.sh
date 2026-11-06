#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# check.mjs: a session whose Codex ended its turn with commands still running
# in the background is listed with them ("done · 1 background terminal"),
# and one that left none is not. The Codex is the real one (@openai/codex,
# pinned below), run against a stand-in model (model.mjs) that has it start
# long commands with exec_command. Nothing signs in anywhere. In Docker.
#
#   run.sh [REF]   REF: build reminal from that git ref instead of this tree
#                  (a control: origin/main must fail the background cases).
set -e
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT=$(CDPATH= cd -- "$DIR/../.." && pwd)
V=${REMINAL_VOLUME_PREFIX:-reminal}
CODEX=${CODEX_VERSION:-0.167.0}
B=$(mktemp -d)
IMG=reminal-codex-background-$$
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
cp "$DIR/check.mjs" "$DIR/model.mjs" "$B/"
cat > "$B/Dockerfile" <<DOCKER
FROM node:22-bookworm
RUN npm i -g --silent @openai/codex@$CODEX >/dev/null 2>&1 && codex --version
COPY reminal /usr/local/bin/
COPY check.mjs model.mjs /e2e/
ENV HOME=/root SHELL=/bin/bash REMINAL_RELAY=ws://127.0.0.1:1 REMINAL_WEB=http://127.0.0.1:1
CMD ["node", "/e2e/check.mjs"]
DOCKER
docker build -q -t "$IMG" "$B" >/dev/null
docker run --rm "$IMG"
