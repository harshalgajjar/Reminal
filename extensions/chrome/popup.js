// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// The extension's button: whether reminal is connected, and the sessions
// allowed (Stop) or denied (Forget, so it may ask again).
async function render() {
  const { sessions = {}, host = {} } = await chrome.storage.session.get(['sessions', 'host']);
  const status = document.getElementById('status');
  status.textContent = host.connected ? 'Connected to reminal on this computer.' : `Not connected to reminal${host.error ? `: ${host.error}` : ''}. Is reminal installed, with \`reminal integrate chrome\`?`;
  const list = document.getElementById('list');
  list.replaceChildren();
  const ids = Object.keys(sessions).filter((id) => sessions[id].allowed || sessions[id].denied);
  document.getElementById('none').hidden = ids.length > 0;
  for (const id of ids) {
    const s = sessions[id];
    const li = document.createElement('li');
    const who = document.createElement('span');
    who.className = 'who';
    who.textContent = `${s.name || id}${s.allowed ? '' : ' (denied)'}`;
    who.title = id;
    const b = document.createElement('button');
    b.textContent = s.allowed ? 'Stop' : 'Forget';
    b.addEventListener('click', async () => {
      await chrome.runtime.sendMessage({ t: s.allowed ? 'stop' : 'forget-denial', session: id });
      render();
    });
    li.append(who, b);
    list.append(li);
  }
  if (!host.connected) chrome.runtime.sendMessage({ t: 'reconnect' });
}
render();
chrome.storage.onChanged.addListener(render);
