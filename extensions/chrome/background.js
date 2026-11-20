// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// reminal's extension: agents in reminal sessions on this computer use this
// browser, once the person allows each session.
//
//   reminal (native messaging host, `reminal browser-host`)
//     -> {t:"req", id, session, name, machine, tool, args}
//     <- {id, ok, result | error}
//     -> {t:"ended", session}      the session is gone: its consent and group go
//
// Consent is asked here, in the browser, once per session, and kept in
// chrome.storage.session (gone when the browser restarts). Each session
// works only in its own tab group. Typing into password or payment fields is
// refused; an action inside a sign-in form, or with a CAPTCHA showing, is
// paused for the person. Input and screenshots go through chrome.debugger
// (the browser shows its own "is debugging this browser" bar); pages are read
// by scripts injected into the extension's own isolated world.

import * as page from './page.js';

const HOST = 'app.reminal.browser';
const CONSENT_WAIT_MS = 60_000;
const COLORS = ['blue', 'green', 'purple', 'cyan', 'orange', 'pink', 'red', 'yellow', 'grey'];

// ---------------------------------------------------------------- the host
let port = null, backoff = 1000;
function connect() {
  if (port) return;
  try { port = chrome.runtime.connectNative(HOST); } catch (e) { port = null; retry(); return; }
  port.onMessage.addListener(onHostMessage);
  port.onDisconnect.addListener(() => {
    const why = chrome.runtime.lastError && chrome.runtime.lastError.message;
    port = null;
    setStatus({ connected: false, error: why || 'reminal is not running' });
    retry();
  });
}
function retry() { setTimeout(connect, backoff); backoff = Math.min(backoff * 2, 30_000); }
function send(m) { try { port && port.postMessage(m); } catch (_) {} }
async function setStatus(s) { await chrome.storage.session.set({ host: { ...s, at: Date.now() } }); }

async function onHostMessage(m) {
  if (!m || typeof m !== 'object') return;
  if (m.t === 'status') { if (m.ok) backoff = 1000; setStatus({ connected: !!m.ok, error: m.error || '' }); return; }
  if (m.t === 'ended') { await forget(String(m.session || ''), 'the session ended'); return; }
  if (m.t !== 'req' || !m.id) return;
  let out;
  try {
    out = { id: m.id, ok: true, result: await handle(m) };
  } catch (e) {
    out = { id: m.id, ok: false, error: String((e && e.message) || e) };
  }
  send(out);
}

chrome.runtime.onStartup.addListener(connect);
chrome.runtime.onInstalled.addListener(connect);
connect();

// ---------------------------------------------------------------- sessions
// {id: {name, machine, allowed, denied, groupId, lastTab, color}}
async function sessions() { return (await chrome.storage.session.get('sessions')).sessions || {}; }
async function saveSession(id, s) { const all = await sessions(); all[id] = s; await chrome.storage.session.set({ sessions: all }); }
async function dropSession(id) { const all = await sessions(); delete all[id]; await chrome.storage.session.set({ sessions: all }); }

// forget: consent gone, the debugger let go of its tabs, its group undone
// (the tabs stay open: the person may want what is in them).
async function forget(id, why) {
  const s = (await sessions())[id];
  if (!s) return;
  await dropSession(id);
  for (const tab of await groupTabs(s)) {
    await detach(tab.id);
    try { await chrome.tabs.ungroup(tab.id); } catch (_) {}
  }
  console.log('[reminal] stopped', id, why);
}

// ---------------------------------------------------------------- consent
const pending = new Map(); // session id -> {promise, resolve, win}

async function consent(m) {
  const all = await sessions();
  const s = all[m.session];
  if (s && s.allowed) return s;
  if (s && s.denied) throw new Error('The person did not allow browser use for this session.');
  let p = pending.get(m.session);
  if (!p) {
    await saveSession(m.session, { ...(s || {}), name: m.name || m.session, machine: m.machine || '' });
    let resolve;
    const promise = new Promise((r) => { resolve = r; });
    p = { promise, resolve, win: null };
    pending.set(m.session, p);
    const q = new URLSearchParams({ session: m.session, name: m.name || '', machine: m.machine || '' });
    const w = await chrome.windows.create({ url: 'consent.html?' + q, type: 'popup', width: 440, height: 330, focused: true });
    p.win = w.id;
    chrome.notifications.create('consent-' + m.session, {
      type: 'basic', iconUrl: 'icons/128.png', title: 'reminal',
      message: `${label(m)} wants to use your browser. Answer in the reminal window.`,
    });
    setTimeout(() => answer(m.session, null), CONSENT_WAIT_MS);
  }
  const allowed = await p.promise;
  if (!allowed) throw new Error('The person did not allow browser use for this session.');
  return (await sessions())[m.session];
}

function label(m) { return `${m.name ? `"${m.name}" (${m.session})` : m.session}, a reminal session on this computer${m.machine ? ` (${m.machine})` : ''},`; }

// answer: true (allow), false (deny), null (no answer in time: asked again
// next time, not remembered).
async function answer(id, allow) {
  const p = pending.get(id);
  if (!p) return;
  pending.delete(id);
  if (allow !== null) {
    const all = await sessions();
    const s = all[id] || {};
    await saveSession(id, { ...s, allowed: !!allow, denied: !allow });
  }
  if (p.win) chrome.windows.remove(p.win).catch(() => {});
  chrome.notifications.clear('consent-' + id);
  p.resolve(!!allow);
}

chrome.runtime.onMessage.addListener((msg, sender, reply) => {
  // Only the extension's own pages (consent.html, popup.html) speak here.
  if (sender.id !== chrome.runtime.id || !sender.url || !sender.url.startsWith(chrome.runtime.getURL(''))) return;
  if (msg.t === 'consent') answer(String(msg.session), !!msg.allow).then(() => reply(true));
  else if (msg.t === 'stop') forget(String(msg.session), 'stopped by the person').then(() => reply(true));
  else if (msg.t === 'forget-denial') dropSession(String(msg.session)).then(() => reply(true));
  else if (msg.t === 'reconnect') { backoff = 1000; connect(); reply(true); }
  else return;
  return true;
});
chrome.windows.onRemoved.addListener((w) => { for (const [id, p] of pending) if (p.win === w) answer(id, null); });

// ---------------------------------------------------------------- the tools
async function handle(m) {
  const s = await consent(m);
  s.name = m.name || s.name || m.session;
  const a = m.args || {};
  switch (m.tool) {
    case 'tabs_context': return tabsContext(m.session, s);
    case 'tabs_create': return tabsCreate(m.session, s, a.url);
  }
  const tabId = await sessionTab(m.session, s, a.tabId);
  await indicate(tabId, s.name, true);
  try {
    switch (m.tool) {
      case 'navigate': return await navigate(m.session, s, tabId, String(a.url || ''));
      case 'screenshot': return await screenshot(tabId);
      case 'read_page': return await inPage(tabId, page.readTree, a.filter === 'interactive');
      case 'get_page_text': return await inPage(tabId, page.pageText);
      case 'click': return await click(tabId, a);
      case 'type': return await type(tabId, a);
      case 'key': return await key(tabId, String(a.key || ''));
      case 'scroll': return await scroll(tabId, a);
    }
    throw new Error('unknown tool: ' + m.tool);
  } finally {
    indicate(tabId, s.name, false);
  }
}

async function groupTabs(s) {
  if (s.groupId == null) return [];
  try { return await chrome.tabs.query({ groupId: s.groupId }); } catch (_) { return []; }
}

async function tabsContext(id, s) {
  const tabs = await groupTabs(s);
  if (!tabs.length) return 'This session has no tabs yet: open one with browser_tabs_create.';
  return JSON.stringify(tabs.map((t) => ({ tabId: t.id, title: t.title, url: t.url, active: t.active, last: t.id === s.lastTab })));
}

async function tabsCreate(id, s, url) {
  if (url) refuseURL(url);
  const tab = await chrome.tabs.create({ url: url || 'about:blank', active: true });
  let group = s.groupId;
  try { if (group != null) await chrome.tabGroups.get(group); } catch (_) { group = null; }
  if (group == null) {
    group = await chrome.tabs.group({ tabIds: [tab.id] });
    const all = await sessions();
    const color = COLORS[Object.keys(all).length % COLORS.length];
    await chrome.tabGroups.update(group, { title: `reminal: ${s.name}`, color });
  } else {
    await chrome.tabs.group({ tabIds: [tab.id], groupId: group });
  }
  await saveSession(id, { ...s, groupId: group, lastTab: tab.id });
  if (url) await loaded(tab.id);
  return JSON.stringify({ tabId: tab.id, url: url || 'about:blank' });
}

// sessionTab: the tab asked for, which must be in the session's group; or
// the one it used last; or its first.
async function sessionTab(id, s, want) {
  const tabs = await groupTabs(s);
  if (!tabs.length) throw new Error('This session has no tabs yet: open one with browser_tabs_create.');
  let tab;
  if (want != null) {
    tab = tabs.find((t) => t.id === Number(want));
    if (!tab) throw new Error(`Tab ${want} is not in this session's tab group.`);
  } else {
    tab = tabs.find((t) => t.id === s.lastTab) || tabs[0];
  }
  refuseURL(tab.url || tab.pendingUrl || '');
  if (s.lastTab !== tab.id) await saveSession(id, { ...s, lastTab: tab.id });
  return tab.id;
}

// The browser's own pages, other extensions and the Web Store are not for agents.
function refuseURL(url) {
  if (/^(chrome|edge|brave|about:(?!blank)|chrome-extension|devtools|view-source):/i.test(url) ||
      /^https:\/\/(chrome\.google\.com\/webstore|chromewebstore\.google\.com)/i.test(url)) {
    throw new Error('Agents may not use the browser\'s own pages, extensions or the Web Store.');
  }
}

async function navigate(id, s, tabId, url) {
  if (url === 'back') await chrome.tabs.goBack(tabId);
  else if (url === 'forward') await chrome.tabs.goForward(tabId);
  else {
    if (!/^[a-z][a-z0-9+.-]*:/i.test(url)) url = 'https://' + url;
    refuseURL(url);
    await chrome.tabs.update(tabId, { url });
  }
  await loaded(tabId);
  const t = await chrome.tabs.get(tabId);
  return JSON.stringify({ tabId, title: t.title, url: t.url });
}

function loaded(tabId) {
  return new Promise((resolve) => {
    let done = false;
    const finish = () => { if (done) return; done = true; chrome.tabs.onUpdated.removeListener(on); clearTimeout(t); setTimeout(resolve, 150); };
    const on = (id, info) => { if (id === tabId && info.status === 'complete') finish(); };
    const t = setTimeout(finish, 30_000);
    chrome.tabs.onUpdated.addListener(on);
    chrome.tabs.get(tabId).then((tab) => { if (tab.status === 'complete' && !tab.pendingUrl) setTimeout(() => chrome.tabs.get(tabId).then((x) => { if (x.status === 'complete') finish(); }), 300); }).catch(finish);
  });
}

// ---------------------------------------------------------------- the debugger
const attached = new Set();
async function cdp(tabId, method, params = {}) {
  if (!attached.has(tabId)) {
    await chrome.debugger.attach({ tabId }, '1.3');
    attached.add(tabId);
  }
  return chrome.debugger.sendCommand({ tabId }, method, params);
}
async function detach(tabId) { if (attached.delete(tabId)) { try { await chrome.debugger.detach({ tabId }); } catch (_) {} } }
chrome.debugger.onDetach.addListener((src) => attached.delete(src.tabId));
chrome.tabs.onRemoved.addListener((tabId) => attached.delete(tabId));

async function screenshot(tabId) {
  const m = await cdp(tabId, 'Page.getLayoutMetrics');
  const vp = m.cssVisualViewport || m.visualViewport;
  const w = Math.round(vp.clientWidth), h = Math.round(vp.clientHeight);
  // The visible part, at CSS pixels: the coordinates browser_click takes.
  const shot = await cdp(tabId, 'Page.captureScreenshot', { format: 'png', clip: { x: vp.pageX, y: vp.pageY, width: w, height: h, scale: 1 } });
  return { image: shot.data, mimeType: 'image/png', note: `${w}x${h} pixels; click at x, y in these pixels.` };
}

async function inPage(tabId, fn, ...args) {
  await chrome.scripting.executeScript({ target: { tabId }, files: ['rules.js'] });
  const [r] = await chrome.scripting.executeScript({ target: { tabId }, func: fn, args });
  if (!r) throw new Error('The page could not be read.');
  return r.result;
}

// target: where an action lands, and whether it is paused there.
async function target(tabId, a) {
  const t = await inPage(tabId, page.locate, a.ref || null, a.x == null ? null : Number(a.x), a.y == null ? null : Number(a.y));
  if (t.error) throw new Error(t.error);
  if (t.paused) throw new Error(t.paused);
  return t;
}

async function click(tabId, a) {
  const t = await target(tabId, a);
  const button = ['left', 'right', 'middle'].includes(a.button) ? a.button : 'left';
  const count = a.double ? 2 : 1;
  await cdp(tabId, 'Input.dispatchMouseEvent', { type: 'mouseMoved', x: t.x, y: t.y });
  for (let i = 1; i <= count; i++) {
    await cdp(tabId, 'Input.dispatchMouseEvent', { type: 'mousePressed', x: t.x, y: t.y, button, clickCount: i });
    await cdp(tabId, 'Input.dispatchMouseEvent', { type: 'mouseReleased', x: t.x, y: t.y, button, clickCount: i });
  }
  return `Clicked ${t.what} at ${Math.round(t.x)}, ${Math.round(t.y)}.`;
}

async function type(tabId, a) {
  if (a.ref) await click(tabId, { ref: a.ref });
  const f = await inPage(tabId, page.focused);
  if (f.refused) throw new Error(f.refused);
  if (f.paused) throw new Error(f.paused);
  await cdp(tabId, 'Input.insertText', { text: String(a.text || '') });
  return `Typed ${String(a.text || '').length} characters into ${f.what}.`;
}

const KEYS = {
  enter: ['Enter', 'Enter', 13, '\r'], tab: ['Tab', 'Tab', 9], escape: ['Escape', 'Escape', 27], esc: ['Escape', 'Escape', 27],
  backspace: ['Backspace', 'Backspace', 8], delete: ['Delete', 'Delete', 46], space: [' ', 'Space', 32, ' '],
  arrowup: ['ArrowUp', 'ArrowUp', 38], arrowdown: ['ArrowDown', 'ArrowDown', 40], arrowleft: ['ArrowLeft', 'ArrowLeft', 37], arrowright: ['ArrowRight', 'ArrowRight', 39],
  up: ['ArrowUp', 'ArrowUp', 38], down: ['ArrowDown', 'ArrowDown', 40], left: ['ArrowLeft', 'ArrowLeft', 37], right: ['ArrowRight', 'ArrowRight', 39],
  home: ['Home', 'Home', 36], end: ['End', 'End', 35], pageup: ['PageUp', 'PageUp', 33], pagedown: ['PageDown', 'PageDown', 34],
};
const MODS = { alt: 1, ctrl: 2, control: 2, meta: 4, cmd: 4, command: 4, shift: 8 };

async function key(tabId, combo) {
  const parts = combo.split('+').map((p) => p.trim()).filter(Boolean);
  if (!parts.length) throw new Error('key is required');
  let mods = 0;
  for (const p of parts.slice(0, -1)) {
    const m = MODS[p.toLowerCase()];
    if (!m) throw new Error('unknown modifier: ' + p);
    mods |= m;
  }
  const last = parts[parts.length - 1];
  let k = KEYS[last.toLowerCase()];
  if (!k && last.length === 1) k = [last, last.match(/[a-z]/i) ? 'Key' + last.toUpperCase() : '', last.toUpperCase().charCodeAt(0), last];
  if (!k && /^f([1-9]|1[0-2])$/i.test(last)) k = [last.toUpperCase(), last.toUpperCase(), 111 + Number(last.slice(1))];
  if (!k) throw new Error('unknown key: ' + last);
  const f = await inPage(tabId, page.focused);
  // A key that types (or sends) in a password or payment field is the person's.
  if (f.refused && (k[3] || mods === 0 && k[0] === 'Enter')) throw new Error(f.refused);
  if (f.paused && (k[3] || k[0] === 'Enter')) throw new Error(f.paused);
  const text = mods & ~8 ? undefined : k[3];
  const base = { key: k[0], code: k[1], windowsVirtualKeyCode: k[2], nativeVirtualKeyCode: k[2], modifiers: mods };
  await cdp(tabId, 'Input.dispatchKeyEvent', { type: text ? 'keyDown' : 'rawKeyDown', ...base, text, unmodifiedText: text });
  await cdp(tabId, 'Input.dispatchKeyEvent', { type: 'keyUp', ...base });
  return `Pressed ${combo}.`;
}

async function scroll(tabId, a) {
  const t = a.ref ? await target(tabId, { ref: a.ref }) : await inPage(tabId, page.middle);
  if (t.error) throw new Error(t.error);
  const n = Math.max(1, Math.min(50, Number(a.amount) || 3)) * 100;
  const d = { down: [0, n], up: [0, -n], right: [n, 0], left: [-n, 0] }[a.direction || 'down'];
  if (!d) throw new Error('direction is up, down, left or right');
  await cdp(tabId, 'Input.dispatchMouseEvent', { type: 'mouseWheel', x: t.x, y: t.y, deltaX: d[0], deltaY: d[1] });
  return `Scrolled ${a.direction || 'down'}.`;
}

// The page shows that an agent is acting on it: a border and a label.
const hideTimers = new Map();
async function indicate(tabId, name, on) {
  clearTimeout(hideTimers.get(tabId));
  const run = (show) => chrome.scripting.executeScript({ target: { tabId }, func: page.indicator, args: [show, name] }).catch(() => {});
  if (on) await run(true);
  else hideTimers.set(tabId, setTimeout(() => run(false), 1500));
}
