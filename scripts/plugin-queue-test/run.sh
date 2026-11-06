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
  /e2e/reminal integrate opencode -y >/dev/null && node /e2e/check.mjs || exit 1
  # An older plugin, as an upgrade leaves it: a session starting puts back
  # the one this binary writes, with nobody running integrate.
  P=/root/.config/opencode/plugin/reminal.js
  cp $P /tmp/current.js && sed -i "/let writing = false;/d" $P
  cmp -s $P /tmp/current.js && { echo "FAIL the older plugin is not older"; exit 1; }
  cp /e2e/reminal /usr/local/bin/reminal
  REMINAL_RELAY=ws://127.0.0.1:1 REMINAL_WEB=http://127.0.0.1:1 SHELL=/bin/bash reminal new healed >/dev/null 2>&1
  for i in 1 2 3 4 5 6 7 8 9 10; do cmp -s $P /tmp/current.js && break; sleep 1; done
  if cmp -s $P /tmp/current.js; then echo "ok   a session starting brought the older plugin up to date"; else echo "FAIL the older plugin was left as it was"; exit 1; fi'
