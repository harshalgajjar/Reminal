// A window pane whose host keeps sending heartbeats but no picture says
// Connecting, then after 10 seconds says why, in plain words (a minimized
// window, Screen Recording off); the first frame takes the message away
// (run.sh).
import { chromium } from 'playwright';
import http from 'node:http';
import { readFileSync } from 'node:fs';
const html = readFileSync('/t/index.html');
const srv = http.createServer((q, r) => { const a = /^\/assets\/([\w.-]+\.js)$/.exec(q.url.split('?')[0]); if (a) { let b; try { b = readFileSync('/t/assets/' + a[1]); } catch (_) { r.writeHead(404); return r.end(); } r.writeHead(200, { 'content-type': 'text/javascript' }); return r.end(b); } if (q.url.split('?')[0] === '/reload.js') { r.writeHead(200, { 'content-type': 'text/javascript' }); return r.end(readFileSync('/t/reload.js')); } r.writeHead(200, { 'content-type': 'text/html' }); r.end(html); }).listen(8099);
const b = await chromium.launch();
const page = await b.newPage({ viewport: { width: 1280, height: 800 } });
await page.goto('http://127.0.0.1:8099/');
const got = await page.evaluate(async () => {
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  // No host here: these stand in for it.
  sendWin = () => {};
  document.body.classList.add('connected');
  const w = { id: 'w1', app: 'TextEdit', title: 'note', w: 800, h: 600 };
  openWinView(w);
  const p = panes.get(w.id);
  const said = () => { updatePaneOverlay(p); return p.el.classList.contains('loading') ? p.loadMsg.textContent : ''; };
  const at = {};
  // Heartbeats every two seconds, the way an idle host sends them, and no frame.
  for (let s = 0; s <= 12; s += 2) {
    if (s === 6) at.six = said();
    onWindowFrame({ id: w.id, hb: true });
    await sleep(2000);
  }
  at.twelve = said();
  onWindowFrame({ id: w.id, w: 800, h: 600, seq: 1, img: btoa('a frame') });
  at.framed = said();
  return at;
});
console.log(JSON.stringify(got));
const bad = [];
if (got.six !== 'Connecting…') bad.push('at 6s it says ' + JSON.stringify(got.six) + ', not Connecting');
if (!/^No picture from this window yet/.test(got.twelve) || !/minimized/.test(got.twelve) || !/Screen Recording/.test(got.twelve) || /…/.test(got.twelve)) bad.push('at 12s it says ' + JSON.stringify(got.twelve));
if (got.framed !== '') bad.push('a frame left ' + JSON.stringify(got.framed) + ' showing');
console.log(bad.length ? 'FAIL  ' + bad.join('; ') : 'PASS  Connecting, then after 10s of no picture a plain reason, and the first frame clears it');
await b.close(); srv.close();
process.exit(bad.length ? 1 : 0);
