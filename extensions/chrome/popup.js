// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// The extension's button: whether reminal is connected; the switch for this
// browser profile (on, as installed: any reminal session on this computer may
// use it; off: every request refused); the sessions using it now, with Stop
// (for that run of the session); and the newest lines of the log, with a
// button for the page that has all of it (history.html).
//
// Each part is drawn from what the browser has stored, by a read of its own,
// as soon as that read is back: the switches first (a read of four small
// values), the connection as it was last known, then the lists. Nothing here
// waits for the extension's worker or for reminal: the worker may be asleep,
// and is only told the panel opened (it connects, and tidies what it keeps),
// after which what it stores changes and the part it touches is drawn again.
const $ = (id) => document.getElementById(id);
const SHOWN = 8; // the newest lines of the log shown here

async function drawSwitches() {
  const { agents, ...more } = await chrome.storage.local.get(['agents', 'scripts', 'uploads', 'cookies']);
  const on = agents !== 'off';
  $('agents').checked = on;
  $('state').textContent = on ? 'on' : 'off';
  for (const box of document.querySelectorAll('input.more')) { box.checked = more[box.id] === 'on'; box.disabled = !on; }
}

async function drawStatus() {
  const { host = {} } = await chrome.storage.session.get('host');
  $('status').textContent = host.connected ? 'Connected to reminal on this computer.' : `Not connected to reminal${host.error ? `: ${host.error}` : ''}. Is reminal installed, with \`reminal integrate\`?`;
  if (!host.connected) chrome.runtime.sendMessage({ t: 'reconnect' }).catch(() => {});
}

// What ran that the person should be able to see afterwards: the newest few.
async function drawNotes() {
  const { notes = [] } = await chrome.storage.local.get('notes');
  const items = [];
  for (const n of notes.slice(-SHOWN).reverse()) {
    const li = document.createElement('li');
    li.textContent = `${new Date(n.at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })} ${n.who}: ${n.text}`;
    items.push(li);
  }
  $('notes').replaceChildren(...items);
  $('nonotes').hidden = items.length > 0;
}

// The sessions shown: one whose tab group is open in this browser, or that
// reminal has not said has ended (it is asked about each as the extension
// connects, when this panel opens, and every half minute). One that has
// ended is shown while its group is open, with a button that closes its
// tabs, and the worker forgets it once the group is gone. With reminal not
// connected nothing can say which are running: only those with a group.
const row = (text, title, button, press) => {
  const li = document.createElement('li');
  const who = document.createElement('span');
  who.className = 'who';
  who.textContent = text;
  who.title = title;
  li.append(who);
  if (button) {
    const b = document.createElement('button');
    b.textContent = button;
    b.addEventListener('click', async () => { b.disabled = true; await press().catch(() => {}); drawSessions(); });
    li.append(b);
  }
  return li;
};
async function sessionRows() {
  const [all, groups] = await Promise.all([chrome.storage.session.get(null), chrome.tabGroups.query({}).catch(() => [])]);
  const grouped = new Set();
  for (const g of groups) { const m = /^([A-Z0-9]{4,32}) - reminal$/.exec(g.title || ''); if (m) grouped.add(m[1]); }
  const connected = !!(all.host && all.host.connected);
  const ended = (id) => ('e:' + id) in all;
  const rows = [], seen = new Set();
  for (const [k, s] of Object.entries(all)) {
    if (!k.startsWith('s:') || ended(s.session)) continue;
    seen.add(s.session);
    if (!connected && !grouped.has(s.session)) continue;
    rows.push(row(`${s.session ? s.session + ' - reminal' : 'reminal'}${s.stopped ? ' (stopped)' : ''}`, s.session || '',
      s.stopped ? '' : 'Stop', () => chrome.runtime.sendMessage({ t: 'stop', key: k })));
  }
  for (const id of grouped) {
    if (ended(id)) rows.push(row(`${id} - reminal (ended)`, id, 'Close its tabs', () => chrome.runtime.sendMessage({ t: 'close', session: id })));
    // A group whose session has not used the browser since the extension
    // started: nothing to stop yet.
    else if (!seen.has(id)) rows.push(row(`${id} - reminal`, id, '', null));
  }
  return rows;
}
// One read at a time, and once more if something changed meanwhile: a busy
// session changes its record with every action.
let drawing = false, again = false;
async function drawSessions() {
  if (drawing) { again = true; return; }
  drawing = true;
  try {
    do {
      again = false;
      const rows = await sessionRows();
      $('list').replaceChildren(...rows);
      $('none').hidden = rows.length > 0;
    } while (again);
  } finally { drawing = false; }
}

$('agents').addEventListener('change', async (e) => {
  await chrome.runtime.sendMessage({ t: 'switch', on: e.target.checked });
  drawSwitches();
});
// Scripts in pages, files given to pages, cookies and storage: each its own
// switch, off as installed.
for (const box of document.querySelectorAll('input.more')) {
  box.addEventListener('change', (e) => chrome.storage.local.set({ [box.id]: e.target.checked ? 'on' : 'off' }));
}
$('history').addEventListener('click', () => chrome.tabs.create({ url: chrome.runtime.getURL('history.html') }));

drawSwitches();
drawStatus();
drawSessions();
drawNotes();
chrome.runtime.sendMessage({ t: 'opened' }).catch(() => {});
chrome.storage.onChanged.addListener((changes, area) => {
  const keys = Object.keys(changes);
  if (area === 'local') {
    if (keys.some((k) => k !== 'notes')) drawSwitches();
    if ('notes' in changes) drawNotes();
  } else if (area === 'session') {
    if ('host' in changes) drawStatus();
    drawSessions();
  }
});
for (const ev of [chrome.tabGroups.onCreated, chrome.tabGroups.onRemoved, chrome.tabGroups.onUpdated]) ev.addListener(drawSessions);
