// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// The whole log (history.html, opened from the button's panel): every line
// the extension keeps of what agents did here that the person should be able
// to see afterwards, newest first, with a search and filters by session, by
// kind of action and by time. Read straight from the browser's storage in
// one read and drawn in one pass, so it does not need the extension's worker
// to be awake; filtering works on what was read.
const $ = (id) => document.getElementById(id);

// The rule the worker keeps the log by (NOTES and NOTES_DAYS in
// background.js): said here in words.
const KEPT = 500, KEPT_DAYS = 30;
$('rule').textContent = `reminal keeps the last ${KEPT} entries, and none older than ${KEPT_DAYS} days, in this browser only.`;

// The kinds of action that are logged, as the filter names them. An entry
// written before entries had a kind is told by its words.
const KINDS = { script: 'Scripts run in pages', upload: 'Files given to pages', cookies: 'Cookies', storage: 'Site storage' };
const kindOf = (n) => {
  if (n.kind in KINDS) return n.kind;
  const t = String(n.text || '');
  if (/^ran a script/.test(t)) return 'script';
  if (/^gave /.test(t)) return 'upload';
  if (/cookie/.test(t)) return 'cookies';
  if (/ storage/.test(t)) return 'storage';
  return '';
};
// A session is called by its id, as its tab group is: "<ID> - reminal".
const idOf = (n) => (/^([A-Z0-9]{4,32}) - reminal$/.exec(n.who || '') || [])[1] || '';

let entries = []; // newest first: {at, who, text, kind, id, time, hay}
const fmt = new Intl.DateTimeFormat([], { dateStyle: 'medium', timeStyle: 'short' });

function choices(select, first, pairs) {
  const was = select.value;
  const opts = [[ '', first ], ...pairs].map(([value, label]) => { const o = document.createElement('option'); o.value = value; o.textContent = label; return o; });
  select.replaceChildren(...opts);
  if (opts.some((o) => o.value === was)) select.value = was;
}

function draw() {
  const q = $('q').value.trim().toLowerCase(), id = $('session').value, kind = $('kind').value, when = $('when').value;
  const midnight = new Date(); midnight.setHours(0, 0, 0, 0);
  const since = when === 'today' ? midnight.getTime() : when === 'week' ? Date.now() - 7 * 86_400_000 : 0;
  const rows = [];
  for (const n of entries) {
    if ((id && n.id !== id) || (kind && n.kind !== kind) || n.at < since || (q && !n.hay.includes(q))) continue;
    const tr = document.createElement('tr');
    for (const text of [n.time, n.who, n.text]) { const td = document.createElement('td'); td.textContent = text; tr.append(td); }
    rows.push(tr);
  }
  $('rows').replaceChildren(...rows);
  $('count').textContent = !entries.length ? 'Nothing has been kept yet.'
    : !rows.length ? `Nothing matches, of ${entries.length} kept.`
    : rows.length === entries.length ? `${entries.length} ${entries.length === 1 ? 'entry' : 'entries'}, newest first.`
    : `${rows.length} of ${entries.length} entries, newest first.`;
}

async function load() {
  const { notes = [] } = await chrome.storage.local.get('notes');
  entries = notes.map((n) => {
    const who = String(n.who || 'reminal'), text = String(n.text || ''), time = fmt.format(new Date(n.at));
    return { at: n.at, who, text, kind: kindOf(n), id: idOf(n), time, hay: `${who} ${text} ${time}`.toLowerCase() };
  }).reverse();
  // Only the sessions and the kinds that are in the log are offered.
  choices($('session'), 'All sessions', [...new Set(entries.map((n) => n.id).filter(Boolean))].sort().map((id) => [id, `${id} - reminal`]));
  choices($('kind'), 'All kinds of action', Object.entries(KINDS).filter(([k]) => entries.some((n) => n.kind === k)));
  draw();
}

for (const id of ['q', 'session', 'kind', 'when']) $(id).addEventListener('input', draw);
chrome.storage.onChanged.addListener((changes, area) => { if (area === 'local' && 'notes' in changes) load(); });
load();
