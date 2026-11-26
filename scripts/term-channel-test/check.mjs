// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// See run.sh. URL is the session's join link through the delaying proxy.
import { chromium } from 'playwright';
const { URL: url } = process.env;
const DELAY = Number(process.env.DELAY || 150), TRIP = 2 * DELAY;
const fails = [];
const check = (ok, what) => { console.log((ok ? 'ok   ' : 'FAIL ') + what); if (!ok) fails.push(what); };
const browser = await chromium.launch({ args: ['--disable-background-timer-throttling', '--disable-renderer-backgrounding'] });

// One viewer: N keys timed from the terminal's input to its next output,
// then a long known line typed with no pause and the screen read back.
async function viewer(direct) {
  const ctx = await browser.newContext({ viewport: { width: 1000, height: 900 } });
  if (!direct) await ctx.addInitScript(() => { try { delete window.RTCPeerConnection; } catch (_) {} window.RTCPeerConnection = undefined; window.webkitRTCPeerConnection = undefined; });
  const page = await ctx.newPage();
  await page.goto(url);
  await page.waitForFunction(() => window.__term && (() => { const b = window.__term.buffer.active; for (let i = 0; i < b.length; i++) if (b.getLine(i).translateToString(true).trim()) return true; return false; })(), null, { timeout: 30000 });
  await page.getByText('Skip', { exact: true }).click({ timeout: 2000 }).catch(() => {});
  if (direct) await page.waitForFunction(() => { try { return rtcTerm && rtcTerm.readyState === 'open'; } catch (_) { return false; } }, null, { timeout: 20000 }).catch(() => {});
  await page.waitForTimeout(2500);
  const open = await page.evaluate(() => { try { return !!(rtcTerm && rtcTerm.readyState === 'open'); } catch (_) { return false; } });
  const ms = await page.evaluate(async () => {
    const t = window.__term, out = [];
    for (let i = 0; i < 40; i++) {
      const got = new Promise((res) => { const d = t.onWriteParsed(() => { d.dispose(); res(performance.now()); }); setTimeout(() => { d.dispose(); res(null); }, 8000); });
      const t0 = performance.now();
      t.input('x', true);
      const t1 = await got;
      out.push(t1 == null ? null : t1 - t0);
      await new Promise((r) => setTimeout(r, 200));
    }
    return out;
  });
  const got = ms.filter((x) => x != null).sort((a, b) => a - b);
  const p50 = got.length ? got[Math.floor(got.length / 2)] : Infinity;
  const rid = 'R' + Math.random().toString(36).slice(2, 8).toUpperCase() + 'Z';
  const want = rid + Array.from({ length: 400 }, (_, i) => 'abcdefghijklmnopqrstuvwxyz0123456789'[(i * 7 + (i >> 3)) % 36]).join('');
  await page.evaluate(async (w) => {
    const t = window.__term;
    t.input('\r', true);
    await new Promise((r) => setTimeout(r, 1500));
    for (const c of 'echo ') t.input(c, true);
    for (let i = 0; i < w.length; i++) { t.input(w[i], true); if (i % 40 === 39) await new Promise((r) => setTimeout(r, 3)); }
  }, want);
  await page.waitForTimeout(2000 + 4 * TRIP);
  const screen = await page.evaluate(() => { const b = window.__term.buffer.active; let s = ''; for (let i = 0; i < b.length; i++) { const l = b.getLine(i); s += (l.isWrapped ? '' : '\n') + l.translateToString(true); } return s; });
  await ctx.close();
  return { open, answered: got.length, p50, once: screen.split(want).length - 1 };
}

const relay = await viewer(false);
check(!relay.open && relay.answered === 40 && relay.p50 >= TRIP * 0.9, `the control, relay only: an echo takes the round trip to the relay (median ${relay.p50.toFixed(0)} ms of ${TRIP} ms, ${relay.answered}/40 keys)`);
check(relay.once === 1, `relay only: a long line typed fast is on the screen once, in order (${relay.once} time(s))`);
const direct = await viewer(true);
check(direct.open, 'with WebRTC: the term channel opened');
check(direct.answered === 40 && direct.p50 < TRIP / 2, `with the channel: an echo comes in well under the relay's round trip (median ${direct.p50.toFixed(0)} ms of ${TRIP} ms, ${direct.answered}/40 keys)`);
check(direct.once === 1, `with the channel: a long line typed fast is on the screen once, in order, its relay copies dropped (${direct.once} time(s))`);
await browser.close();
console.log(fails.length ? `${fails.length} failed` : 'all passed');
process.exit(fails.length ? 1 : 0);
