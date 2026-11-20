// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// The extension's button: whether reminal is connected, and the sessions
// allowed (Stop) or denied (Forget, so it may ask again).
async function render() {
  const all = await chrome.storage.session.get(null);
  const host = all.host || {};
  const status = document.getElementById('status');
  status.textContent = host.connected ? 'Connected to reminal on this computer.' : `Not connected to reminal${host.error ? `: ${host.error}` : ''}. Is reminal installed, with \`reminal integrate\`?`;
  const list = document.getElementById('list');
  list.replaceChildren();
  const keys = Object.keys(all).filter((k) => k.startsWith('s:') && (all[k].allowed || all[k].denied));
  document.getElementById('none').hidden = keys.length > 0;
  for (const k of keys) {
    const s = all[k];
    const li = document.createElement('li');
    const who = document.createElement('span');
    who.className = 'who';
    who.textContent = `${s.name || s.session}${s.allowed ? '' : ' (denied)'}`;
    who.title = s.session || '';
    const b = document.createElement('button');
    b.textContent = s.allowed ? 'Stop' : 'Forget';
    b.addEventListener('click', async () => {
      await chrome.runtime.sendMessage({ t: s.allowed ? 'stop' : 'forget-denial', key: k });
      render();
    });
    li.append(who, b);
    list.append(li);
  }
  if (!host.connected) chrome.runtime.sendMessage({ t: 'reconnect' });
}
render();
chrome.storage.onChanged.addListener(render);
