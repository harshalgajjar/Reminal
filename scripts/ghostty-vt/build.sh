#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# Builds internal/vtwasm/ghostty-vt.wasm: libghostty-vt (MIT, ghostty-org/
# ghostty) for wasm32-freestanding, at the commit and with the Zig pinned
# below, in Docker, and writes its sha256 beside it. go build cannot run Zig,
# so the module is kept in the tree; running this again must give the same
# bytes (vtwasm's test checks the recorded sum).
#
#   build.sh            rebuild and record
#   build.sh --check    rebuild and fail if it differs from what is kept
set -e
GHOSTTY=f6cb4cf1276c3cb1a027468ddeb1a976a822ccfa
ZIG=0.17.0
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT=$(CDPATH= cd -- "$DIR/../.." && pwd)
OUT=$ROOT/internal/vtwasm
B=$(mktemp -d)
trap '"$ROOT/scripts/rm-tmp.sh" "$B"' EXIT
docker run --rm -v "$B":/b -v "${REMINAL_VOLUME_PREFIX:-reminal}-zigcache":/root/.cache/zig -e GHOSTTY=$GHOSTTY -e ZIG=$ZIG debian:bookworm-slim sh -c '
  set -e
  apt-get update -qq >/dev/null && apt-get install -y -qq curl xz-utils git ca-certificates >/dev/null
  A=$(uname -m)
  curl -fsSL "https://ziglang.org/download/$ZIG/zig-$A-linux-$ZIG.tar.xz" -o /tmp/zig.tar.xz
  mkdir -p /opt/zig && tar -xJf /tmp/zig.tar.xz -C /opt/zig --strip-components=1
  git init -q /b/src && cd /b/src && git remote add origin https://github.com/ghostty-org/ghostty
  git fetch -q --depth 1 origin $GHOSTTY && git checkout -q FETCH_HEAD
  /opt/zig/zig build -Demit-lib-vt -Dtarget=wasm32-freestanding -Doptimize=ReleaseSmall
  cp zig-out/bin/ghostty-vt.wasm /b/ghostty-vt.wasm'
SUM=$(sha256sum "$B/ghostty-vt.wasm" 2>/dev/null | cut -d" " -f1 || shasum -a 256 "$B/ghostty-vt.wasm" | cut -d" " -f1)
if [ "${1:-}" = "--check" ]; then
  [ "$SUM" = "$(cut -d" " -f1 "$OUT/ghostty-vt.wasm.sha256")" ] && echo "same: $SUM" && exit 0
  echo "differs: built $SUM, kept $(cat "$OUT/ghostty-vt.wasm.sha256")"; exit 1
fi
cp "$B/ghostty-vt.wasm" "$OUT/ghostty-vt.wasm"
echo "$SUM  ghostty-vt.wasm (ghostty $GHOSTTY, zig $ZIG)" > "$OUT/ghostty-vt.wasm.sha256"
echo "built $SUM"
