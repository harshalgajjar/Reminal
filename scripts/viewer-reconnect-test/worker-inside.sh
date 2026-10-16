#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# Inside run-worker.sh's container.
set -u
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null 2>&1; apt-get install -y -qq iproute2 python3 >/dev/null 2>&1
# The Worker beside the viewer's source, as in the tree: wrangler builds the
# page from it (wrangler.toml [build]).
mkdir -p /x/internal/client && cp -r /web /x/internal/client/web && cp -r /cf /x/cloudflare && cd /x/cloudflare && rm -rf node_modules
# wrangler dev hands the Worker the Host of the routes in wrangler.toml, and
# builds its containers (if it has any): this copy has neither.
python3 - <<'PY'
import re
p = "wrangler.toml"; s = open(p).read()
s = re.sub(r"^routes = \[.*?^\]\n", "", s, flags=re.S | re.M)
s = re.sub(r"^\[\[containers\]\]\n.*?(?=^\[)", "", s, flags=re.S | re.M)
open(p, "w").write(s)
PY
npm ci --silent >/dev/null 2>&1
WRANGLER_SEND_METRICS=false npx wrangler dev --local --ip 127.0.0.1 --port 8787 >/tmp/wrangler.log 2>&1 &
for i in $(seq 1 90); do curl -s -m 2 -o /dev/null http://127.0.0.1:8787/ && break; sleep 1; done
export HOME=/root/h REMINAL_RELAY=ws://127.0.0.1:8787/ws REMINAL_WEB=http://127.0.0.1:8787
mkdir -p $HOME && cd $HOME
/b/reminal new alpha >/dev/null 2>&1; SHELL=/t/tui.sh REMINAL=/b/reminal /b/reminal new tui >/dev/null 2>&1; sleep 2
S=$(/b/reminal info alpha | awk '/Session:/{print $2}'); PIN=$(/b/reminal info alpha | awk '/PIN:/{print $2}')
TS=$(/b/reminal info tui | awk '/Session:/{print $2}'); TPIN=$(/b/reminal info tui | awk '/PIN:/{print $2}')
echo "session: $S   link: $DELAY each way, $RATE   relay: the Worker (wrangler dev)"
tc qdisc add dev lo root netem delay "$DELAY" rate "$RATE"
mkdir -p /e2e && cd /e2e && npm i --silent --no-save playwright@1.56.0 >/dev/null 2>&1
sed 's#http://127.0.0.1:18080#http://localhost:8787#' /t/checks.mjs > checks.mjs
cp /t/bookkeeping.mjs .
f=0
node checks.mjs "$S" "$PIN" "$TS" "$TPIN" || f=1
CONNECTIONS="/b/reminal connections" node bookkeeping.mjs "$S" "$PIN" || f=1
exit $f
