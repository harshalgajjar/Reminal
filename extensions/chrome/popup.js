// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// The extension's button: whether reminal is connected; the switch for this
// browser profile (on, as installed: any reminal session on this computer may
// use it; off: every request refused); and the sessions using it now, with Stop (for that
// run of the session).
async function render() {
  const all = await chrome.storage.session.get(null);
  const { agents, ...more } = await chrome.storage.local.get(['agents', 'scripts', 'uploads', 'cookies']);
  const host = all.host || {};
  document.getElementById('status').textContent = host.connected ? 'Connected to reminal on this computer.' : `Not connected to reminal${host.error ? `: ${host.error}` : ''}. Is reminal installed, with \`reminal integrate\`?`;
  const on = agents !== 'off';
  document.getElementById('agents').checked = on;
  document.getElementById('state').textContent = on ? 'on' : 'off';
  for (const box of document.querySelectorAll('input.more')) { box.checked = more[box.id] === 'on'; box.disabled = !on; }
  // What ran that the person should be able to see afterwards.
  const notes = document.getElementById('notes');
  notes.replaceChildren();
  for (const n of (all.notes || []).slice(-8).reverse()) {
    const li = document.createElement('li');
    li.textContent = `${new Date(n.at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })} ${n.who}: ${n.text}`;
    notes.append(li);
  }
  const list = document.getElementById('list');
  list.replaceChildren();
  const keys = Object.keys(all).filter((k) => k.startsWith('s:'));
  document.getElementById('none').hidden = keys.length > 0;
  for (const k of keys) {
    const s = all[k];
    const li = document.createElement('li');
    const who = document.createElement('span');
    who.className = 'who';
    who.textContent = `${s.name || s.session}${s.stopped ? ' (stopped)' : ''}`;
    who.title = s.session || '';
    li.append(who);
    if (!s.stopped) {
      const b = document.createElement('button');
      b.textContent = 'Stop';
      b.addEventListener('click', async () => { await chrome.runtime.sendMessage({ t: 'stop', key: k }); render(); });
      li.append(b);
    }
    list.append(li);
  }
  if (!host.connected) chrome.runtime.sendMessage({ t: 'reconnect' });
}
document.getElementById('agents').addEventListener('change', async (e) => {
  await chrome.runtime.sendMessage({ t: 'switch', on: e.target.checked });
  render();
});
// Scripts in pages, files given to pages, cookies and storage: each its own
// switch, off as installed.
for (const box of document.querySelectorAll('input.more')) {
  box.addEventListener('change', async (e) => {
    await chrome.storage.local.set({ [box.id]: e.target.checked ? 'on' : 'off' });
    render();
  });
}
render();
chrome.storage.onChanged.addListener(render);
