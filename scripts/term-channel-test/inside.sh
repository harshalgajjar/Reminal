#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# In the container (run.sh): a throwaway HOME, reminal's relay and one
# session on it, the delaying proxy in front of the relay, then check.mjs.
set -u
export HOME=/tmp/h PATH=/b:$PATH REMINAL_LOCAL=1 SHELL=/bin/bash
mkdir -p $HOME /b && cp /out/reminal /b/reminal && chmod +x /b/reminal
cp /s/*.mjs /e2e/ && cd /e2e && npm i --silent --no-save playwright@1.56.0 >/dev/null 2>&1
reminal relay 8080 > /tmp/relay.log 2>&1 &
sleep 2
JOIN=$(reminal new "term channel" 2>&1 | sed -n 's/^ *Join: *//p' | head -1)
sleep 2
node proxy.mjs 8081 8080 "$DELAY" & sleep 1
URL=$(echo "$JOIN" | sed 's/:8080/:8081/') DELAY=$DELAY node check.mjs
