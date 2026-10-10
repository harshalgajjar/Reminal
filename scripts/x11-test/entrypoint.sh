#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# Bring up a headless X11 desktop, put one window on it, then run whatever
# command was passed (default: the gated X11 backend tests).
set -e

SCREEN="${SCREEN:-1280x800x24}"

# XVFB_FBDIR: the screen kept in a file there (-fbdir), which the window
# backend then reads frames from instead of running import (xvfbfb.go).
FBDIR_ARGS=""
if [ -n "${XVFB_FBDIR:-}" ]; then
    mkdir -p "$XVFB_FBDIR"
    FBDIR_ARGS="-fbdir $XVFB_FBDIR"
fi
Xvfb "$DISPLAY" -screen 0 "$SCREEN" -nolisten tcp $FBDIR_ARGS >/tmp/xvfb.log 2>&1 &
# Wait for the server to accept connections rather than sleeping a guess —
# openbox exits immediately if it starts first, and then nothing has a window
# manager, which makes wmctrl report zero windows for reasons unrelated to the
# code under test.
for _ in $(seq 50); do
    xdpyinfo >/dev/null 2>&1 && break
    sleep 0.1
done
xdpyinfo >/dev/null 2>&1 || { echo "Xvfb never came up:"; cat /tmp/xvfb.log; exit 1; }

openbox >/tmp/openbox.log 2>&1 &

# Wait for the window manager to actually own the screen before opening
# anything. `wmctrl -m` fails until a WM has claimed the selection, and a window
# mapped before that never gets managed — which shows up later as wmctrl listing
# nothing, looking exactly like a backend bug.
for _ in $(seq 100); do
    wmctrl -m >/dev/null 2>&1 && break
    sleep 0.1
done
wmctrl -m >/dev/null 2>&1 || { echo "no window manager:"; cat /tmp/openbox.log; exit 1; }

# A window to point the tests at. `sh -i` keeps it alive and lets typed input do
# something observable.
xterm -geometry 80x24+60+40 -T xterm -e sh -i >/tmp/xterm.log 2>&1 &

for _ in $(seq 100); do
    [ -n "$(wmctrl -l 2>/dev/null)" ] && break
    sleep 0.1
done
if [ -z "$(wmctrl -l 2>/dev/null)" ]; then
    echo "no managed windows appeared; openbox log:"; cat /tmp/openbox.log
    echo "xterm log:"; cat /tmp/xterm.log
    exit 1
fi

echo "X11 ready on $DISPLAY:"
wmctrl -lGx

if [ "$#" -eq 0 ]; then
    set -- go test ./internal/client/ -run 'TestX11' -v -count=1
fi
exec "$@"
