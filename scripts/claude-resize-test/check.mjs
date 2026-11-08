// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// The viewer (the real page) on a Claude Code session: it asks for the
// story, and while the story streams its grid goes through a phone's
// changes (run.sh). At the end, what its terminal holds is read and judged.
// The page keeps its xterm in a closure and exposes it to tests as
// window.__term (app.js).
import { chromium } from 'playwright';
import * as fs from 'node:fs';
import { SECTIONS, SENTENCES, STORIES, storyHeading } from './model.mjs';

const [ID, PIN] = process.argv.slice(2);
// The sentences of both stories (S…, then T…), and each story's last.
const ids = [];
for (const led of STORIES) for (let s = 1; s <= SECTIONS; s++) for (let k = 1; k <= SENTENCES; k++) ids.push(`${led}${s}x${String(k).padStart(2, '0')}`);

// headingsOf counts the rows that are one of a story's headings.
const headingsOf = (rows, part) => rows.filter((r) => new RegExp(`Section \\d of the ${part ? 'second ' : ''}story`).test(r)).length;

const b = await chromium.launch();
const ctx = await b.newContext({ viewport: { width: 1200, height: 900 }, deviceScaleFactor: 1 });
await ctx.addInitScript(() => { try { localStorage.setItem('reminal-tour-v1', 'skipped'); } catch (_) {} });
// A box with no network: the page's CDN files, fetched at image build and
// checked against the page's SRI hashes, are served from CDN_DIR
// (<dir>/<host>/<path>); any other external request is refused.
const CDN_DIR = process.env.CDN_DIR || '/opt/pw/cdn';
if (fs.existsSync(CDN_DIR)) {
  await ctx.route(/^https?:\/\//, async (route) => {
    const u = new URL(route.request().url());
    if (u.hostname === '127.0.0.1' || u.hostname === 'localhost') return route.continue();
    const p = `${CDN_DIR}/${u.hostname}${u.pathname}`;
    if (!fs.existsSync(p)) { console.log(`  (external request refused: ${u.href})`); return route.abort(); }
    return route.fulfill({ status: 200, body: fs.readFileSync(p), headers: { 'content-type': p.endsWith('.css') ? 'text/css' : 'application/javascript', 'access-control-allow-origin': '*' } });
  });
}
const page = await ctx.newPage();
const url = `http://127.0.0.1:18080/?s=${ID}#p=${PIN}`;

const ready = async () => {
  await page.waitForFunction(() => typeof window.__term === 'object' && window.__term && document.querySelector('.xterm-helper-textarea'), null, { timeout: 60000 });
};
const grid = () => page.evaluate(() => [window.__term.cols, window.__term.rows]);
// size makes the viewer's grid cols x rows: the page fits its terminal to
// the window, so the window is set from the terminal's cell size and the
// room the page takes around it, then corrected until it is right. The
// page shows min(its own room, the PTY), so a grid wider than the PTY
// cannot be had while another size holds the PTY: what it is is printed.
const size = async (cols, rows) => {
  for (let i = 0; i < 8; i++) {
    const [c, r] = await grid();
    if (c === cols && r === rows) return;
    const vp = page.viewportSize();
    const { cw, ch } = await page.evaluate(() => {
      const d = window.__term._core._renderService.dimensions;
      return { cw: d.css.cell.width, ch: d.css.cell.height };
    });
    await page.setViewportSize({ width: Math.max(300, Math.round(vp.width + (cols - c) * cw)), height: Math.max(200, Math.round(vp.height + (rows - r) * ch)) });
    await page.waitForTimeout(500);
  }
  const [c, r] = await grid();
  console.log(`  (grid is ${c}x${r}, asked ${cols}x${rows})`);
};
const rowsNow = () => page.evaluate(() => {
  const buf = window.__term.buffer.active, out = [];
  for (let i = 0; i < buf.length; i++) out.push(buf.getLine(i)?.translateToString(true) ?? '');
  return out;
});
const sees = async (re) => (await rowsNow()).some((r) => re.test(r));
const waitFor = async (re, ms) => {
  for (let t = 0; t < ms; t += 250) { if (await sees(re)) return true; await page.waitForTimeout(250); }
  return false;
};
const fail = async (why) => {
  console.log(`FAIL: ${why}`);
  if (process.env.DUMP) fs.writeFileSync(process.env.DUMP, (await rowsNow()).join('\n'));
  await b.close();
  process.exit(1);
};

await page.goto(url, { waitUntil: 'domcontentloaded' });
await ready();
await size(102, 45);
if (!(await waitFor(/❯/, 60000))) await fail('no Claude prompt');
await page.waitForTimeout(1500);
// Never type into a first-run or dialog screen (the prompt's ❯ is in
// menus too): if one is showing, stop.
{
  const scr = (await rowsNow()).join('\n');
  for (const bad of ['Choose the text style', 'Select login', 'Log in', 'Enter to confirm', 'trust this folder', 'Try the new', 'Unable to connect', 'API key']) {
    if (scr.includes(bad)) await fail(`a dialog is showing ("${bad}"), not typing`);
  }
}
await page.locator('.xterm-helper-textarea').focus();
await page.keyboard.type('share a long story with headings', { delay: 20 });
await page.keyboard.press('Enter');
if (!(await waitFor(/S1x01/, 60000))) await fail('the story did not start');

// The phone's changes while the story streams: the keyboard ten times;
// then a reconnect on the wider inner screen (the join path, on a grid
// wider than the history was laid out for); then a reconnect on the
// narrow outer screen (the join on a grid NARROWER than the history: what
// clipped on the old emulator); then landscape again.
const step = async (what, cols, rows, ms) => { console.log(`  ${what} -> ${cols}x${rows}`); await size(cols, rows); await page.waitForTimeout(ms); };
const reconnect = async (what, w, h, cols, rows, ms) => {
  console.log(`  ${what}: reconnect on a ${cols}x${rows} grid`);
  await page.setViewportSize({ width: w, height: h });
  await page.reload({ waitUntil: 'domcontentloaded' });
  await ready();
  await size(cols, rows);
  await page.waitForTimeout(ms);
};
// KEYBOARD=0 leaves the keyboard out: a height shrink loses rows to Claude
// Code's own redraw in any terminal, so that variant is the one judged on
// sentences lost; the full one is judged on layout.
const keyboard = process.env.KEYBOARD !== "0";
for (let i = 0; keyboard && i < 10; i++) {
  await step(`keyboard open ${i + 1}`, 102, 24, 700);
  await step(`keyboard closed ${i + 1}`, 102, 45, 700);
}
// A grid change inside the settle window (the PTY still at the old size
// for 200 ms, the viewer's grid already at the new, Claude placing words
// with CHA for the old width), wider and narrower, while the story streams.
for (let i = 0; i < 3; i++) {
  await step('flurry: narrower', 80, 45, 120);
  await step('flurry: back', 102, 45, 120);
  await step('flurry: wider', 120, 45, 120);
  await step('flurry: back', 102, 45, 120);
  // A height shrink loses rows to Claude's own redraw: with the keyboard.
  if (keyboard) {
    await step('flurry: shorter', 102, 30, 120);
    await step('flurry: back', 102, 45, 120);
  }
  await page.waitForTimeout(800);
}
// A second story, so the reconnects below meet a Claude with frames in
// flight, not an idle one that only redraws on SIGWINCH (the first story
// is done by now: the size helper's corrections stretch the run).
await page.locator('.xterm-helper-textarea').focus();
await page.keyboard.type('share a long story with headings', { delay: 20 });
await page.keyboard.press('Enter');
await page.waitForTimeout(1500);
// A grid change in the middle of a paced snapshot: the reconnect's parts
// still arriving when the grid shrinks (after the wide reconnect) and when
// it grows (after the narrow one).
await reconnect('inner screen', 1500, 900, 120, 49, 150);
await step('mid-snapshot: narrower', 90, 40, 2500);
await reconnect('outer screen, portrait', 700, 1100, 55, 58, 150);
await step('mid-snapshot: wider', 102, 49, 2500);
await step('landscape again', 102, 49, 2000);

// The story's end, then a quiet terminal.
await waitFor(new RegExp(ids[ids.length - 1]), 120000);
let prev = -1;
for (let still = 0; still < 8;) {
  await page.waitForTimeout(500);
  const n = (await rowsNow()).length;
  still = n === prev ? still + 1 : 0;
  prev = n;
}

// What the viewer holds, judged at the grid it holds it in.
const rows = await rowsNow();
const [cols, trows] = await grid();
const text = rows.join('\n');
// An id can wrap at the grid's edge ("S" ending one row, "1x10" opening the
// next): present when the rows read on into each other too.
const flat = rows.join('');
const lost = ids.filter((id) => !text.includes(id) && !flat.includes(id));
const seen = new Map();
for (const r of rows) for (const m of r.matchAll(/S\d+x\d\d/g)) seen.set(m[0], (seen.get(m[0]) || 0) + 1);
const doubled = [...seen].filter(([, n]) => n > 1).map(([id]) => id);
// A staircase: three or more rows in a row holding one or two words each,
// the first word further right each time.
let stair = 0, run = 0, lastCol = -1;
for (const r of rows) {
  const m = r.match(/^( *)(\S+)( +\S+)? *$/);
  const col = m ? m[1].length : -1;
  if (m && col > lastCol && col > 0) { run++; if (run >= 3) stair++; } else run = 0;
  lastCol = col;
}
const headings = [headingsOf(rows, 0), headingsOf(rows, 1)];
const rules = rows.filter((r) => /^─{10,}$/.test(r.trim()));
const splitRules = rules.filter((r) => r.trim().length < cols - 2).length;
const status = rows.filter((r) => /auto mode on|esc to interrupt/.test(r)).length;
const wide = rows.filter((r) => r.length > cols).length;
console.log(`grid at the end: ${cols}x${trows}, ${rows.length} rows held`);
console.log(`sentences: ${ids.length - lost.length}/${ids.length} present${lost.length ? ' (lost: ' + lost.join(' ') + ')' : ''}`);
console.log(`doubled sentences: ${doubled.length}${doubled.length ? ' (' + doubled.slice(0, 12).join(' ') + (doubled.length > 12 ? ' …' : '') + ')' : ''}; section headings ${headings[0]} and ${headings[1]} for ${SECTIONS} sections each`);
console.log(`layout: staircase rows ${stair}, rows wider than ${cols} cols ${wide}, rule rows ${rules.length} (${splitRules} short), status rows ${status}`);
// Someone reading the history when the width changes: scrolled up, the grid
// narrows, Claude Code redraws, and once it is quiet the agent repaints this
// viewer (a re-snapshot). The reader must still be up in the history, not
// jumped to the bottom. Only the width changes: a height shrink scrolls to
// the bottom on purpose (a phone's keyboard opened to type). READ_LOOPS
// times (default 1), narrowing and widening in turn.
const upBy = () => page.evaluate(() => { const b = window.__term.buffer.active; return b.baseY - b.viewportY; });
const widthTo = async (want) => {
  for (let i = 0; i < 8; i++) {
    const [c, r] = await grid();
    if (c === want) return r;
    const vp = page.viewportSize();
    const cw = await page.evaluate(() => window.__term._core._renderService.dimensions.css.cell.width);
    await page.setViewportSize({ width: Math.max(300, Math.round(vp.width + (want - c) * cw)), height: vp.height });
    await page.waitForTimeout(500);
  }
  return (await grid())[1];
};
const record = () => page.evaluate(() => { try { return window.__size().events.slice(-24).map((e) => e.kind + ' ' + e.d).join(' | '); } catch (_) { return ''; } });
const loops = Number(process.env.READ_LOOPS || 1);
let keptPlace = true, readFails = 0;
for (let i = 0; i < loops; i++) {
  await page.evaluate(() => window.__term.scrollLines(-40));
  const upBefore = await upBy();
  const rowsBefore = (await grid())[1];
  const target = i % 2 === 0 ? cols - 12 : cols;
  const rowsAfter = await widthTo(target);
  await page.waitForTimeout(4000);
  const upAfter = await upBy();
  const ok = upBefore > 0 && upAfter > 0 && rowsAfter === rowsBefore;
  if (!ok) { keptPlace = false; readFails++; console.log(`reading the history, loop ${i + 1}: ${upBefore} up before, ${upAfter} after, rows ${rowsBefore} -> ${rowsAfter}\n  the viewer's record: ${await record()}`); }
  else if (loops === 1 || i === loops - 1) console.log(`reading the history through a repaint: ${upBefore} rows up before, ${upAfter} after (${loops} loop${loops > 1 ? 's' : ''}, ${readFails} lost the place)`);
}
await widthTo(cols);
await page.waitForTimeout(3000);

// A fresh join at the end, for comparison: its history is the agent's,
// deduped, so a count above the attached viewer's own says a copy is the
// history's, below it that the attached viewer kept one the history shed.
await page.reload({ waitUntil: 'domcontentloaded' });
await ready();
await size(cols, trows);
await waitFor(new RegExp(ids[ids.length - 1]), 30000);
await page.waitForTimeout(3000);
const fresh = await rowsNow();
const freshHeadings = [headingsOf(fresh, 0), headingsOf(fresh, 1)];
console.log(`a fresh join at the end: ${fresh.length} rows, section headings ${freshHeadings[0]} and ${freshHeadings[1]}`);
if (process.env.DUMP) fs.writeFileSync(process.env.DUMP.replace(/(\.txt)?$/, '-fresh$1'), fresh.join('\n'));
// Without the keyboard, every width change re-snapshots the viewer from the
// deduped history once the program is quiet. The second story streams
// through the fold's changes (one width change, then a pause: a reconnect,
// a grid change): its headings appear once each, on the attached viewer and
// on a fresh join alike. The first streams through the flurry (a width
// change every 120 ms): what the program prints for one width lands on the
// next, in any terminal, and its headings are counted but not judged. With
// the keyboard, Claude's own redraws count, and the run is judged on layout.
const once = headings[1] === SECTIONS && freshHeadings[1] === SECTIONS;
const ok = (keyboard || (lost.length === 0 && once)) && stair === 0 && wide === 0 && splitRules === 0 && keptPlace;
console.log(ok ? 'PASS' : 'FAIL');
if (process.env.DUMP) fs.writeFileSync(process.env.DUMP, rows.join('\n'));
await b.close();
process.exit(ok ? 0 : 1);
