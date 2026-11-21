#!/bin/bash
# Inside the container (run.sh): reminal's relay, daemon and one session;
# Chromium with reminal's extension; then the same tasks through reminal's
# browser tools (from inside the session) and through Playwright MCP.
set -u
export HOME=/tmp/h PATH=/b:$PATH REMINAL_LOCAL=1 SHELL=/bin/bash DISPLAY=:99
mkdir -p $HOME/.config/chromium /b && cp /out/reminal /b/reminal && chmod +x /b/reminal
Xvfb :99 -screen 0 1280x800x24 >/dev/null 2>&1 & sleep 1
openbox >/dev/null 2>&1 &
node -e '
const http=require("http"),fs=require("fs"),path=require("path");const root="/r/scripts/browser-test/pages";
http.createServer((q,r)=>{const u=q.url.split("?")[0],p=path.join(root,path.normalize(u));fs.readFile(p,(e,b)=>{if(e){r.writeHead(404);return r.end()}r.writeHead(200,{"content-type":"text/html"});r.end(b)})}).listen(8088)' &
mkdir -p /pwmcp && (cd /pwmcp && npm init -y >/dev/null 2>&1 && npm i --silent @playwright/mcp@${PWMCP:-0.0.86} >/dev/null 2>&1)
echo "playwright mcp $(node -e 'console.log(require("/pwmcp/node_modules/@playwright/mcp/package.json").version)' 2>&1)"
reminal integrate -y >/dev/null 2>&1
reminal relay 8080 > /tmp/relay.log 2>&1 &
sleep 1; reminal daemon > /tmp/daemon.log 2>&1 &
for i in $(seq 1 30); do reminal list >/dev/null 2>&1 && [ -S $HOME/.reminal/browser.sock ] && break; sleep 0.5; done
ID=$(reminal new "bench" 2>&1 | sed -n 's/^ *Session: *\([A-Z0-9]*\).*/\1/p' | head -1)
cp /r/scripts/browser-test/bench/*.mjs /e2e/
sleep 3
(cd /e2e && node launch.mjs > /tmp/launch.log 2>&1 &) ; for i in $(seq 1 180); do [ -e /tmp/bench.browser ] && break; sleep 0.5; done; sleep 3
mcp() { ( printf '%s\n%s\n%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"op","version":"1"}}}' '{"jsonrpc":"2.0","method":"notifications/initialized"}' "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"$1\",\"arguments\":$2}}"; sleep 2 ) | reminal mcp 2>/dev/null | grep '"id":2'; }
# Ours runs inside the session, as an agent's tools do.
mcp send_keys "{\"session\":\"$ID\",\"keys\":\"cd /e2e && RUNS=${RUNS:-5} node bench.mjs ours /tmp/ours.json > /tmp/ours.log 2>&1; touch /tmp/ours.done\",\"enter\":true}" > /tmp/send.out 2>&1; echo "session $ID; sent: $(cut -c1-200 /tmp/send.out)"
for i in $(seq 1 ${OURS_WAIT:-300}); do [ -e /tmp/ours.done ] && break; sleep 1; done
[ -e /tmp/ours.done ] || { echo "ours did not finish; the session shows:"; mcp read_transcript "{\"session\":\"$ID\"}" | tail -c 700; }
touch /tmp/bench.done; sleep 2
CHROME=$(ls -d /ms-playwright/chromium-*/chrome-linux/chrome | head -1)
(cd /e2e && CHROME=$CHROME SCHEMAS=/s/pw-tools.json RUNS=${RUNS:-5} node bench.mjs playwright /tmp/pw.json)
cp /tmp/ours.log /tmp/launch.log /s/ 2>/dev/null; reminal list > /s/list.txt 2>&1
node -e '
const a=JSON.parse(require("fs").readFileSync("/tmp/ours.json")),b=JSON.parse(require("fs").readFileSync("/tmp/pw.json"));
const pad=(s,n)=>String(s).padEnd(n);
console.log(pad("task",36)+pad("reminal",26)+"Playwright MCP");
console.log(pad("first page open",36)+pad(a.firstPageMs+" ms",26)+b.firstPageMs+" ms");
for(const k of Object.keys(a.tasks)){const x=a.tasks[k],y=b.tasks[k];const f=(t)=>t.ms==null?"FAILED: "+t.why.slice(0,60):`${t.ms} ms, ${t.chars} chars, ${t.calls} calls`+(t.failed?` (${t.failed} failed)`:"");console.log(pad(k,36)+pad(f(x),26+14)+f(y));}'
cp /tmp/ours.json /tmp/pw.json /s/ 2>/dev/null
