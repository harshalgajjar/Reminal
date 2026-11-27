// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// The session dock's order (run.sh): two tabs of one browser, which share the
// history the dock is ordered by. The first moves between two sessions; the
// second was left on a third and connects to it again and again, as a tab
// does when the screen wakes. The dock in the first tab keeps the order the
// person moved in: the session just left right after the current one, the
// third never jumping ahead of it. No host or relay: no socket is opened.
import { chromium } from 'playwright';
import http from 'node:http';
import { readFileSync } from 'node:fs';
const html = readFileSync('/t/index.html');
// The page as built: its own files by their paths, the page itself for the rest.
const types = { js: 'text/javascript', css: 'text/css', json: 'application/json', html: 'text/html', wasm: 'application/wasm' };
http.createServer((q, r) => {
  const path = q.url.split('?')[0];
  if (/^\/[\w./-]+\.\w+$/.test(path) && !path.includes('..')) {
    try { const b = readFileSync('/t' + path); r.writeHead(200, { 'content-type': types[path.split('.').pop()] || 'application/octet-stream' }); return r.end(b); } catch (_) {}
  }
  r.writeHead(200, { 'content-type': 'text/html' }); r.end(html);
}).listen(8099);
const browser = await chromium.launch();
const ctx = await browser.newContext();
const tab = async () => {
  const p = await ctx.newPage();
  p.on('pageerror', (e) => console.log('page error: ' + String(e).slice(0, 200)));
  await p.goto('http://127.0.0.1:8099/');
  // The page's script has run to its end (its last declarations are made).
  await p.waitForFunction(() => { try { return typeof pinForSession === 'string' && typeof refreshSessdockPool === 'function'; } catch (_) { return false; } }, null, { timeout: 20000 });
  await p.evaluate(() => { openSocket = () => {}; });
  return p;
};
const one = await tab(), two = await tab();
const A = 'AAAA1111', B = 'BBBB2222', C = 'CCCC3333';
const pause = () => new Promise((r) => setTimeout(r, 12));
// The page is connected: what its answer to the handshake has it note. (A
// page of before this noted every connection the same way.)
const authed = async (p, id) => { await p.evaluate((id) => (typeof noteSessionAuthed === 'function' ? noteSessionAuthed : rememberSession)(id, '123456'), id); await pause(); };
// The person moves the tab to a session, and it connects.
const move = async (p, id) => { await p.evaluate((id) => connectToSession(id, '123456'), id); await pause(); await authed(p, id); };
const order = (p) => p.evaluate(async () => { await refreshSessdockPool(); return sessdockPool.map((x) => x.id).join(' '); });
const fails = [];
const check = async (want, what) => { const got = await order(one); const ok = got === want.join(' '); console.log((ok ? 'ok   ' : 'FAIL ') + what + ': ' + got); if (!ok) fails.push(what); };

await move(two, C); // the other tab, opened on C a while ago
await move(one, A);
await move(one, B);
await check([B, A, C], 'moved to A, then to B');
await authed(two, C); // the other tab connects to C again
await check([B, A, C], 'the other tab connected to C again');
for (let i = 1; i <= 3; i++) {
  await move(one, A);
  await check([A, B, C], `round ${i}: back to A, the one just left is second`);
  await authed(two, C);
  await check([A, B, C], `round ${i}: C connected again in its tab`);
  await authed(one, A); // this tab's own connection, made again
  await check([A, B, C], `round ${i}: this tab connected to A again`);
  await move(one, B);
  await check([B, A, C], `round ${i}: to B, the one just left is second`);
  await authed(two, C);
  await authed(one, B);
  await check([B, A, C], `round ${i}: both tabs connected again`);
}
// A move whose connection has not come yet: the one left is still next.
await authed(two, C);
await one.evaluate((id) => connectToSession(id, '123456'), A); await pause();
await check([A, B, C], 'moved to A, not connected yet: B, just left, is second');
// The person does move to C, in this tab: then it is a visit, and it ranks.
await authed(one, A);
await move(one, C);
await check([C, A, B], 'moved to C in this tab: now it is first, the one left second');
await browser.close();
console.log(fails.length ? `${fails.length} failed` : 'all passed');
process.exit(fails.length ? 1 : 0);
