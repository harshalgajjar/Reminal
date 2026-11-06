#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# check.mjs: the plugin `reminal integrate` writes for OpenCode (and Kilo
# Code CLI) runs one `reminal hook` at a time and ends on the newest state.
# The plugin is the one this tree writes, run by node against a slow stand-in
# for `reminal hook` that logs when each starts and finishes, fed a turn's
# events as OpenCode sends them. In Docker.
#
#   run.sh [REF]   REF: build reminal from that git ref instead of this tree
#                  (a control: origin/main must fail).
set -e
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT=$(CDPATH= cd -- "$DIR/../.." && pwd)
V=${REMINAL_VOLUME_PREFIX:-reminal}
B=$(mktemp -d)
trap '"$ROOT/scripts/rm-tmp.sh" "$B"' EXIT
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
docker run --rm -v "$B":/e2e:ro -e HOME=/root node:22-bookworm sh -c '
  # A stand-in opencode on PATH, only so integrate writes its plugin.
  printf "#!/bin/sh\necho 1.19.5\n" > /usr/local/bin/opencode && chmod +x /usr/local/bin/opencode
  /e2e/reminal integrate opencode -y >/dev/null && node /e2e/check.mjs'
