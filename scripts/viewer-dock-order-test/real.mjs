// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// The session dock's order on the real page (run.sh): this tree's reminal as
// relay with three sessions on it, and one browser with a phone's screen and
// two tabs. Tab one moves between sessions by the dock's own gesture (the ≡
// button dragged up onto a session, and let go) and by a Recents row; tab two
// is left on the third session and has its connection dropped and made again
// between the moves; the screen goes off and on; the pages are loaded again
// part way. The order is read from the dock as it is drawn.
import { chromium } from 'playwright';
const idOf = (u) => new URL(u).searchParams.get('s');
const J = { A: process.env.JA, B: process.env.JB, C: process.env.JC };
const A = idOf(J.A), B = idOf(J.B), C = idOf(J.C);
const name = { [A]: 'A', [B]: 'B', [C]: 'C' };
const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true });
const fails = [];
let count = 0;
const said = (ok, what, got) => { count++; console.log((ok ? 'ok   ' : 'FAIL ') + what + ': ' + got); if (!ok) fails.push(what); };
const pause = (ms) => new Promise((r) => setTimeout(r, ms));

// The page is connected to id, live.
const live = (p, id) => p.waitForFunction((id) => { try { return sessionId === id && inputLive(); } catch (_) { return false; } }, id, { timeout: 30000 });
const open = async (p, url) => { await p.goto(url); await p.getByText('Skip', { exact: true }).click({ timeout: 3000 }).catch(() => {}); await live(p, idOf(url)); };
// Its connection is dropped (as a network that went away, or a phone that
// slept, drops it) and the page makes it again by itself.
const reconnect = async (p, id) => {
  await p.evaluate(() => { window.__was = ws; ws.close(); });
  await p.waitForFunction((id) => { try { return ws && ws !== window.__was && sessionId === id && inputLive(); } catch (_) { return false; } }, id, { timeout: 30000 });
};
// The screen goes off and on: the page is hidden, its connection goes, it is
// shown again and connects again.
const screenOffOn = async (p, id) => {
  const see = (hidden) => p.evaluate((hidden) => {
    Object.defineProperty(document, 'hidden', { configurable: true, get: () => hidden });
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => (hidden ? 'hidden' : 'visible') });
    document.dispatchEvent(new Event('visibilitychange'));
  }, hidden);
  await see(true);
  await p.evaluate(() => { window.__was = ws; try { ws.close(); } catch (_) {} });
  await pause(400);
  await see(false);
  await p.waitForFunction((id) => { try { return ws && ws !== window.__was && sessionId === id && inputLive(); } catch (_) { return false; } }, id, { timeout: 30000 });
};
// The dock's gesture: the ≡ button pressed and dragged up; the dock opens.
// Its rows are read as drawn. Let go on a session's row (to), or away from
// every row (no move).
const dock = async (p, to) => {
  const h = await p.locator('#keybar-toggle').boundingBox();
  const x = h.x + h.width / 2, y = h.y + h.height / 2;
  await p.mouse.move(x, y);
  await p.mouse.down();
  await pause(250); // the dock's list is brought up to date while the finger travels
  await p.mouse.move(x, y - 30, { steps: 4 });
  await p.waitForSelector('#sessdock.open', { timeout: 5000 });
  const rows = await p.evaluate(() => [...document.querySelectorAll('#sessdock .sessdock-item')].map((el) => { const r = el.getBoundingClientRect(); return { id: el._entry.id, x: r.left + r.width / 2, y: r.top + r.height / 2 }; }));
  const target = to ? rows.find((r) => r.id === to) : null;
  if (target) await p.mouse.move(target.x, target.y, { steps: 6 });
  else await p.mouse.move(3, 3, { steps: 6 });
  await pause(60);
  await p.mouse.up();
  if (target) await live(p, to);
  return rows.map((r) => name[r.id] || r.id).join(' ');
};
// A Recents row, tapped.
const recents = async (p, to) => {
  await p.evaluate(() => { document.getElementById('recents').open = true; renderRecents(); });
  await p.evaluate((to) => { const row = [...document.querySelectorAll('#recents-list .recents-row')].find((r) => r.textContent.includes(to)); if (!row) throw new Error('no Recents row for ' + to); row.click(); }, to);
  await live(p, to);
};
const shows = async (p, want, what) => { const got = await dock(p, null); said(got === want, what, got); };

const one = await ctx.newPage(), two = await ctx.newPage();
await open(two, J.B);
await open(two, J.C); // tab two is left on C
await open(one, J.A);
await shows(one, 'A C B', 'tab one opened on A (C was opened after B, in tab two)');

said((await dock(one, B)) === 'A C B', 'the dock, dragged up, then let go on B', 'moved to B');
await shows(one, 'B A C', 'after the dock gesture to B: B, then A just left, then C');
await reconnect(two, C);
await shows(one, 'B A C', 'tab two, on C, connected again: C did not move');

await recents(one, A);
await shows(one, 'A B C', 'after a Recents row to A: A, then B just left, then C');
await screenOffOn(two, C);
await shows(one, 'A B C', 'tab two, on C: screen off and on, connected again: C did not move');
await screenOffOn(one, A);
await shows(one, 'A B C', 'tab one: screen off and on, connected to A again: nothing moved');

await dock(one, B);
await shows(one, 'B A C', 'the dock gesture to B again: B, A, C');
await reconnect(two, C);
await reconnect(one, B);
await shows(one, 'B A C', 'both tabs connected again: nothing moved');

await one.reload(); await one.getByText('Skip', { exact: true }).click({ timeout: 1500 }).catch(() => {}); await live(one, B);
await shows(one, 'B A C', 'tab one loaded again, on B: the order is as it was');
await reconnect(two, C);
await shows(one, 'B A C', 'and tab two connected again after that: C did not move');

await recents(one, A);
await shows(one, 'A B C', 'a Recents row to A after the reload: A, B, C');
await two.reload(); await two.getByText('Skip', { exact: true }).click({ timeout: 1500 }).catch(() => {}); await live(two, C);
await shows(one, 'A B C', 'tab two loaded again, on C: C did not move');

// The person does go to C, in tab one: then it ranks.
await dock(one, C);
await shows(one, 'C A B', 'the dock gesture to C in tab one: C first, A just left second');
// And the history holds each session's PIN still (a first PIN connection
// lands in Recents, and none of the above lost one).
const pins = await one.evaluate(() => loadHistory().map((e) => (e.pin ? 'pin' : 'none')).join(' '));
said(pins === 'pin pin pin', 'the three sessions are in the history with their PINs', pins);
await browser.close();
console.log(fails.length ? `${fails.length} failed of ${count}` : `all ${count} passed`);
process.exit(fails.length ? 1 : 0);
