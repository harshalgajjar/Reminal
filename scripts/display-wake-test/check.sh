#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# A stream started while the display is asleep still gets a picture, for a
# window and for the whole display: the display is put to sleep,
# `reminal-capture serve` is asked for the target, and the frames it sends in
# 12 seconds are counted. Runs on a macOS CI runner (native.yml), where the
# display can be slept with sudo.
#
# Given a helper built with -D REMINAL_TEST_HOOKS as well, it is also run with
# the wake left out, as a display that will not wake: its stream must end
# saying the screen is asleep, not leave the viewer waiting.
#
#   scripts/display-wake-test/check.sh <reminal-capture> [<test-hooks build>]
set -uo pipefail
HELPER=$1
HOOKS=${2:-}
HERE=$(cd "$(dirname "$0")" && pwd)
W=$(mktemp -d)
trap 'osascript -e "quit app \"TextEdit\"" >/dev/null 2>&1; rm -rf "$W"' EXIT
swiftc -O -o "$W/displays" "$HERE/displays.swift" || exit 1
echo "a window to show" > "$W/note.txt"
open -a TextEdit "$W/note.txt"
for _ in $(seq 20); do read -r ID _ WIN < <("$W/displays"); [ "$WIN" != 0 ] && break; sleep 0.5; done
fails=0
for target in "window:$WIN" "display:$ID"; do
  spec=${target#window:}
  [ "$target" = "window:0" ] && { echo "FAIL  no TextEdit window to show"; fails=$((fails+1)); continue; }
  sudo pmset displaysleepnow
  sleep 3
  read -r _ ASLEEP _ < <("$W/displays")
  if [ "$ASLEEP" != 1 ]; then
    echo "SKIP  $target: the display did not go to sleep here, so this proves nothing"
    continue
  fi
  { echo "start 1 $spec 640 45 5 jpeg"; sleep 12; } | "$HELPER" serve > "$W/out" 2> "$W/err"
  read -r _ AFTER _ < <("$W/displays")
  RES=$(perl "$HERE/frames.pl" < "$W/out")
  echo "$target: $RES asleep-after=$AFTER"
  case "$RES" in
    frames=0*) echo "FAIL  $target: no picture from a stream started while the display slept"; fails=$((fails+1)) ;;
    *) echo "PASS  $target: a stream started while the display slept got a picture" ;;
  esac
done
if [ -n "$HOOKS" ] && [ "$WIN" != 0 ]; then
  sudo pmset displaysleepnow
  sleep 3
  # Up to 3s waiting for a wake, then 10s for a first frame: 16s is room.
  { echo "start 1 $WIN 640 45 5 jpeg"; sleep 16; } | REMINAL_NO_DISPLAY_WAKE=1 "$HOOKS" serve > "$W/out" 2> "$W/err"
  RES=$(perl "$HERE/frames.pl" < "$W/out")
  read -r _ AFTER _ < <("$W/displays")
  echo "no wake: $RES asleep-after=$AFTER"
  sed 's/^/  stderr: /' "$W/err" | head -5
  case "$RES" in
    "frames=0 error=This Mac's screen is asleep"*) echo "PASS  a display that does not wake: the stream ends saying the screen is asleep" ;;
    *) echo "FAIL  a display that does not wake: expected the screen-asleep message"; fails=$((fails+1)) ;;
  esac
fi
exit $((fails > 0))
