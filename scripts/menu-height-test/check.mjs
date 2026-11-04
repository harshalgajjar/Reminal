// A header menu with more rows than the window has room for ends at the
// bottom of the window and scrolls, so its last row can be reached; one with
// few rows stays as tall as its rows. The Machines search stays in sight at
// the top as its list scrolls (run.sh).
import { chromium } from 'playwright';
import http from 'node:http';
import { readFileSync } from 'node:fs';
const html = readFileSync('/t/index.html');
const srv = http.createServer((q, r) => { const a = /^\/assets\/([\w.-]+\.js)$/.exec(q.url.split('?')[0]); if (a) { let b; try { b = readFileSync('/t/assets/' + a[1]); } catch (_) { r.writeHead(404); return r.end(); } r.writeHead(200, { 'content-type': 'text/javascript' }); return r.end(b); } if (q.url.split('?')[0] === '/reload.js') { r.writeHead(200, { 'content-type': 'text/javascript' }); return r.end(readFileSync('/t/reload.js')); } r.writeHead(200, { 'content-type': 'text/html' }); r.end(html); }).listen(8099);
const SIZES = [
  ['desktop', { viewport: { width: 1440, height: 900 } }],
  ['short', { viewport: { width: 1280, height: 520 } }],
  // A header wrapped onto more rows (more buttons, a narrower window): its
  // menus open lower down, with less room under them.
  ['wrapped', { viewport: { width: 1440, height: 900 } }, 200],
  ['phone', { viewport: { width: 412, height: 800 }, isMobile: true, hasTouch: true }],
];
// Each menu, and where its rows go: the Machines menu's rows sit in a list
// inside it, below its search.
const MENUS = [['recents', '#machines-list'], ['windows', '#windows-list'], ['apps', '#apps-list'], ['host', '#host-list']];
const ROWS = 60;
const b = await chromium.launch();
const fails = [];
for (const [size, opts, headerPad = 0] of SIZES) {
  for (const [menu, rowsSel] of MENUS) {
    for (const rows of [ROWS, 2]) {
      const ctx = await b.newContext(opts);
      const page = await ctx.newPage();
      await page.goto('http://127.0.0.1:8099/');
      await page.evaluate((pad) => {
        // No host here: nothing is asked of one.
        sendWin = () => {};
        document.body.classList.add('connected');
        if (pad) document.querySelector('header').style.paddingTop = pad + 'px';
      }, headerPad);
      // Opened first: a menu fills itself as it opens, which would replace
      // the rows put in before.
      await page.evaluate((menu) => { document.getElementById(menu).open = true; }, menu);
      await page.waitForTimeout(200);
      await page.evaluate(([menu, rowsSel, rows]) => {
        const box = document.querySelector(rowsSel);
        box.textContent = '';
        for (let i = 0; i < rows; i++) {
          const d = document.createElement('div');
          // As machine cards in the Machines menu: its search shows once there
          // is one to search.
          d.className = menu === 'recents' ? 'recents-row machine-box' : 'recents-row';
          d.textContent = 'row ' + i;
          d.id = 'filler-' + i;
          box.appendChild(d);
        }
      }, [menu, rowsSel, rows]);
      await page.waitForTimeout(200);
      if (process.env.SHOTS) await page.screenshot({ path: `${process.env.SHOTS}/${menu}-${size}-${rows}.png` });
      const got = await page.evaluate(([menu, rows]) => {
        const list = document.querySelector('#' + menu + ' > .recents-list');
        const r = list.getBoundingClientRect();
        const last = document.getElementById('filler-' + (rows - 1));
        last.scrollIntoView({ block: 'end' });
        const lr = last.getBoundingClientRect();
        const lst = list.getBoundingClientRect();
        const s = menu === 'recents' ? document.querySelector('#recents .ms-bar').getBoundingClientRect() : null;
        return { vh: innerHeight, top: Math.round(r.top), bottom: Math.round(r.bottom), height: Math.round(r.height),
                 lastBottom: Math.round(lr.bottom), listBottom: Math.round(lst.bottom), scrolls: list.scrollHeight > list.clientHeight + 1,
                 searchShown: !s || (s.top >= lst.top - 1 && s.bottom <= lst.top + 60) };
      }, [menu, rows]);
      const bad = [];
      if (got.bottom > got.vh) bad.push(`runs ${got.bottom - got.vh}px off the bottom`);
      if (got.lastBottom > Math.min(got.vh, got.listBottom) + 1) bad.push('last row out of reach');
      if (!got.searchShown) bad.push('search scrolled out of sight');
      if (rows === 2 && got.scrolls) bad.push('a short list scrolls');
      if (rows === 2 && got.height > 200) bad.push(`a short list is ${got.height}px tall`);
      console.log(size, menu, rows, JSON.stringify(got), bad.length ? 'BAD ' + bad.join(', ') : 'ok');
      if (bad.length) fails.push(`${size} ${menu} ${rows}: ${bad.join(', ')}`);
      await ctx.close();
    }
  }
}
console.log(fails.length ? 'FAIL  ' + fails.join('; ') : 'PASS  every header menu ends in the window and reaches its last row, on desktop, a short window and a phone');
await b.close(); srv.close();
process.exit(fails.length ? 1 : 0);
