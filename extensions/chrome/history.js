// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// The whole log (history.html, opened from the button's panel): every line
// the extension keeps of what agents did here that the person should be able
// to see afterwards, newest first, with a search, filters by session, by
// kind of action and by time, and clearing as a browser's own history has
// it (a range of time, or one entry). Read straight from the browser's
// storage in one read and drawn in one pass, so it shows with the
// extension's worker asleep; filtering works on what was read. Clearing is
// asked of the worker, the log's one writer, and takes the entries out of
// storage; this page and the button's panel then draw what is left.
const $ = (id) => document.getElementById(id);

// The rule the worker keeps the log by (NOTES and NOTES_DAYS in
// background.js): said here in words.
const KEPT = 5000, KEPT_DAYS = 30;
$('rule').textContent = `reminal keeps this for ${KEPT_DAYS} days, the newest ${KEPT.toLocaleString()} entries at most, in this browser only. You can clear it here.`;

// The kinds of action that are logged, as the filter names them. An entry
// written before entries had a kind is told by its words.
const KINDS = { visit: 'Pages opened', script: 'Scripts run in pages', upload: 'Files given to pages', cookies: 'Cookies', storage: 'Site storage' };
const kindOf = (n) => {
  if (n.kind in KINDS) return n.kind;
  const t = String(n.text || '');
  if (/^opened a page/.test(t)) return 'visit';
  if (/^ran a script/.test(t)) return 'script';
  if (/^gave /.test(t)) return 'upload';
  if (/cookie/.test(t)) return 'cookies';
  if (/ storage/.test(t)) return 'storage';
  return '';
};
// A session is called by its id, as its tab group is: "<ID> - reminal".
const idOf = (n) => (/^([A-Z0-9]{4,32}) - reminal$/.exec(n.who || '') || [])[1] || '';

let entries = []; // newest first: {n (as stored), at, who, text, url, kind, id, time, hay}
const fmt = new Intl.DateTimeFormat([], { dateStyle: 'medium', timeStyle: 'short' });

function choices(select, first, pairs) {
  const was = select.value;
  const opts = [['', first], ...pairs].map(([value, label]) => { const o = document.createElement('option'); o.value = value; o.textContent = label; return o; });
  select.replaceChildren(...opts);
  if (opts.some((o) => o.value === was)) select.value = was;
}

function draw() {
  const q = $('q').value.trim().toLowerCase(), id = $('session').value, kind = $('kind').value, when = $('when').value;
  const midnight = new Date(); midnight.setHours(0, 0, 0, 0);
  const since = when === 'today' ? midnight.getTime() : when === 'week' ? Date.now() - 7 * 86_400_000 : 0;
  const rows = [];
  for (const e of entries) {
    if ((id && e.id !== id) || (kind && e.kind !== kind) || e.at < since || (q && !e.hay.includes(q))) continue;
    const tr = document.createElement('tr');
    for (const text of [e.time, e.who, e.text]) { const td = document.createElement('td'); td.textContent = text; tr.append(td); }
    // A page opened: its address under the line, as text (never a link: the
    // page's own address is not something to click through to from here).
    if (e.url) { const a = document.createElement('div'); a.className = 'dim'; a.textContent = e.url; tr.lastChild.append(a); }
    const td = document.createElement('td');
    const b = document.createElement('button');
    b.className = 'small';
    b.textContent = 'Remove';
    b.addEventListener('click', () => { b.disabled = true; chrome.runtime.sendMessage({ t: 'remove', at: e.n.at, who: e.n.who, text: e.n.text }).catch(() => {}); });
    td.append(b);
    tr.append(td);
    rows.push(tr);
  }
  $('rows').replaceChildren(...rows);
  $('count').textContent = !entries.length ? 'Nothing is kept.'
    : !rows.length ? `Nothing matches, of ${entries.length} kept.`
    : rows.length === entries.length ? `${entries.length} ${entries.length === 1 ? 'entry' : 'entries'}, newest first.`
    : `${rows.length} of ${entries.length} entries, newest first.`;
}

async function load() {
  const { notes = [] } = await chrome.storage.local.get('notes');
  entries = notes.map((n) => {
    const who = String(n.who || 'reminal'), text = String(n.text || ''), url = String(n.url || ''), time = fmt.format(new Date(n.at));
    return { n, at: n.at, who, text, url, kind: kindOf(n), id: idOf(n), time, hay: `${who} ${text} ${url} ${time}`.toLowerCase() };
  }).reverse();
  // Only the sessions and the kinds that are in the log are offered.
  choices($('session'), 'All sessions', [...new Set(entries.map((e) => e.id).filter(Boolean))].sort().map((id) => [id, `${id} - reminal`]));
  choices($('kind'), 'All kinds of action', Object.entries(KINDS).filter(([k]) => entries.some((e) => e.kind === k)));
  draw();
}

// Clearing a range: asked once, here in the page, with how many entries it
// is; then they are taken out of storage.
const RANGES = { hour: ['the last hour', 3_600_000], day: ['the last day', 86_400_000], week: ['the last 7 days', 7 * 86_400_000], all: ['', 0] };
let clearFrom = null;
const unask = () => { clearFrom = null; $('sure').hidden = true; };
$('clear').addEventListener('click', () => {
  const [words, ms] = RANGES[$('range').value];
  const since = ms ? Date.now() - ms : 0;
  const count = entries.filter((e) => e.at >= since).length;
  $('cleared').textContent = '';
  if (!count) { unask(); $('cleared').textContent = words ? `Nothing is kept from ${words}.` : 'Nothing is kept.'; return; }
  clearFrom = since;
  $('ask').textContent = words ? `Remove ${count === 1 ? 'the 1 entry' : `the ${count} entries`} from ${words}? They cannot be brought back.` : `Remove all ${count === 1 ? '1 entry' : `${count} entries`}? They cannot be brought back.`;
  $('sure').hidden = false;
});
$('range').addEventListener('input', unask);
$('no').addEventListener('click', unask);
$('yes').addEventListener('click', async () => {
  const since = clearFrom;
  unask();
  if (since == null) return;
  await chrome.runtime.sendMessage({ t: 'clear', since }).catch(() => {});
  $('cleared').textContent = 'Cleared.';
});

for (const id of ['q', 'session', 'kind', 'when']) $(id).addEventListener('input', draw);
chrome.storage.onChanged.addListener((changes, area) => { if (area === 'local' && 'notes' in changes) load(); });
load();
