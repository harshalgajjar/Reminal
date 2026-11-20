// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// reminal's extension: agents in reminal sessions on this computer use this
// browser, once the person allows each session.
//
//   reminal (native messaging host, `reminal browser-host`)
//     -> {t:"req", id, session, run, name, machine, tool, args}
//     <- {id, ok, result | error}
//     -> {t:"ended", session}      the session is gone: its consent and group go
//
// Consent is asked here, in the browser, for one run of one session (its id
// and when its process started: a session restored or an id used again is
// asked again), and kept in chrome.storage.session under that key (gone
// when the browser restarts). Stop wins: it bumps the key's epoch, and every
// action checks consent and the epoch again right before each debugger call.
// Each session works only in its own tab group. Typing into password or
// payment fields is refused, in any frame; an action inside a sign-in, or
// with a CAPTCHA showing, is paused for the person. Input and screenshots go
// through chrome.debugger (the browser shows its own "is debugging this
// browser" bar); pages are read by scripts in the extension's isolated world.

import * as page from './page.js';

const HOST = 'app.reminal.browser';
const CONSENT_WAIT_MS = 60_000;
const COLORS = ['blue', 'green', 'purple', 'cyan', 'orange', 'pink', 'red', 'yellow', 'grey'];
const STOPPED = 'The person stopped browser use for this session.';
const NOT_ALLOWED = 'The person did not allow browser use for this session.';

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
  if (m.t === 'refused') { setStatus({ connected: false, error: m.error || 'refused' }); return; }
  if (m.t === 'ended') { await endSession(String(m.session || '')); return; }
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
// One storage key per run of a session: "s:<id>:<run>" ->
// {session, run, name, machine, allowed, denied, groupId, lastTab}. Each
// read is fresh; a write merges into what is stored now, and never brings a
// stopped one back (only the consent answer creates a record).
const keyOf = (m) => `s:${m.session}:${m.run || ''}`;
async function getS(k) { return (await chrome.storage.session.get(k))[k] || null; }
async function putS(k, patch, create = false) {
  const cur = await getS(k);
  if (!cur && !create) return null;
  const next = { ...(cur || {}), ...patch };
  await chrome.storage.session.set({ [k]: next });
  return next;
}
async function allS() {
  const all = await chrome.storage.session.get(null);
  return Object.entries(all).filter(([k]) => k.startsWith('s:'));
}

// The epoch of each key: bumped by Stop and by the session's end, checked
// before every debugger call of an action that began under an older one.
const epochs = new Map();
const epochOf = (k) => epochs.get(k) || 0;
const bump = (k) => epochs.set(k, epochOf(k) + 1);

// forget: consent gone, the debugger let go of its tabs, its group undone
// (the tabs stay open: the person may want what is in them).
async function forget(k, why) {
  bump(k);
  const s = await getS(k);
  await chrome.storage.session.remove(k);
  if (!s) return;
  for (const tab of await groupTabs(s)) {
    await detach(tab.id);
    try { await chrome.tabs.ungroup(tab.id); } catch (_) {}
  }
  console.log('[reminal] stopped', k, why);
}
async function endSession(id) {
  for (const [k] of await allS()) if (k.startsWith(`s:${id}:`)) await forget(k, 'the session ended');
}

// ---------------------------------------------------------------- consent
const pending = new Map(); // key -> {promise, resolve, win}

async function consent(m) {
  const k = keyOf(m);
  const s = await getS(k);
  if (s && s.allowed) return k;
  if (s && s.denied) throw new Error(NOT_ALLOWED);
  let p = pending.get(k);
  if (!p) {
    let resolve;
    const promise = new Promise((r) => { resolve = r; });
    p = { promise, resolve, win: null };
    pending.set(k, p);
    await putS(k, { session: m.session, run: m.run || '', name: m.name || m.session, machine: m.machine || '' }, true);
    const q = new URLSearchParams({ key: k, session: m.session, name: m.name || '', machine: m.machine || '' });
    const w = await chrome.windows.create({ url: 'consent.html?' + q, type: 'popup', width: 440, height: 330, focused: true });
    p.win = w.id;
    chrome.notifications.create('consent-' + k, {
      type: 'basic', iconUrl: 'icons/128.png', title: 'reminal',
      message: `${label(m)} wants to use your browser. Answer in the reminal window.`,
    });
    // This prompt's own timer: it answers this prompt, never a later one.
    const mine = p;
    setTimeout(() => { if (pending.get(k) === mine) answer(k, null); }, CONSENT_WAIT_MS);
  }
  const allowed = await p.promise;
  if (!allowed) throw new Error(NOT_ALLOWED);
  return k;
}

function label(m) { return `${m.name ? `"${m.name}" (${m.session})` : m.session}, a reminal session on this computer${m.machine ? ` (${m.machine})` : ''},`; }

// answer: true (allow), false (deny), null (no answer in time: asked again
// next time, not remembered).
async function answer(k, allow) {
  const p = pending.get(k);
  if (!p) return;
  pending.delete(k);
  if (allow === null) await chrome.storage.session.remove(k);
  else await putS(k, { allowed: !!allow, denied: !allow }, true);
  if (p.win) chrome.windows.remove(p.win).catch(() => {});
  chrome.notifications.clear('consent-' + k);
  p.resolve(!!allow);
}

chrome.runtime.onMessage.addListener((msg, sender, reply) => {
  // Only the extension's own pages (consent.html, popup.html) speak here.
  if (sender.id !== chrome.runtime.id || !sender.url || !sender.url.startsWith(chrome.runtime.getURL(''))) return;
  const k = String(msg.key || '');
  if (msg.t === 'consent') answer(k, !!msg.allow).then(() => reply(true));
  else if (msg.t === 'stop') forget(k, 'stopped by the person').then(() => reply(true));
  else if (msg.t === 'forget-denial') chrome.storage.session.remove(k).then(() => reply(true));
  else if (msg.t === 'reconnect') { backoff = 1000; connect(); reply(true); }
  else return;
  return true;
});
chrome.windows.onRemoved.addListener((w) => { for (const [k, p] of pending) if (p.win === w) answer(k, null); });

// ---------------------------------------------------------------- the tools
// A request's own view of its session: still allowed, under the epoch it
// began with. Checked before each step that touches a page.
function guardFor(k) {
  const epoch = epochOf(k);
  return async () => {
    const s = await getS(k);
    if (!s || !s.allowed || epochOf(k) !== epoch) throw new Error(STOPPED);
    return s;
  };
}

async function handle(m) {
  const k = await consent(m);
  const alive = guardFor(k);
  const s = await alive();
  const name = m.name || s.name || m.session;
  if (m.name && m.name !== s.name) await putS(k, { name: m.name });
  const a = m.args || {};
  switch (m.tool) {
    case 'tabs_context': return tabsContext(s);
    case 'tabs_create': return tabsCreate(k, alive, name, a.url);
  }
  const tabId = await sessionTab(k, s, a.tabId);
  const c = { tabId, alive };
  await indicate(tabId, name, true);
  try {
    switch (m.tool) {
      case 'navigate': return await navigate(c, String(a.url ?? ''));
      case 'screenshot': return await screenshot(c);
      case 'read_page': return await inPage(c, page.readTree, a.filter === 'interactive');
      case 'get_page_text': return await inPage(c, page.pageText);
      case 'click': return await click(c, a);
      case 'type': return await type(c, a);
      case 'key': return await key(c, String(a.key || ''));
      case 'scroll': return await scroll(c, a);
    }
    throw new Error('unknown tool: ' + m.tool);
  } finally {
    indicate(tabId, name, false);
  }
}

async function groupTabs(s) {
  if (s.groupId == null) return [];
  try { return await chrome.tabs.query({ groupId: s.groupId }); } catch (_) { return []; }
}

async function tabsContext(s) {
  const tabs = await groupTabs(s);
  if (!tabs.length) return 'This session has no tabs yet: open one with browser_tabs_create.';
  return JSON.stringify(tabs.map((t) => ({ tabId: t.id, title: t.title, url: t.url, active: t.active, last: t.id === s.lastTab })));
}

async function tabsCreate(k, alive, name, url) {
  const href = url ? allowedURL(url) : 'about:blank';
  let s = await alive();
  const tab = await chrome.tabs.create({ url: href, active: true });
  let group = s.groupId;
  try { if (group != null) await chrome.tabGroups.get(group); } catch (_) { group = null; }
  try {
    s = await alive();
  } catch (e) {
    chrome.tabs.remove(tab.id).catch(() => {});
    throw e;
  }
  if (group == null) {
    group = await chrome.tabs.group({ tabIds: [tab.id] });
    const n = (await allS()).length;
    await chrome.tabGroups.update(group, { title: `reminal: ${name}`, color: COLORS[n % COLORS.length] });
  } else {
    await chrome.tabs.group({ tabIds: [tab.id], groupId: group });
  }
  await putS(k, { groupId: group, lastTab: tab.id });
  if (url) await loaded(tab.id);
  return JSON.stringify({ tabId: tab.id, url: href });
}

// sessionTab: the tab asked for, which must be in the session's group; or
// the one it used last; or its first.
async function sessionTab(k, s, want) {
  const tabs = await groupTabs(s);
  if (!tabs.length) throw new Error('This session has no tabs yet: open one with browser_tabs_create.');
  let tab;
  if (want != null) {
    tab = tabs.find((t) => t.id === Number(want));
    if (!tab) throw new Error(`Tab ${want} is not in this session's tab group.`);
  } else {
    tab = tabs.find((t) => t.id === s.lastTab) || tabs[0];
  }
  const url = tab.url || tab.pendingUrl || '';
  if (url) allowedURL(url);
  if (s.lastTab !== tab.id) await putS(k, { lastTab: tab.id });
  return tab.id;
}

// allowedURL: only web pages (http, https) and a blank tab; never the
// browser's own pages, extensions, files, data or script URLs, the Web
// Store, or a URL dressed up with a user name to look like another site.
// Returns the URL as the browser will read it.
const NO_URL = 'Agents may only open web pages (http and https), not the browser\'s own pages, extensions, files or the Web Store.';
function allowedURL(raw) {
  const s = String(raw);
  if (!s || s !== s.trim() || /[\u0000-\u001f\u007f]/.test(s)) throw new Error(NO_URL);
  if (s === 'about:blank') return s;
  let u;
  try { u = new URL(/^[a-z][a-z0-9+.-]*:/i.test(s) ? s : 'https://' + s); } catch (_) { throw new Error(NO_URL); }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') throw new Error(NO_URL);
  if (u.username || u.password) throw new Error(NO_URL);
  const host = u.hostname.toLowerCase().replace(/\.$/, '');
  if (host === 'chromewebstore.google.com' || (host === 'chrome.google.com' && /^\/webstore/i.test(u.pathname)) || host === 'microsoftedge.microsoft.com') throw new Error(NO_URL);
  return u.href;
}

async function navigate(c, url) {
  await c.alive();
  if (url === 'back') await chrome.tabs.goBack(c.tabId);
  else if (url === 'forward') await chrome.tabs.goForward(c.tabId);
  else await chrome.tabs.update(c.tabId, { url: allowedURL(url) });
  await loaded(c.tabId);
  const t = await chrome.tabs.get(c.tabId);
  return JSON.stringify({ tabId: c.tabId, title: t.title, url: t.url });
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
// cdp: a debugger call, made only while the session is still allowed under
// the epoch its action began with (Stop wins over an action under way).
async function cdp(c, method, params = {}) {
  await c.alive();
  if (!attached.has(c.tabId)) {
    await chrome.debugger.attach({ tabId: c.tabId }, '1.3');
    attached.add(c.tabId);
  }
  return chrome.debugger.sendCommand({ tabId: c.tabId }, method, params);
}
async function detach(tabId) { if (attached.delete(tabId)) { try { await chrome.debugger.detach({ tabId }); } catch (_) {} } }
chrome.debugger.onDetach.addListener((src) => attached.delete(src.tabId));
chrome.tabs.onRemoved.addListener((tabId) => attached.delete(tabId));

async function screenshot(c) {
  const m = await cdp(c, 'Page.getLayoutMetrics');
  const vp = m.cssVisualViewport || m.visualViewport;
  const w = Math.round(vp.clientWidth), h = Math.round(vp.clientHeight);
  // The visible part, at CSS pixels: the coordinates browser_click takes.
  const shot = await cdp(c, 'Page.captureScreenshot', { format: 'png', clip: { x: vp.pageX, y: vp.pageY, width: w, height: h, scale: 1 } });
  return { image: shot.data, mimeType: 'image/png', note: `${w}x${h} pixels; click at x, y in these pixels.` };
}

// inPage: fn in the page's top frame, after the rules. inFrames: in every
// frame the extension can reach (each frame's own answer, frameId 0 the top).
async function inPage(c, fn, ...args) {
  await c.alive();
  await chrome.scripting.executeScript({ target: { tabId: c.tabId }, files: ['rules.js'] });
  const [r] = await chrome.scripting.executeScript({ target: { tabId: c.tabId }, func: fn, args });
  if (!r) throw new Error('The page could not be read.');
  return r.result;
}
async function inFrames(c, fn) {
  await c.alive();
  await chrome.scripting.executeScript({ target: { tabId: c.tabId, allFrames: true }, files: ['rules.js'] }).catch(() => {});
  const rs = await chrome.scripting.executeScript({ target: { tabId: c.tabId, allFrames: true }, func: fn }).catch(() => []);
  return rs.filter((r) => r && r.result);
}

// target: where an action lands, and whether it is paused there. A CAPTCHA
// in any frame pauses it; an action landing on a frame is paused when any
// frame holds something only the person does.
async function target(c, a) {
  const t = await inPage(c, page.locate, a.ref || null, a.x == null ? null : Number(a.x), a.y == null ? null : Number(a.y));
  if (t.error) throw new Error(t.error);
  if (t.paused) throw new Error(t.paused);
  const frames = await inFrames(c, page.frameCheck);
  if (frames.some((r) => r.result.captcha)) throw new Error('Paused: a CAPTCHA is showing on this page. Ask the person to solve it, then continue.');
  if (t.frame) {
    // A frame whose inside cannot be checked (not a web page, or no frame
    // answered for it: another extension's menu, say) is the person's too.
    const src = t.frameSrc || '';
    const web = /^(https?:|about:blank$|about:srcdoc$)/i.test(src);
    if (!web || !frames.some((r) => r.frameId !== 0 && sameDoc(r.result.url, src))) {
      throw new Error('Paused: that part of the page is a frame reminal cannot check. Ask the person to do it, then continue.');
    }
    if (frames.some((r) => r.frameId !== 0 && r.result.personOnly)) {
      throw new Error('Paused: that part of the page is a frame with a sign-in, payment field or CAPTCHA in it. Ask the person to do it, then continue.');
    }
  }
  return t;
}

// sameDoc: a frame's URL now is the one its element named (its origin and
// path: a redirect within the site, a hash, still count).
function sameDoc(a, b) {
  if (a === b) return true;
  try { const x = new URL(a), y = new URL(b); return x.origin === y.origin && x.pathname === y.pathname; } catch (_) { return false; }
}

// focusedField: what typing goes into, in whichever frame has the focus. A
// focus in a frame that cannot be read is refused: it could be anything.
async function focusedField(c) {
  const rs = await inFrames(c, page.focused);
  if (rs.some((r) => r.result.captcha)) return { paused: 'Paused: a CAPTCHA is showing on this page. Ask the person to solve it, then continue.' };
  const leaf = rs.find((r) => r.result.hasFocus && !r.result.frame);
  if (leaf) return leaf.result;
  const top = rs.find((r) => r.frameId === 0);
  if (!top || top.result.frame) return { refused: 'Refused: the focus is in a frame reminal cannot check. Ask the person to type it.' };
  return top.result;
}

async function click(c, a) {
  const t = await target(c, a);
  const button = ['left', 'right', 'middle'].includes(a.button) ? a.button : 'left';
  const count = a.double ? 2 : 1;
  await cdp(c, 'Input.dispatchMouseEvent', { type: 'mouseMoved', x: t.x, y: t.y });
  for (let i = 1; i <= count; i++) {
    await cdp(c, 'Input.dispatchMouseEvent', { type: 'mousePressed', x: t.x, y: t.y, button, clickCount: i });
    await cdp(c, 'Input.dispatchMouseEvent', { type: 'mouseReleased', x: t.x, y: t.y, button, clickCount: i });
  }
  return `Clicked ${t.what} at ${Math.round(t.x)}, ${Math.round(t.y)}.`;
}

async function type(c, a) {
  if (a.ref) {
    // A password or payment field is refused as such, before it is clicked.
    const t = await inPage(c, page.locate, a.ref, null, null);
    if (t.error) throw new Error(t.error);
    if (t.secret) throw new Error(t.secret);
    await click(c, { ref: a.ref });
  }
  const f = await focusedField(c);
  if (f.refused) throw new Error(f.refused);
  if (f.paused) throw new Error(f.paused);
  await cdp(c, 'Input.insertText', { text: String(a.text || '') });
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

async function key(c, combo) {
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
  // Every key in a password or payment field, or in a sign-in, is the
  // person's: Backspace edits a password, Tab or ArrowDown can take the
  // browser's saved sign-in, Enter sends it.
  const f = await focusedField(c);
  if (f.refused) throw new Error(f.refused);
  if (f.paused) throw new Error(f.paused);
  const text = mods & ~8 ? undefined : k[3];
  const base = { key: k[0], code: k[1], windowsVirtualKeyCode: k[2], nativeVirtualKeyCode: k[2], modifiers: mods };
  await cdp(c, 'Input.dispatchKeyEvent', { type: text ? 'keyDown' : 'rawKeyDown', ...base, text, unmodifiedText: text });
  await cdp(c, 'Input.dispatchKeyEvent', { type: 'keyUp', ...base });
  return `Pressed ${combo}.`;
}

async function scroll(c, a) {
  const t = a.ref ? await target(c, { ref: a.ref }) : await inPage(c, page.middle);
  if (t.error) throw new Error(t.error);
  const n = Math.max(1, Math.min(50, Number(a.amount) || 3)) * 100;
  const d = { down: [0, n], up: [0, -n], right: [n, 0], left: [-n, 0] }[a.direction || 'down'];
  if (!d) throw new Error('direction is up, down, left or right');
  await cdp(c, 'Input.dispatchMouseEvent', { type: 'mouseWheel', x: t.x, y: t.y, deltaX: d[0], deltaY: d[1] });
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
