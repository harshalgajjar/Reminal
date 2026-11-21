// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// The extension's button: whether reminal is connected; the switch for this
// browser profile (on, as installed: any reminal session on this computer may
// use it; off: every request refused); and the sessions using it now, with Stop (for that
// run of the session).
async function render() {
  const all = await chrome.storage.session.get(null);
  const { agents } = await chrome.storage.local.get('agents');
  const host = all.host || {};
  document.getElementById('status').textContent = host.connected ? 'Connected to reminal on this computer.' : `Not connected to reminal${host.error ? `: ${host.error}` : ''}. Is reminal installed, with \`reminal integrate\`?`;
  const on = agents !== 'off';
  document.getElementById('agents').checked = on;
  document.getElementById('state').textContent = on ? 'on' : 'off';
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
render();
chrome.storage.onChanged.addListener(render);
