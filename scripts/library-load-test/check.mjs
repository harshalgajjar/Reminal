// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// The page as served, with the terminal library's request held back, then
// with nothing held back (run.sh).
import { chromium } from 'playwright';
import http from 'node:http';
import { readFileSync } from 'node:fs';
import { join, normalize } from 'node:path';

const TYPES = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.webmanifest': 'application/manifest+json' };
const srv = http.createServer((q, r) => {
  let p = normalize(q.url.split('?')[0]);
  if (p === '/') p = '/index.html';
  let b;
  try { b = readFileSync(join('/t', p)); } catch (_) { r.writeHead(404); return r.end(); }
  r.writeHead(200, { 'content-type': TYPES[(p.match(/\.[a-z]+$/) || [''])[0]] || 'application/octet-stream' });
  r.end(b);
}).listen(8099);

const fails = [];
const check = (ok, what) => { console.log((ok ? 'ok   ' : 'FAIL ') + what); if (!ok) fails.push(what); };
const b = await chromium.launch();

async function load(hold) {
  const ctx = await b.newContext({ viewport: { width: 1280, height: 800 }, serviceWorkers: 'block' });
  const page = await ctx.newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(String(e && e.message || e)));
  let held = 0;
  if (hold) await page.route(/\/xterm(\.min)?\.js(\?|$)/, (route) => { held++; return route.abort('failed'); });
  await page.goto('http://127.0.0.1:8099/', { waitUntil: 'load' });
  await page.waitForTimeout(1500);
  const seen = await page.evaluate(() => {
    const o = document.getElementById('overlay'), t = document.getElementById('overlay-text');
    return { library: typeof Terminal, overlay: !!o && getComputedStyle(o).display !== 'none', text: t ? t.textContent : '' };
  });
  await ctx.close();
  return { errors, held, ...seen };
}

// The library's request fails, as it does when its host cannot be reached.
{
  const r = await load(true);
  check(r.held === 1 && r.library === 'undefined', `the terminal library's request was held back (${r.held} held, Terminal is ${r.library})`);
  check(r.overlay && /did not load completely/.test(r.text), `the page says in words that it did not load (overlay shown: ${r.overlay}, "${r.text}")`);
  const own = r.errors.filter((e) => /the terminal library did not load/.test(e));
  const others = r.errors.filter((e) => !/the terminal library did not load/.test(e));
  check(own.length === 1, `it stops once, saying which part is missing (${own.length} time(s))`);
  check(others.length === 0, `and nothing else fails after it (${others.length ? others.slice(0, 3).join(' | ') : 'no other error'})`);
}

// Nothing held back: the page loads as ever.
{
  const r = await load(false);
  check(r.library === 'function', `with nothing held back the terminal library is there (Terminal is ${r.library})`);
  check(!/did not load completely/.test(r.text), `and the page does not say it failed to load ("${r.text}")`);
  const bad = r.errors.filter((e) => /is not defined|before initialization|did not load/.test(e));
  check(bad.length === 0, `and no script fails for want of another (${bad.length ? bad.slice(0, 3).join(' | ') : 'none'})`);
}

await b.close(); srv.close();
console.log(fails.length ? `${fails.length} failed` : 'all passed');
process.exit(fails.length ? 1 : 0);
