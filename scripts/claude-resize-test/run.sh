#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# A real Claude Code (pinned below) in a real reminal session, answering a
# long headed story from a stand-in model while the ONLY viewer, the real
# web page in a browser, changes size under it the way a phone does: the
# keyboard opening and closing ten times (102x45 <-> 102x24), then the phone
# turned (102x49 -> 55x58 -> 102x49), with one reconnect on a wider grid
# in between (a folding phone's inner screen: the join path). These are the
# twelve PTY resizes recorded from such a session. check.mjs then reads what
# the viewer's terminal holds: every sentence present, no staircase, Claude's
# rule and status rows single rows, and how many passages are doubled.
# All in Docker: the box, and a Playwright container for the browser;
# nothing here touches the host's reminal.
#
#   scripts/claude-resize-test/run.sh [REF]   REF: build reminal from that
#                                             git ref (a control: today's
#                                             main must show the doubling)
set -euo pipefail
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT=$(CDPATH= cd -- "$DIR/../.." && pwd)
V=${REMINAL_VOLUME_PREFIX:-reminal}
CLAUDE=${CLAUDE_VERSION:-2.1.319}
PW=mcr.microsoft.com/playwright:v1.56.0-noble
NAME=reminal-claude-resize-$$
B=$(mktemp -d)
trap 'docker rm -f "$NAME" >/dev/null 2>&1 || true; docker image rm "$NAME" >/dev/null 2>&1 || true; "$ROOT/scripts/rm-tmp.sh" "$B"' EXIT
SRC=$ROOT
if [ -n "${1:-}" ]; then
  mkdir -p "$B/src" && git -C "$ROOT" archive "$1" | tar -x -C "$B/src" && SRC=$B/src
fi
docker run --rm -v "$SRC":/src:ro -v "$B":/out -v "$V-gocache":/root/.cache/go-build -v "$V-gomod":/go/pkg/mod \
  -e GOFLAGS=-buildvcs=false -w /src golang:1.26-bookworm sh -c 'go build -o /out/relay ./cmd/reminal && cp /out/relay /out/agent'
cp "$DIR/Dockerfile" "$DIR/model.mjs" "$DIR/setup.sh" "$B/"
docker build -q -t "$NAME" --build-arg CLAUDE_VERSION="$CLAUDE" "$B" >/dev/null
docker run -d --name "$NAME" "$NAME" >/dev/null
for _ in $(seq 40); do docker exec "$NAME" sh -c 'grep -q "model on" /tmp/model.log 2>/dev/null && test -f /root/.claude.json' && break; sleep 0.5; done
sleep 2
# Twice, a fresh session each: without the keyboard (judged on sentences
# lost) and with it (judged on layout; a height shrink loses rows to Claude
# Code's own redraw in any terminal).
# KEYBOARDS picks the runs (default both); RESIZE_OUT a directory the
# viewer rows each run ends with are written to (story<KB>.txt).
OUT=${RESIZE_OUT:-$B/out}
mkdir -p "$OUT"
for KB in ${KEYBOARDS:-0 1}; do
  S=story$KB
  docker exec "$NAME" sh -c "mkdir -p /root/$S && cd /root/$S && SHELL=/usr/local/bin/claude-shell reminal new $S >/dev/null 2>&1"
  read -r ID PIN < <(docker exec "$NAME" sh -c "reminal info $S | grep -E 'Session:|PIN:' | awk '{print \$2}'" | tr '\n' ' '; echo)
  echo "== KEYBOARD=$KB: session $ID (claude $CLAUDE, reminal from ${1:-this tree})"
  docker run --rm --network "container:$NAME" -v "$DIR:/t:ro" -v "$OUT:/out" -w /tmp -e KEYBOARD=$KB -e DUMP=/out/story$KB.txt "$PW" sh -c \
    'npm i -s playwright@1.56.0 >/dev/null 2>&1 && cp /t/check.mjs /t/model.mjs . && node check.mjs "$@"' _ "$ID" "$PIN" || exit 1
done
