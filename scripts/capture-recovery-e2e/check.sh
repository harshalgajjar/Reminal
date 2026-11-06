#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# The daemon's capture end to end on a real Mac: a development `reminal
# daemon` under its own HOME, with a test-hooks reminal-capture beside it, and
# mirror.pl standing in for a session on its capture socket against a TextEdit
# window that never changes. Nothing outside the temporary HOME is touched,
# and only processes under it are signalled. Cases:
#   still     the window streams on its own (the idle refresh, macOS 14+)
#   once      a helper that stalls is replaced and the stream comes back
#   kill      a helper killed mid-stream comes back
#   churn     20 streams opened and closed: one helper, no replacements
#   always    every helper stalls: replacements back off, never a storm
#   retire    with no streams the helper leaves after its grace
#   headless  closed-lid mode on a Mac the census is told is headless
#             (REMINAL_FORCE_VDISPLAY) makes its virtual display, capture
#             works, and it goes when a real display is found again
# On macOS 13 there is no idle refresh: the replacement is off by design,
# and once/always must show no replacement at all.
#
#   scripts/capture-recovery-e2e/check.sh <reminal> <test-hooks reminal-capture>
set -uo pipefail
REMINAL=$1 HOOKS=$2
HERE=$(cd "$(dirname "$0")" && pwd)
W=$(mktemp -d /tmp/capture-e2e.XXXXXX)
MAJOR=$(sw_vers -productVersion | cut -d. -f1)
mkdir -p "$W/bin" "$W/home/.reminal"
cp "$REMINAL" "$W/bin/reminal"; cp "$HOOKS" "$W/bin/reminal-capture"; chmod +x "$W/bin/"*
HELPER="$W/bin/reminal-capture serve"
SOCK="$W/home/.reminal/mirror.sock"
DPID=
cleanup() {
  [ -n "$DPID" ] && kill "$DPID" 2>/dev/null
  pkill -f "$W/bin/" 2>/dev/null
  osascript -e 'tell application "TextEdit" to close (every document whose name is "note.txt")' >/dev/null 2>&1
  rm -rf "$W"
}
trap cleanup EXIT
fails=0
expect() { if [ "$2" = 1 ]; then echo "PASS  $1"; else echo "FAIL  $1"; fails=$((fails+1)); fi; }
helpers() { pgrep -f "$HELPER" | wc -l | tr -d ' '; }
replaced() { grep -c "starting a fresh capture helper" "$W/daemon.log"; }
field() { sed -nE "s/.*summary.* $1=([0-9.]+).*/\1/p" "$W/m.out"; }
mirror() { perl "$HERE/mirror.pl" "$SOCK" "$WIN" "$1" > "$W/m.out"; tail -1 "$W/m.out"; }
ge() { awk -v a="$1" -v b="$2" 'BEGIN{exit !(a>=b)}'; }
daemon() { # daemon [env...]: (re)start the development daemon
  [ -n "$DPID" ] && { kill "$DPID"; wait "$DPID" 2>/dev/null; pkill -f "$W/bin/"; sleep 1; }
  env HOME="$W/home" REMINAL_RELAY=ws://127.0.0.1:1/ws REMINAL_WEB=http://127.0.0.1:1 \
    REMINAL_NO_RESTORE=1 REMINAL_CAPTURE_STALL="$W/stall" "$@" "$W/bin/reminal" daemon >> "$W/daemon.log" 2>&1 &
  DPID=$!
  for _ in $(seq 50); do [ -S "$SOCK" ] && return; sleep 0.2; done
  echo "FAIL  the daemon never opened its capture socket"; tail -5 "$W/daemon.log"; exit 1
}
fresh() { pkill -f "$HELPER"; sleep 1; } # the next stream starts a new helper

swiftc -O -o "$W/window" "$HERE/window.swift" || exit 1
echo "a window that never changes" > "$W/note.txt"
open -a TextEdit "$W/note.txt"
WIN=0; for _ in $(seq 20); do WIN=$("$W/window"); [ "$WIN" != 0 ] && break; sleep 0.5; done
[ "$WIN" = 0 ] && { echo "FAIL  no TextEdit window"; exit 1; }
echo "macOS $(sw_vers -productVersion), window $WIN"
daemon

echo "== still"; mirror 12
if [ "$MAJOR" -ge 14 ]; then
  expect "a still window streams: $(field frames) frames in 12s, longest gap $(field maxgap)s" "$(ge "$(field frames)" 8 && ! ge "$(field maxgap)" 2.5 && echo 1)"
else
  expect "a still window gives its first picture: $(field frames) frame(s)" "$(ge "$(field frames)" 1 && echo 1)"
fi

echo "== once"; echo once > "$W/stall"; fresh; r0=$(replaced); mirror 16
if [ "$MAJOR" -ge 14 ]; then
  expect "a stalled helper is replaced once ($(( $(replaced)-r0 ))) and the picture is back at $(field first)s" \
    "$([ $(( $(replaced)-r0 )) = 1 ] && ge "$(field first)" 4 && ! ge "$(field first)" 10 && grep -q 'capture service restarted' "$W/m.out" && echo 1)"
else
  expect "macOS 13: no replacement ($(( $(replaced)-r0 )))" "$([ $(( $(replaced)-r0 )) = 0 ] && echo 1)"
fi
rm -f "$W/stall"

echo "== kill"; fresh
( sleep 5; pkill -9 -f "$HELPER" ) & mirror 14
last=$(awk '$2=="frame"{l=$1} END{print l+0}' "$W/m.out")
expect "a helper killed at 5s: $(field ends) stream end(s), $(field frames) frames, longest gap $(field maxgap)s" \
  "$(ge "$(field ends)" 2 && ! ge "$(field maxgap)" 4 && echo 1)"

echo "== churn"; r0=$(replaced); most=0
for i in $(seq 20); do perl "$HERE/mirror.pl" "$SOCK" "$WIN" 0.6 > /dev/null; n=$(helpers); [ "$n" -gt "$most" ] && most=$n; done
expect "20 streams opened and closed: at most $most helper, $(( $(replaced)-r0 )) replacements" "$([ "$most" -le 1 ] && [ $(( $(replaced)-r0 )) = 0 ] && echo 1)"

echo "== always"; echo always > "$W/stall"; fresh; r0=$(replaced); most=0
( for _ in $(seq 84); do n=$(helpers); echo "$n"; sleep 0.5; done ) > "$W/count" & SAMPLER=$!
mirror 42; wait $SAMPLER; most=$(sort -n "$W/count" | tail -1)
n=$(( $(replaced)-r0 ))
if [ "$MAJOR" -ge 14 ]; then
  expect "every helper stalls for 42s: $n replacements (backing off 5, 10, 20s), at most $most helper at once" "$([ "$n" -ge 2 ] && [ "$n" -le 4 ] && [ "$most" -le 1 ] && echo 1)"
else
  expect "macOS 13: every helper stalls, no replacement ($n)" "$([ "$n" = 0 ] && echo 1)"
fi
grep "starting a fresh capture helper" "$W/daemon.log" | tail -4 | sed 's/^/  log: /'
rm -f "$W/stall"

echo "== retire"; sleep 33
expect "no streams: the helper has left ($(helpers) left)" "$([ "$(helpers)" = 0 ] && echo 1)"

echo "== headless"; echo '{"closed_lid": true}' > "$W/home/.reminal/settings.json"; echo 1 > "$W/headless"
daemon REMINAL_FORCE_VDISPLAY="$W/headless"
for _ in $(seq 40); do [ -f "$W/home/.reminal/vdisplay.pid" ] && break; sleep 1; done
screens=$(osascript -l JavaScript -e 'ObjC.import("AppKit"); var s=$.NSScreen.screens, o=[]; for (var i=0;i<s.count;i++) o.push(ObjC.unwrap(s.objectAtIndex(i).localizedName)); o.join(",")')
expect "headless: the virtual display is made (screens: $screens)" "$([ -f "$W/home/.reminal/vdisplay.pid" ] && grep -qi reminal <<<"$screens" && echo 1)"
vp=$(cat "$W/home/.reminal/vdisplay.pid" 2>/dev/null); sleep 30
expect "headless: the virtual display stays, not remade (pid $vp, then $(cat "$W/home/.reminal/vdisplay.pid" 2>/dev/null))" "$([ -n "$vp" ] && [ "$(cat "$W/home/.reminal/vdisplay.pid" 2>/dev/null)" = "$vp" ] && kill -0 "$vp" 2>/dev/null && echo 1)"
mirror 6
expect "headless: the window is captured with it up ($(field frames) frames)" "$(ge "$(field frames)" 1 && echo 1)"
echo 0 > "$W/headless"
for _ in $(seq 40); do [ -f "$W/home/.reminal/vdisplay.pid" ] || break; sleep 1; done
expect "a real display again: the virtual display goes" "$([ ! -f "$W/home/.reminal/vdisplay.pid" ] && echo 1)"
grep -i "liddim" "$W/daemon.log" | head -2 | sed 's/^/  log: /'
echo "fails=$fails"
exit $((fails > 0))
