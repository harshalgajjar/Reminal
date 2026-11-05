#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# `reminal-capture serve` against a window that never changes, on a macOS CI
# runner (native.yml):
#   - it keeps sending the picture (the idle refresh), several a second's worth
#     in 8 seconds;
#   - a test build told to stall (REMINAL_CAPTURE_STALL) sends nothing, and
#     says so on stderr: a screenshot that never answers is given up on and
#     tried again, not waited on for good;
#   - told to stall "once", only the first process stalls; the next streams.
#
#   scripts/capture-stall-test/check.sh <reminal-capture> <test-hooks build>
set -uo pipefail
HELPER=$1 HOOKS=$2
HERE=$(cd "$(dirname "$0")" && pwd)
W=$(mktemp -d)
trap 'osascript -e "quit app \"TextEdit\"" >/dev/null 2>&1; rm -rf "$W"' EXIT
swiftc -O -o "$W/window" "$HERE/window.swift" || exit 1
echo "a window that never changes" > "$W/note.txt"
open -a TextEdit "$W/note.txt"
WIN=0
for _ in $(seq 20); do WIN=$("$W/window"); [ "$WIN" != 0 ] && break; sleep 0.5; done
[ "$WIN" = 0 ] && { echo "FAIL  no TextEdit window to capture"; exit 1; }
fails=0
# stream <helper> [env...]: 8 seconds of the window; prints "frames=<n> error=<e>".
stream() {
  local h=$1; shift
  { echo "start 1 $WIN 640 45 5 jpeg"; sleep 8; } | env "$@" "$h" serve > "$W/out" 2> "$W/err"
  perl "$HERE/frames.pl" < "$W/out"
}
expect() { # expect <what> <ok?>
  if [ "$2" = 1 ]; then echo "PASS  $1"; else echo "FAIL  $1"; fails=$((fails+1)); fi
}
frames() { sed -E 's/^frames=([0-9]+).*/\1/' <<<"$1"; }

R=$(stream "$HELPER"); echo "still window: $R"
expect "a window that never changes keeps streaming" "$([ "$(frames "$R")" -ge 5 ] && echo 1)"

echo always > "$W/stall"
R=$(stream "$HOOKS" REMINAL_CAPTURE_STALL="$W/stall"); echo "stalled: $R"
sed 's/^/  stderr: /' "$W/err" | head -4
expect "a stalled helper sends nothing" "$([ "$(frames "$R")" = 0 ] && echo 1)"
expect "a stalled helper says its screenshots go unanswered" "$(grep -q 'gave no answer in 2s' "$W/err" && echo 1)"

echo once > "$W/stall"
R1=$(stream "$HOOKS" REMINAL_CAPTURE_STALL="$W/stall")
R2=$(stream "$HOOKS" REMINAL_CAPTURE_STALL="$W/stall"); echo "once: first $R1, next $R2"
expect "stalled once: the first process stalls, the next streams" "$([ "$(frames "$R1")" = 0 ] && [ "$(frames "$R2")" -ge 5 ] && echo 1)"
exit $((fails > 0))
