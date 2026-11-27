#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# In the container (run.sh): a throwaway HOME, this tree's reminal as relay
# and three sessions on it, then real.mjs in one browser.
set -u
export HOME=/tmp/h PATH=/b:$PATH REMINAL_LOCAL=1 SHELL=/bin/bash
mkdir -p $HOME /b && cp /out/reminal /b/reminal && chmod +x /b/reminal
cp /s/real.mjs /e2e/ && cd /e2e && npm i --silent --no-save playwright@1.56.0 >/dev/null 2>&1
reminal relay 8080 > /tmp/relay.log 2>&1 &
sleep 2
join() { reminal new "$1" 2>&1 | sed -n 's/^ *Join: *//p' | head -1; }
A=$(join sales); B=$(join researcher); C=$(join notes)
sleep 2
JA="$A" JB="$B" JC="$C" node real.mjs
