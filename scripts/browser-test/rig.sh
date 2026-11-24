#!/bin/bash
# Inside the rig's container (run.sh): a throwaway HOME, Chromium on Xvfb with
# reminal's extension loaded, reminal's relay, daemon and one session, and in
# that session the stand-in agent (agent.sh). check.mjs drives it.
set -u
export HOME=/tmp/h PATH=/b:$PATH REMINAL_LOCAL=1 SHELL=/bin/bash DISPLAY=:99
mkdir -p $HOME/.config/chromium /b /tmp/q && cp /out/reminal /b/reminal && chmod +x /b/reminal
# POLICY=1: a managed browser. Its administrator's policy (a file only root
# writes) starts three of the extension's switches on; files has no policy.
if [ -n "${POLICY:-}" ]; then
  mkdir -p /etc/chromium/policies/managed
  echo '{"3rdparty":{"extensions":{"igahbbfpoepnabdbgndnheeieimlhpjh":{"scripts":"on","cookies":"on","passwords":"on"}}}}' > /etc/chromium/policies/managed/reminal.json
fi
Xvfb :99 -screen 0 1280x800x24 >/dev/null 2>&1 & sleep 1
openbox >/dev/null 2>&1 &
node -e '
const http=require("http"),fs=require("fs"),path=require("path");const root="/r/scripts/browser-test/pages";
http.createServer((q,r)=>{const u=q.url.split("?")[0],p=path.join(root,path.normalize(u));setTimeout(()=>fs.readFile(p,(e,b)=>{if(e){r.writeHead(404);return r.end()}r.writeHead(200,{"content-type":"text/html"});r.end(b)}),u==="/slow.html"?4000:0)}).listen(8088)' &
echo "== reminal integrate"; reminal integrate -y 2>&1 | sed 's/^/  /'
reminal relay 8080 > /tmp/relay.log 2>&1 &
sleep 1; reminal daemon > /tmp/daemon.log 2>&1 &
for i in $(seq 1 30); do reminal list >/dev/null 2>&1 && [ -S $HOME/.reminal/browser.sock ] && break; sleep 0.5; done
ls -la $HOME/.reminal/browser.sock | sed 's/^/  /'
out=$(reminal new "web helper" 2>&1)
ID=$(echo "$out" | sed -n 's/^ *Session: *\([A-Z0-9]*\).*/\1/p' | head -1)
echo "== session $ID"
sleep 2
mcp() { ( printf '%s\n%s\n%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"op","version":"1"}}}' '{"jsonrpc":"2.0","method":"notifications/initialized"}' "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"$1\",\"arguments\":$2}}"; sleep 2 ) | reminal mcp 2>/dev/null | grep '"id":2'; }
mcp send_keys "{\"session\":\"$ID\",\"keys\":\"bash /r/scripts/browser-test/agent.sh\",\"enter\":true}" >/dev/null
for i in $(seq 1 20); do mcp read_transcript "{\"session\":\"$ID\"}" | grep -q "stand-in agent ready" && break; sleep 0.5; done
# A second session, with its own stand-in agent: the person is not asked again for it.
out2=$(reminal new "second helper" 2>&1)
ID2=$(echo "$out2" | sed -n 's/^ *Session: *\([A-Z0-9]*\).*/\1/p' | head -1)
echo "== second session $ID2"
sleep 2
mcp send_keys "{\"session\":\"$ID2\",\"keys\":\"bash /r/scripts/browser-test/agent.sh /tmp/q2\",\"enter\":true}" >/dev/null
for i in $(seq 1 20); do mcp read_transcript "{\"session\":\"$ID2\"}" | grep -q "stand-in agent ready" && break; sleep 0.5; done
echo "== the same tools from OUTSIDE the session (a program naming it):"
REMINAL_SESSION=$ID mcp browser_tabs_context '{}' | grep -o '"text":"Error: [^"]*' | head -1 | sed 's/^/  /'
REC=""
if [ -n "${GIF:-}" ]; then ffmpeg -loglevel error -f x11grab -video_size 1280x800 -framerate 10 -i :99 -y /tmp/rec.mp4 & REC=$!; fi
cp /r/scripts/browser-test/check.mjs /e2e/check.mjs
cd /e2e && ID=$ID ID2=$ID2 MODE=${MODE:-full} POLICY=${POLICY:-} node check.mjs; rc=$?
if [ -n "$REC" ]; then
  kill -INT $REC; wait $REC 2>/dev/null
  ffmpeg -loglevel error -i /tmp/rec.mp4 -vf "setpts=PTS/${GIF_SPEED:-1.6},fps=8,scale=960:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=96[p];[b][p]paletteuse=dither=bayer:bayer_scale=4" -y /s/browser.gif && ls -la /s/browser.gif | sed 's/^/  /'
fi
echo "== daemon log (browser):"; grep -i browser /tmp/daemon.log | tail -5 | sed 's/^/  /'
exit $rc
