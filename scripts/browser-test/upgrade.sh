#!/bin/bash
# Inside the rig's container (run.sh, MODE=upgrade): reminal upgraded in
# place, with the browser closed while its files change. The release before
# (/out/reminal-prev) sets up, runs, and its extension is used in a profile;
# everything stops; this reminal (/out/reminal) takes its place at the same
# paths; the browser starts again on the same profile. upgrade.mjs checks
# that the tools work with nothing done by hand, and that the extension's
# running code is this reminal's.
set -u
export HOME=/tmp/h PATH=/b:$PATH REMINAL_LOCAL=1 SHELL=/bin/bash DISPLAY=:99
mkdir -p $HOME/.config/chromium /b
Xvfb :99 -screen 0 1280x800x24 >/dev/null 2>&1 & sleep 1
openbox >/dev/null 2>&1 &
node -e '
const http=require("http"),fs=require("fs"),path=require("path");const root="/r/scripts/browser-test/pages";
http.createServer((q,r)=>{const u=q.url.split("?")[0],p=path.join(root,path.normalize(u));fs.readFile(p,(e,b)=>{if(e){r.writeHead(404);return r.end()}r.writeHead(200,{"content-type":"text/html"});r.end(b)})}).listen(8088)' &
mcp() { ( printf '%s\n%s\n%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"op","version":"1"}}}' '{"jsonrpc":"2.0","method":"notifications/initialized"}' "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"$1\",\"arguments\":$2}}"; sleep 2 ) | reminal mcp 2>/dev/null | grep '"id":2'; }
# up <binary> <queue>: that reminal set up and running, with one session and
# its stand-in agent (agent.sh) answering in <queue>.
up() {
  cp "$1" /b/reminal.new && chmod +x /b/reminal.new && mv /b/reminal.new /b/reminal
  echo "== $(reminal --version 2>&1 | head -1): integrate"; reminal integrate -y >/dev/null 2>&1
  reminal relay 8080 > /tmp/relay.log 2>&1 & RELAY=$!
  sleep 1; reminal daemon > /tmp/daemon.log 2>&1 & DAEMON=$!
  for i in $(seq 1 30); do reminal list >/dev/null 2>&1 && [ -S $HOME/.reminal/browser.sock ] && break; sleep 0.5; done
  ID=$(reminal new "web helper" 2>&1 | sed -n 's/^ *Session: *\([A-Z0-9]*\).*/\1/p' | head -1)
  echo "== session $ID"; sleep 2
  mkdir -p "$2"
  mcp send_keys "{\"session\":\"$ID\",\"keys\":\"bash /r/scripts/browser-test/agent.sh $2\",\"enter\":true}" >/dev/null
  for i in $(seq 1 20); do mcp read_transcript "{\"session\":\"$ID\"}" | grep -q "stand-in agent ready" && break; sleep 0.5; done
}
down() {
  reminal kill "$ID" >/dev/null 2>&1
  kill $DAEMON $RELAY 2>/dev/null; wait $DAEMON $RELAY 2>/dev/null
  rm -f $HOME/.reminal/browser.sock
}
cp /r/scripts/browser-test/upgrade.mjs /e2e/upgrade.mjs
cd /e2e
up /out/reminal-prev /tmp/q1
PHASE=before Q=/tmp/q1 node upgrade.mjs; rc=$?
down
[ $rc = 0 ] || { echo "the release before did not work here: nothing to upgrade from"; exit $rc; }
up /out/reminal /tmp/q2
PHASE=after Q=/tmp/q2 node upgrade.mjs; rc=$?
echo "== daemon log (browser):"; grep -i browser /tmp/daemon.log | tail -5 | sed 's/^/  /'
exit $rc
