// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// run.sh: a page attached to a session whose relay restarts while the agent
// is held, so the page is back first and refused as not ready. Asks run.sh
// for the restart (RELAY) and for the agent to go on (AGENT), each answered
// by "go" on stdin.
import { chromium } from 'playwright';
import readline from 'node:readline';
const BASE = 'http://127.0.0.1:18080';
const [S, PIN] = process.argv.slice(2);
const lines = readline.createInterface({ input: process.stdin })[Symbol.asyncIterator]();
const ask = async (what) => { console.log(what); await lines.next(); };
const b = await chromium.launch();
let failed = 0;
const say = (n, what, ok) => { if (!ok) failed++; console.log(`${n}  ${what}   ${ok ? 'PASS' : 'FAIL'}`); };

const ctx = await b.newContext({ viewport: { width: 1000, height: 700 } });
await ctx.addInitScript(() => { try { localStorage.setItem('reminal-tour-v1', 'skipped'); } catch (_) {} });
const p = await ctx.newPage();
const refused = [];
p.on('websocket', (ws) => ws.on('framereceived', (f) => {
  if (typeof f.payload === 'string' && f.payload.includes('not found or not ready')) refused.push(Date.now());
}));
await p.goto(`${BASE}/?s=${S}#p=${PIN}`, { waitUntil: 'domcontentloaded' });
const live = (ms) => p.waitForFunction(() => inputLive(), null, { timeout: ms }).then(() => true, () => false);
if (!(await live(60000))) {
  console.log('0  the page has the session live before the relay restarts   FAIL');
  await b.close();
  process.exit(1);
}
const shows = (re, ms) => p.waitForFunction((src) => {
  const t = term.buffer.active;
  for (let i = 0; i < t.length; i++) if (new RegExp(src).test(t.getLine(i).translateToString(true))) return true;
  return false;
}, re.source, { timeout: ms }).then(() => true, () => false);

// 1: the relay restarts with the agent held; the page is back first.
await ask('RELAY');
const until = Date.now() + 20000;
while (Date.now() < until && refused.length === 0) await new Promise((r) => setTimeout(r, 200));
const wasRefused = refused.length > 0;
console.log(`   the page was back first and refused as not ready: ${wasRefused}`);
await new Promise((r) => setTimeout(r, 3000)); // held a while past the refusal
say(1, '   while it waits, the page says so', await p.evaluate(() => document.body.innerText.includes('Waiting for the session to come back')));
await ask('AGENT');
const back = await live(60000);
say(1, 'the page is live again once the agent is back', wasRefused && back);
if (back) {
  await p.locator('.xterm-helper-textarea').focus();
  await p.keyboard.type('echo after-$((40+2))', { delay: 15 });
  await p.keyboard.press('Enter');
  say(1, '   and what is typed then echoes', await shows(/^after-42$/, 30000));
} else {
  console.log('     overlay: ' + (await p.evaluate(() => document.querySelector('#overlay, .overlay')?.textContent?.trim() || '(none)')));
}

// 3: a wrong PIN for that live session is not waited out: it ends at once.
const w = await ctx.newPage();
const t1 = Date.now();
await w.goto(`${BASE}/?s=${S}#p=00000000`, { waitUntil: 'domcontentloaded' });
const wrong = await w.waitForFunction(() => fatal === true, null, { timeout: 20000 }).then(() => true, () => false);
say(3, `a wrong PIN still ends at once (${((Date.now() - t1) / 1000).toFixed(1)}s)`, wrong);

// 2: a session no relay has heard of: said at once, not waited for.
const q = await ctx.newPage();
const t0 = Date.now();
await q.goto(`${BASE}/?s=ZZZZ9999#p=12345678`, { waitUntil: 'domcontentloaded' });
const ended = await q.waitForFunction(() => fatal === true, null, { timeout: 15000 }).then(() => true, () => false);
say(2, `a session never seen is said not found at once (${((Date.now() - t0) / 1000).toFixed(1)}s)`, ended);

await b.close();
process.exit(failed ? 1 : 0);
