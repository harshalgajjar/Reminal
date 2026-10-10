// A caller awaiting the next window list (nextWindowList: an app's launch
// baseline, the full desktop's panes) is answered when the host says it
// cannot list windows (unsupported: no desktop) or failed to — never left
// waiting for a list that will not come (run.sh).
import { chromium } from 'playwright';
import http from 'node:http';
import { readFileSync } from 'node:fs';
const html = readFileSync('/t/index.html');
const srv = http.createServer((q, r) => { r.writeHead(200, { 'content-type': 'text/html' }); r.end(html); }).listen(8099);
const b = await chromium.launch();
const page = await b.newPage();
await page.goto('http://127.0.0.1:8099/');
const out = await page.evaluate(async () => {
  sendWin = () => {}; // no host here: the answers below stand in for its
  const settles = (p) => Promise.race([p.then((w) => ({ settled: true, windows: w })), new Promise((r) => setTimeout(() => r({ settled: false }), 2000))]);
  const res = {};
  for (const [name, answer] of [['unsupported', { unsupported: 'This machine has no desktop to list windows on.' }], ['error', { error: 'xdotool: not found' }], ['windows', { windows: [{ id: 'w1', app: 'x', title: 't' }] }]]) {
    const p = nextWindowList();
    onWindowList(answer);
    res[name] = await settles(p);
  }
  return res;
});
console.log(JSON.stringify(out));
const ok = out.unsupported.settled && Array.isArray(out.unsupported.windows) && out.unsupported.windows.length === 0
  && out.error.settled && Array.isArray(out.error.windows) && out.error.windows.length === 0
  && out.windows.settled && out.windows.windows.length === 1;
console.log(ok ? 'PASS  a window-list waiter is answered on unsupported, on error, and on a list'
  : 'FAIL  ' + Object.entries(out).filter(([, v]) => !v.settled).map(([k]) => 'a waiter on "' + k + '" never resolved').join('; '));
await b.close(); srv.close();
process.exit(ok ? 0 : 1);
