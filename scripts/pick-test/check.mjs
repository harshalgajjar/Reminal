// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// <ui-pick> (internal/client/web/pick.js) in a browser: what it shows, how
// it opens, its keys, its events, and that it reads as a select does.
//   node scripts/pick-test/check.mjs      (playwright with chromium)
import { chromium } from 'playwright';
import { readFileSync } from 'node:fs';

const src = readFileSync(new URL('../../internal/client/web/pick.js', import.meta.url), 'utf8');
let failed = 0;
const result = (id, what, ok, why) => { console.log(`${ok ? 'PASS' : 'FAIL'} ${id} ${what}${ok ? '' : ' — ' + (why || '')}`); if (!ok) failed++; };
const b = await chromium.launch();
const ctx = await b.newContext({ viewport: { width: 1000, height: 700 } });
const p = await ctx.newPage();
await p.setContent(`<!doctype html><html><body style="margin:0;padding:20px;background:#0d1117;color:#e6edf3;font:14px system-ui">
<label>Leave <ui-pick class="aw" aria-label="Leave">
  <option value="" data-sub="Requests reach them as usual.">Not on leave</option>
  <option value="1" data-sub="Away for the days below." selected>On leave</option>
  <option value="x" data-sub="Not now." disabled>Gone</option>
</ui-pick></label>
<ui-pick id="to" aria-label="To"><optgroup label="Your lead"><option value="l" data-sub="The seat you report to.">Ops Lead</option></optgroup><optgroup label="Beside you"><option value="d">Dispatch</option><option value="s">Stores</option></optgroup></ui-pick>
<ui-pick id="c" data-compact aria-label="Month"><option value="m1" data-sub="This month so far.">October 2026</option><option value="m2">September 2026</option></ui-pick>
<ui-pick id="off" disabled><option value="a">A</option></ui-pick>
<div id="made"></div>
</body></html>`);
await p.addScriptTag({ content: src });
await p.evaluate(() => { window.__changes = []; document.addEventListener('change', e => window.__changes.push(e.target.className || e.target.id)); });

// What the control shows: the chosen option's label and its meaning.
const shown = await p.evaluate(() => { const el = document.querySelector('.aw'); return { label: el.querySelector('.pk-label').textContent, sub: el.querySelector('.pk-sub').textContent, value: el.value, idx: el.selectedIndex, opts: el.options.length, sel: el.querySelector('option[selected]').value, btn: el.querySelector('button').getAttribute('aria-haspopup') }; });
result('P1', 'the control shows the chosen label and what it means; it reads as a select (value, options, selectedIndex, option[selected])', shown.label === 'On leave' && shown.sub === 'Away for the days below.' && shown.value === '1' && shown.idx === 1 && shown.opts === 3 && shown.sel === '1' && shown.btn === 'listbox', JSON.stringify(shown));

// Opened by a click: a listbox under it, each option with its meaning, the chosen one marked, a disabled one said so.
await p.click('.aw button');
const list = await p.evaluate(() => { const l = document.querySelector('.pk-list'); const r = l.getBoundingClientRect(), br = document.querySelector('.aw button').getBoundingClientRect(); return { role: l.getAttribute('role'), rows: [...l.querySelectorAll('.pk-opt')].map(o => [o.querySelector('.pk-label').textContent, o.querySelector('.pk-sub').textContent, o.getAttribute('aria-selected'), o.getAttribute('aria-disabled')]), under: r.top >= br.bottom, expanded: document.querySelector('.aw button').getAttribute('aria-expanded'), focus: document.activeElement === l }; });
result('P2', 'opened: a listbox under the control, every option with its meaning, the chosen one marked, a disabled one said', list.role === 'listbox' && list.rows.length === 3 && list.rows[0][1] === 'Requests reach them as usual.' && list.rows[1][2] === 'true' && list.rows[2][3] === 'true' && list.under && list.expanded === 'true' && list.focus, JSON.stringify(list));

// Keys: Up moves to the first, Enter picks it: the value, a change event, the keyboard back on the control; the list gone.
await p.keyboard.press('ArrowUp');
await p.keyboard.press('Enter');
const picked = await p.evaluate(() => ({ value: document.querySelector('.aw').value, label: document.querySelector('.aw .pk-label').textContent, changes: window.__changes.slice(), list: !!document.querySelector('.pk-list'), focus: document.activeElement === document.querySelector('.aw button') }));
result('P3', 'Up then Enter picks the first: value, label, a change event, the list closed, the keyboard back on the control', picked.value === '' && picked.label === 'Not on leave' && picked.changes.join() === 'aw' && !picked.list && picked.focus, JSON.stringify(picked));

// Space opens; the arrows skip a disabled option; Escape closes with nothing changed.
await p.keyboard.press(' ');
await p.keyboard.press('ArrowDown'); await p.keyboard.press('ArrowDown');
const skipped = await p.evaluate(() => document.querySelector('.pk-list .pk-active .pk-label').textContent);
await p.keyboard.press('Escape');
const esc = await p.evaluate(() => ({ list: !!document.querySelector('.pk-list'), value: document.querySelector('.aw').value, changes: window.__changes.length }));
result('P4', 'Space opens, the arrows step over a disabled option (back to the first), Escape closes and changes nothing', skipped === 'Not on leave' && !esc.list && esc.value === '' && esc.changes === 1, JSON.stringify({ skipped, esc }));

// Groups: an optgroup is a heading in the list; typed letters find a label; a click outside closes.
await p.click('#to button');
const groups = await p.evaluate(() => [...document.querySelector('.pk-list').children].map(x => (x.classList.contains('pk-head') ? 'head:' : x.classList.contains('pk-opt') ? 'opt:' : x.className + ':') + (x.querySelector('.pk-label') || x).textContent.trim()));
await p.keyboard.type('st');
const typed = await p.evaluate(() => document.querySelector('.pk-list .pk-active .pk-label').textContent);
await p.mouse.click(900, 650);
const outside = await p.evaluate(() => !document.querySelector('.pk-list'));
result('P5', 'an optgroup is a heading over its options; typed letters find Stores; a click outside closes', groups.join('|') === 'pk-title:To|head:Your lead|opt:Ops Lead|head:Beside you|opt:Dispatch|opt:Stores' && typed === 'Stores' && outside, JSON.stringify({ groups, typed, outside }));

// Code sets a value: drawn, no change event; a value no option has leaves it unchosen; new options keep a value they have.
const set = await p.evaluate(() => { const el = document.querySelector('#to'); el.value = 'd'; const a = [el.value, el.querySelector('.pk-label').textContent]; el.value = 'zzz'; const b = [el.value, el.selectedIndex]; el.options = [{ value: 'd', label: 'Dispatch', sub: 'Still here' }, { head: 'New' }, { value: 'n', label: 'New one' }]; el.value = 'd'; const c = [el.value, el.querySelector('.pk-sub').textContent, el.querySelectorAll('optgroup').length, el.options.length]; return { a, b, c, changes: window.__changes.length }; });
result('P6', 'set from code: drawn without a change event; an unknown value leaves it unchosen; options written again keep its value', set.a.join() === 'd,Dispatch' && set.b.join() === ',-1' && set.c.join() === 'd,Still here,1,2' && set.changes === 1, JSON.stringify(set));

// Compact: the control shows the label alone; disabled: it does not open; Pick.html escapes; Pick.make builds.
const compact = await p.evaluate(() => { const c = document.querySelector('#c'); const sub = getComputedStyle(c.querySelector('.pk-sub')).display; document.querySelector('#off button').click(); const off = !document.querySelector('.pk-list'); const h = Pick.html([{ value: 'a"b', label: '<b>x</b>', sub: "it's" }], 'a"b', { class: 'z', 'aria-label': 'Q' }); document.querySelector('#made').innerHTML = h; const m = document.querySelector('#made ui-pick'); const made = Pick.make([{ value: '1', label: 'One', sub: 'first' }, { value: '2', label: 'Two' }], '2', { id: 'mk' }); document.body.appendChild(made); return { sub, off, label: m.querySelector('.pk-label').textContent, value: m.value, bold: !!m.querySelector('b'), made: [made.value, made.querySelector('.pk-label').textContent] }; });
result('P7', 'data-compact hides the meaning on the control; a disabled one does not open; Pick.html escapes its words; Pick.make builds one chosen', compact.sub === 'none' && compact.off && compact.label === '<b>x</b>' && compact.value === 'a"b' && !compact.bold && compact.made.join() === '2,Two', JSON.stringify(compact));

// On a phone: a sheet from the bottom, titled by the control's label, a shade behind it that closes it.
await p.setViewportSize({ width: 412, height: 860 });
await p.click('.aw button');
const sheet = await p.evaluate(() => { const l = document.querySelector('.pk-list'); const r = l.getBoundingClientRect(); return { sheet: l.classList.contains('pk-sheet'), bottom: Math.round(r.bottom), width: Math.round(r.width), title: l.querySelector('.pk-title').textContent, titleShown: getComputedStyle(l.querySelector('.pk-title')).display !== 'none', back: !!document.querySelector('.pk-back') }; });
await p.mouse.click(200, 30);
const closed = await p.evaluate(() => !document.querySelector('.pk-list') && !document.querySelector('.pk-back'));
result('P8', 'on a phone the list is a sheet from the bottom, titled by the control’s label, with a shade that closes it', sheet.sheet && sheet.bottom === 860 && sheet.width === 412 && sheet.title === 'Leave' && sheet.titleShown && sheet.back && closed, JSON.stringify({ sheet, closed }));

await b.close();
console.log(failed ? failed + ' FAILED' : 'all passed');
process.exit(failed ? 1 : 0);
