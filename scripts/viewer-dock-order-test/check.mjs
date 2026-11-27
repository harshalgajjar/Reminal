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
let count = 0;
const ready = async (p) => { await p.waitForFunction(() => { try { return typeof pinForSession === 'string' && typeof refreshSessdockPool === 'function'; } catch (_) { return false; } }, null, { timeout: 20000 }); await p.evaluate(() => { openSocket = () => {}; }); };
const authed3 = async (p, id, pin) => { await p.evaluate(([id, pin]) => (typeof noteSessionAuthed === 'function' ? noteSessionAuthed : rememberSession)(id, pin), [id, pin]); await pause(); };
const check = async (want, what) => { count++; const got = await order(one); const ok = got === want.join(' '); console.log((ok ? 'ok   ' : 'FAIL ') + what + ': ' + got); if (!ok) fails.push(what); };

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

// A session opened for the first time, in the other tab: new to the history,
// it ranks right after the current one.
const D = 'DDDD4444';
await move(two, D);
await check([C, D, A, B], 'a session opened for the first time (in the other tab) is right after the current one');

// The page loaded again in its tab, on the session it was on: no visit. The
// other tab's, loaded again on D: D does not move.
await move(one, A); // order now A C D B
await check([A, C, D, B], 'moved to A');
await two.reload(); await ready(two);
await two.evaluate((id) => connectToSession(id, '123456'), D); await pause(); await authed(two, D);
await check([A, C, D, B], 'the other tab loaded again on D and connected: nothing moved');
await one.reload(); await ready(one);
await one.evaluate((id) => connectToSession(id, '123456'), A); await pause(); await authed(one, A);
await check([A, C, D, B], 'this tab loaded again on A and connected: nothing moved');

// Recents reads the same history: its rows are in that order too.
const rows = () => one.evaluate(() => { renderRecents(); return [...document.querySelectorAll('#recents-list .recents-row .meta')].map((m) => (m.textContent.match(/[A-Z]{4}\d{4}/) || [''])[0]).join(' '); });
const said = (ok, what, got) => { count++; console.log((ok ? 'ok   ' : 'FAIL ') + what + (got === undefined ? '' : ': ' + got)); if (!ok) fails.push(what); };
{ const got = await rows(); said(got === [A, C, D, B].join(' '), 'Recents rows are in the same order', got); }

// Forget drops the row, from the dock and from Recents.
await one.evaluate((id) => forgetSession(id), D);
await check([A, C, B], 'a session forgotten is gone from the dock');
{ const got = await rows(); said(!got.includes(D), 'and from Recents', got); }
// A session killed is kept out, though another tab's history still names it.
await one.evaluate((id) => { killedSessionIds.add(id); renderRecents(); }, C);
await check([A, B], 'a session killed is gone from the dock');
{ const got = await rows(); said(!got.includes(C), 'and from Recents', got); }

// A session connected to for the first time with a PIN lands in Recents,
// with its PIN.
const fresh = await browser.newContext();
const three = await fresh.newPage();
await three.goto('http://127.0.0.1:8099/'); await ready(three);
await three.evaluate(() => connectToSession('EEEE5555', '424242')); await pause(); await authed3(three, 'EEEE5555', '424242');
{
  const got = await three.evaluate(() => ({ hist: loadHistory().map((e) => e.id + ':' + e.pin).join(' '), rows: [...document.querySelectorAll('#recents-list .recents-row .meta')].map((m) => m.textContent).join(' | ') }));
  said(got.hist === 'EEEE5555:424242' && got.rows.includes('EEEE5555'), 'a first connection with a PIN lands in Recents with its PIN', JSON.stringify(got));
}
// A connection made again with a PIN learned since keeps it, and moves nothing.
await three.evaluate(() => { saveHistory([{ id: 'EEEE5555', pin: '', name: '', lastSeen: 1000 }, { id: 'FFFF6666', pin: '1', name: '', lastSeen: 500 }]); });
await authed3(three, 'EEEE5555', '777777');
{
  const got = await three.evaluate(() => loadHistory().map((e) => e.id + ':' + e.pin + ':' + e.lastSeen).join(' '));
  said(got === 'EEEE5555:777777:1000 FFFF6666:1:500', 'a connection made again keeps a PIN learned then, its time and place as they were', got);
}

// Named sessions are kept however many unnamed ones come after.
{
  const got = await three.evaluate(() => {
    saveHistory([{ id: 'NAMED001', pin: '1', name: 'my server', lastSeen: 1 }]);
    for (let i = 0; i < 14; i++) rememberSession('UNNAMED' + String(i).padStart(2, '0'), '1');
    const h = loadHistory();
    return { named: h.some((e) => e.id === 'NAMED001' && e.name === 'my server'), unnamed: h.filter((e) => !e.name).length, newestFirst: h[0].id };
  });
  said(got.named && got.unnamed === 10 && got.newestFirst === 'UNNAMED13', 'a named session is kept past the ten most recent unnamed ones', JSON.stringify(got));
}

// History written by a page of before (an entry is id, pin, name, lastSeen;
// one may have no lastSeen at all): sorted by it, the ones without last;
// then a move in this page orders as any.
{
  const got = await three.evaluate(async () => {
    sessionStorage.clear();
    saveHistory([{ id: 'OLDD0003', pin: '1', name: '', lastSeen: 3000 }, { id: 'OLDD0001', pin: '1', name: 'one', lastSeen: 1000 }, { id: 'OLDD0000', pin: '1', name: '' }, { id: 'OLDD0002', pin: '1', name: '', lastSeen: 2000 }]);
    sessionId = '';
    await refreshSessdockPool();
    return sessdockPool.map((x) => x.id).join(' ');
  });
  said(got === 'OLDD0003 OLDD0002 OLDD0001 OLDD0000', 'history of a page of before sorts by its lastSeen, an entry without one last', got);
  await three.evaluate(() => connectToSession('OLDD0001', '1')); await pause(); await authed3(three, 'OLDD0001', '1');
  await three.evaluate(() => connectToSession('OLDD0000', '1')); await pause(); await authed3(three, 'OLDD0000', '1');
  const after = await three.evaluate(async () => { await refreshSessdockPool(); return sessdockPool.map((x) => x.id).join(' '); });
  said(after === 'OLDD0000 OLDD0001 OLDD0003 OLDD0002', 'and after two moves: the current one, the one left, then the rest as they were', after);
}
await browser.close();
console.log(fails.length ? `${fails.length} failed of ${count}` : `all ${count} passed`);
process.exit(fails.length ? 1 : 0);
