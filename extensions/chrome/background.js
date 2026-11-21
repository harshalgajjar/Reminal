// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// reminal's extension: agents in reminal sessions on this computer use this
// browser, once the person allows it.
//
//   reminal (native messaging host, `reminal browser-host`)
//     -> {t:"req", id, session, run, name, machine, tool, args}
//     <- {id, ok, result | error}
//     -> {t:"ended", session}      the session is gone: its tab group goes
//
// Installing the extension (`reminal integrate`, then loading it) is the
// person's consent: no prompt. A session's first use is shown (its tab group,
// the page's border and label, a notification with Stop), and the switch in
// the extension's button turns all of it off. Each session (one run of it:
// its id and when its process started) works only in its own tab group, and
// can be stopped from the button or the notification (Stop holds for that
// run). Stop and the switch win over an action under way: an epoch, checked
// right before each debugger call.
//
// Everything a session needs survives the service worker going to sleep: its
// group and tabs are kept in chrome.storage.session (a group the browser
// dropped is made again from its tabs), the debugger is attached again when
// needed, and an alarm reconnects to reminal.
//
// Typing into password or payment fields is refused, in any frame; an action
// inside a sign-in, or with a CAPTCHA showing, is paused for the person. Input
// and screenshots go through chrome.debugger (the browser shows its own "is
// debugging this browser" bar); pages are read by scripts in the extension's
// isolated world.

import * as page from './page.js';

const HOST = 'app.reminal.browser';

const COLORS = ['blue', 'green', 'purple', 'cyan', 'orange', 'pink', 'red', 'yellow', 'grey'];
const STOPPED = 'The person stopped browser use for this session.';
const OFF = 'The person switched off agents\' use of this browser (the reminal button in the browser\'s toolbar). Ask them to switch it on if they want you to use it.';

// ---------------------------------------------------------------- the host
let port = null, backoff = 1000, retrying = 0;
function connect() {
  if (port) return;
  clearTimeout(retrying);
  try { port = chrome.runtime.connectNative(HOST); } catch (e) { port = null; retry(); return; }
  port.onMessage.addListener(onHostMessage);
  port.onDisconnect.addListener(() => {
    const why = chrome.runtime.lastError && chrome.runtime.lastError.message;
    port = null;
    setStatus({ connected: false, error: why || 'reminal is not running' });
    retry();
  });
}
function retry() { clearTimeout(retrying); retrying = setTimeout(connect, backoff); backoff = Math.min(backoff * 2, 30_000); }
function send(m) { try { port && port.postMessage(m); } catch (_) {} }
async function setStatus(s) { await chrome.storage.session.set({ host: { ...s, at: Date.now() } }); }

// reminal says, as it connects, the version of the extension it carries (its
// files' own, see Version in embed.go). The browser goes on running the
// extension it loaded when reminal writes newer files, across its own
// restart too: this one then reloads itself from them, once per version (if
// it still differs after that, it was loaded from another folder: said in
// the button's panel, not tried again).
async function newerFiles(ext) {
  const mine = chrome.runtime.getManifest().version;
  if (ext === mine) { chrome.storage.local.remove('reloadedFor'); return; }
  const { reloadedFor } = await chrome.storage.local.get('reloadedFor');
  if (reloadedFor === ext) {
    setStatus({ connected: true, error: `reminal has a newer extension (${ext}) than the one loaded here (${mine}): reload it on the extensions page` });
    return;
  }
  await chrome.storage.local.set({ reloadedFor: ext });
  console.log('[reminal] reloading: reminal carries', ext, 'this is', mine);
  chrome.runtime.reload();
}

async function onHostMessage(m) {
  if (!m || typeof m !== 'object') return;
  if (m.t === 'status') {
    if (m.ok) backoff = 1000;
    await setStatus({ connected: !!m.ok, error: m.error || '' });
    if (m.ok && m.ext) await newerFiles(String(m.ext));
    // The sessions this browser holds tabs for: reminal says which of them
    // ended while it was not looking (it was being updated, or this browser
    // was closed), and their groups are let go.
    if (m.ok) send({ t: 'have', sessions: [...new Set([...(await allS()).map(([, s]) => s.session).filter(Boolean), ...(await groupsOfSessions()).keys()])] });
    return;
  }
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

// Connected on every wake: the browser starting, the extension installed,
// the worker started again, and an alarm every 30 s (a worker that went to
// sleep while reminal was away comes back to it).
chrome.runtime.onStartup.addListener(connect);
chrome.runtime.onInstalled.addListener(connect);
chrome.alarms.create('reminal-connect', { periodInMinutes: 0.5 });
chrome.alarms.onAlarm.addListener((a) => { if (a.name === 'reminal-connect') connect(); });
connect();

// ---------------------------------------------------------------- sessions
// One storage key per run of a session: "s:<id>:<run>" ->
// {session, run, name, machine, groupId, tabs, lastTab, stopped}. Each read
// is fresh; a write merges into what is stored now.
const keyOf = (m) => `s:${m.session}:${m.run || ''}`;
async function getS(k) { return (await chrome.storage.session.get(k))[k] || null; }
async function putS(k, patch) {
  const cur = (await getS(k)) || {};
  const next = { ...cur, ...patch };
  await chrome.storage.session.set({ [k]: next });
  return next;
}
async function allS() {
  const all = await chrome.storage.session.get(null);
  return Object.entries(all).filter(([k]) => k.startsWith('s:'));
}

// The person's answer for this browser profile.
async function allowedHere() { return (await chrome.storage.local.get('agents')).agents !== 'off'; }

// Epochs: one for every session (bumped by the switch going off), and one a
// run (bumped by its Stop and its end); an action checks both before each
// debugger call.
let allEpoch = 0;
const epochs = new Map();
const epochOf = (k) => epochs.get(k) || 0;
const bump = (k) => epochs.set(k, epochOf(k) + 1);

// let go of a run's tabs: the debugger detached, its group undone (the tabs
// stay open: the person may want what is in them).
async function letGo(s, why) {
  console.log("[reminal] stopped", s && `s:${s.session}:${s.run || ""}`, why);
  for (const tab of await sessionTabs(s, false)) {
    await detach(tab.id);
    try { await chrome.tabs.ungroup(tab.id); } catch (_) {}
  }
}
// Stopped runs are kept in the profile's own storage too: the browser empties
// the session's storage when it restarts, while the run goes on, and a Stop
// holds for the run. Dropped when the session ends; the oldest go past
// STOPPED_KEPT (a session that ended while the browser was closed).
const STOPPED_KEPT = 200;
async function stoppedRuns() { return (await chrome.storage.local.get('stopped')).stopped || {}; }
async function keepStopped(k) {
  const all = await stoppedRuns();
  all[k] = Date.now();
  const keys = Object.keys(all).sort((a, b) => all[b] - all[a]);
  for (const old of keys.slice(STOPPED_KEPT)) delete all[old];
  await chrome.storage.local.set({ stopped: all });
}
async function isStopped(k, s) { return !!(s && s.stopped) || k in (await stoppedRuns()); }
// stop: the person's Stop, for this run of the session.
async function stop(k, why = "stopped by the person") {
  bump(k);
  await keepStopped(k);
  const s = await putS(k, { stopped: true });
  await letGo(s, why);
}
// endSession: the session is gone; its runs' records and groups go too.
async function endSession(id) {
  const kept = await stoppedRuns();
  const mine = Object.keys(kept).filter((k) => k.startsWith(`s:${id}:`));
  if (mine.length) { for (const k of mine) delete kept[k]; await chrome.storage.local.set({ stopped: kept }); }
  for (const [k, s] of await allS()) {
    if (!k.startsWith(`s:${id}:`)) continue;
    bump(k);
    await chrome.storage.session.remove(k);
    await letGo(s, "the session ended");
  }
  // And a group under its name that no record holds any more (the records
  // went when the extension reloaded itself): its tabs stay, ungrouped.
  for (const g of (await groupsOfSessions()).get(id) || []) {
    for (const t of await chrome.tabs.query({ groupId: g }).catch(() => [])) {
      await detach(t.id);
      try { await chrome.tabs.ungroup(t.id); } catch (_) {}
    }
  }
}
// groupsOfSessions: the tab groups in the browser named as a session's are
// ("<id> - reminal"), by session id.
async function groupsOfSessions() {
  const out = new Map();
  for (const g of await chrome.tabGroups.query({}).catch(() => [])) {
    const m = /^([A-Z0-9]{4,32}) - reminal$/.exec(g.title || '');
    if (m) out.set(m[1], [...(out.get(m[1]) || []), g.id]);
  }
  return out;
}
// switchOff: the switch in the button goes off; every session lets go.
async function switchOff() {
  allEpoch++;
  await chrome.storage.local.set({ agents: 'off' });
  for (const [, s] of await allS()) for (const tab of await sessionTabs(s, false)) await detach(tab.id);
}

// ---------------------------------------------------------------- consent
// Installing reminal's extension (`reminal integrate`, then loading it) is
// the person's consent: agents in reminal sessions on this computer may use
// this browser, with no prompt. The switch in the extension's button turns it
// off (every request refused, saying so). A session's first use is shown, not
// asked: its tab group, the page's border and label, and a notification with
// a Stop button (Stop holds for that run of the session).
async function consent() {
  if (!(await allowedHere())) throw new Error(OFF);
}

// announce: one notification the first time a run of a session uses the
// browser, with Stop.
async function announce(k, s, name) {
  if (s && s.noticed) return;
  await putS(k, { noticed: true });
  chrome.notifications.create('use:' + k, {
    type: 'basic', iconUrl: 'icons/128.png', title: 'reminal',
    message: `"${name}", a reminal session on this computer, is using this browser.`,
    buttons: [{ title: 'Stop' }],
  });
}
chrome.notifications.onButtonClicked.addListener((id) => {
  if (id.startsWith('use:')) stop(id.slice(4)).then(() => chrome.notifications.clear(id));
});

chrome.runtime.onMessage.addListener((msg, sender, reply) => {
  // Only the extension's own pages (popup.html) speak here.
  if (sender.id !== chrome.runtime.id || !sender.url || !sender.url.startsWith(chrome.runtime.getURL(''))) return;
  if (msg.t === 'stop') stop(String(msg.key || '')).then(() => reply(true));
  else if (msg.t === 'switch') (msg.on ? chrome.storage.local.set({ agents: 'on' }) : switchOff()).then(() => reply(true));
  else if (msg.t === 'reconnect') { backoff = 1000; connect(); reply(true); }
  else return;
  return true;
});

// ---------------------------------------------------------------- the tools
// A request's own view of its session: still allowed, not stopped, under the
// epochs it began with. Checked before each step that touches a page.
function guardFor(k) {
  const epoch = epochOf(k), all = allEpoch;
  return async () => {
    const s = await getS(k);
    if (!(await allowedHere()) || allEpoch !== all) throw new Error(OFF);
    if (!s || epochOf(k) !== epoch || await isStopped(k, s)) throw new Error(STOPPED);
    return s;
  };
}

async function handle(m) {
  await consent(m);
  const k = keyOf(m);
  const name = m.name || m.session;
  let s = await getS(k);
  if (await isStopped(k, s)) throw new Error(STOPPED);
  const before = s;
  s = await putS(k, { session: m.session, run: m.run || "", name, machine: m.machine || "" });
  await announce(k, before, name);
  const alive = guardFor(k);
  await alive();
  const a = m.args || {};
  switch (m.tool) {
    case 'tabs_context': return tabsContext(k);
    case 'tabs_create': return tabsCreate(k, alive, name, a.url);
    case 'tabs_close': return tabsClose(k, a.tabId);
    case 'batch': return batch(m, a);
  }
  const tabId = await sessionTab(k, a.tabId);
  const c = { tabId, alive, name };
  // A dialog of the page's (alert, confirm, prompt) holds the page still:
  // it is answered first.
  if (m.tool === 'handle_dialog') return handleDialog(c, a);
  if (dialogs.has(tabId)) throw new Error(dialogSays(tabId));
  if (m.tool === 'tabs_select') return tabsSelect(k, c);
  await indicate(tabId, name, true);
  const was = ACTS.has(m.tool) ? (await chrome.tabs.get(tabId)).url : null;
  const began = Date.now();
  try {
    const out = await (async () => {
    switch (m.tool) {
      case 'navigate': return await navigate(c, String(a.url ?? ''));
      case 'screenshot': return await screenshot(c, a);
      case 'read_page': return await inPage(c, page.readTree, a.filter === 'interactive');
      case 'get_page_text': return await inPage(c, page.pageText);
      case 'click': return await click(c, a);
      case 'type': return await type(c, a);
      case 'key': return await key(c, String(a.key || ''));
      case 'scroll': return await scroll(c, a);
      case 'hover': return await hover(c, a);
      case 'drag': return await drag(c, a);
      case 'select_option': return await selectOption(c, a);
      case 'fill_form': return await fillForm(c, a);
      case 'wait_for': return await waitFor(c, a);
      case 'find': return await find(c, String(a.query || ''));
      case 'resize': return await resize(c, a);
      case 'console_messages': return await consoleMessages(c, a);
      case 'network_requests': return await networkRequests(c, a);
      case 'evaluate': return await evaluate(c, a);
      case 'file_upload': return await fileUpload(c, a);
      case 'downloads': return await downloads(c, a);
      case 'pdf_save': return await pdfSave(c, a);
      case 'cookies': return await cookies(c, a);
      case 'storage': return await storage(c, a);
      case 'emulate': return await emulate(c, a);
    }
    throw new Error('unknown tool: ' + m.tool);
    })();
    // An action that took the tab somewhere else says where it is now, so
    // the agent need not ask.
    // (If the action set a page loading, that is waited for, as
    // browser_navigate waits.)
    if (was != null && typeof out === 'string') {
      // The browser says at once when an action starts a page loading
      // (navAt, from the debugger's events): no fixed wait to find out.
      await new Promise((r) => setTimeout(r, 6));
      let t = await chrome.tabs.get(tabId).catch(() => null);
      if (t && ((navAt.get(tabId) || 0) >= began || t.status === 'loading' || t.pendingUrl)) {
        await Promise.race([loaded(tabId), new Promise((r) => setTimeout(r, 10_000))]);
        t = await chrome.tabs.get(tabId).catch(() => null);
      }
      if (t && t.url && t.url !== was) return `${out}\nThe tab is now at ${JSON.stringify(t.url.slice(0, 300))}`;
    }
    return out;
  } finally {
    indicate(tabId, name, false);
  }
}

// ACTS are the tools that act on a page (a click can take it elsewhere).
const ACTS = new Set(['click', 'type', 'key', 'select_option', 'fill_form', 'drag', 'handle_dialog']);

// sessionTabs: the tabs of a run, by the ids it keeps (still open), in its
// group. regroup: when the group itself is gone (the browser dropped it),
// it is made again from the run's tabs. While the group is there, a tab
// outside it was taken out by the person: it is theirs again, and dropped
// from the run's.
async function sessionTabs(s, regroup = true) {
  if (!s) return [];
  const ids = new Set(s.tabs || []);
  let group = s.groupId;
  try { if (group != null) await chrome.tabGroups.get(group); } catch (_) { group = null; }
  // A run with no group of its own takes up the one the browser still has
  // under the session's name: the extension forgets its records when it
  // reloads itself for an update, and the group and its tabs are still there.
  if (group == null && regroup) {
    const named = (await groupsOfSessions()).get(s.session);
    if (named && named.length) group = named[0];
  }
  const inGroup = group == null ? [] : await chrome.tabs.query({ groupId: group }).catch(() => []);
  for (const t of inGroup) ids.add(t.id);
  let tabs = [];
  for (const id of ids) { try { tabs.push(await chrome.tabs.get(id)); } catch (_) {} }
  if (regroup && tabs.length) {
    if (group != null) {
      for (const t of tabs.filter((t) => t.groupId !== group)) await detach(t.id);
      tabs = tabs.filter((t) => t.groupId === group);
    } else {
      group = await chrome.tabs.group({ tabIds: tabs.map((t) => t.id) });
      await chrome.tabGroups.update(group, { title: `${s.session} - reminal`, color: s.color || 'blue' }).catch(() => {});
    }
    const key = `s:${s.session}:${s.run || ''}`;
    await putS(key, { groupId: group, tabs: tabs.map((t) => t.id) });
  }
  return tabs;
}

// noTabs: what a session with no tab is told: it never opened one, or the
// ones it opened are closed now (by the person, or the browser restarting).
const noTabs = (s) => (s && s.opened
  ? 'The tabs this session opened are closed now: open a new one with browser_tabs_create.'
  : 'This session has no tabs yet: open one with browser_tabs_create.');

async function tabsContext(k) {
  const s = await getS(k);
  const tabs = await sessionTabs(s);
  if (!tabs.length) return noTabs(s);
  return JSON.stringify(tabs.map((t) => ({ tabId: t.id, title: t.title, url: t.url, active: t.active, last: t.id === s.lastTab })));
}

async function tabsCreate(k, alive, name, url) {
  const href = url ? allowedURL(url) : 'about:blank';
  let s = await alive();
  await sessionTabs(s); // its group as it should be, first
  s = await getS(k);
  const tab = await chrome.tabs.create({ url: href, active: true });
  try {
    s = await alive();
  } catch (e) {
    chrome.tabs.remove(tab.id).catch(() => {});
    throw e;
  }
  let group = s.groupId;
  try { if (group != null) await chrome.tabGroups.get(group); } catch (_) { group = null; }
  const color = s.color || COLORS[(await allS()).length % COLORS.length];
  if (group == null) {
    group = await chrome.tabs.group({ tabIds: [tab.id] });
    await chrome.tabGroups.update(group, { title: `${s.session} - reminal`, color });
  } else {
    await chrome.tabs.group({ tabIds: [tab.id], groupId: group });
  }
  await putS(k, { groupId: group, color, opened: true, lastTab: tab.id, tabs: [...new Set([...(s.tabs || []), tab.id])] });
  if (url) await loaded(tab.id);
  return JSON.stringify({ tabId: tab.id, url: href });
}

// sessionTab: the tab asked for, which must be one of the session's; or the
// one it used last; or its first.
async function sessionTab(k, want) {
  const s = await getS(k);
  const tabs = await sessionTabs(s);
  if (!tabs.length) throw new Error(noTabs(s));
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
  else if (url === 'reload') await chrome.tabs.reload(c.tabId);
  else await chrome.tabs.update(c.tabId, { url: allowedURL(url) });
  await loaded(c.tabId);
  const t = await chrome.tabs.get(c.tabId);
  return JSON.stringify({ tabId: c.tabId, title: t.title, url: t.url });
}

// settled: after the browser says a page has loaded, the page is given until
// it has drawn a frame: that is when it puts its focus where it means to (an
// autofocus field), and a key pressed before that would be judged against
// the wrong element. Asked of the page itself, so it takes as long as the
// page takes and no longer (a tab not showing draws nothing and moves no
// focus: no wait).
async function settled(tabId) {
  try {
    await chrome.scripting.executeScript({ target: { tabId }, func: page.drawn });
  } catch (_) {
    await new Promise((r) => setTimeout(r, 20));
  }
}
function loaded(tabId) {
  return new Promise((resolve) => {
    let done = false;
    const finish = () => { if (done) return; done = true; chrome.tabs.onUpdated.removeListener(on); clearTimeout(t); settled(tabId).then(resolve); };
    const on = (id, info) => { if (id === tabId && info.status === 'complete') finish(); };
    const t = setTimeout(finish, 30_000);
    chrome.tabs.onUpdated.addListener(on);
    chrome.tabs.get(tabId).then((tab) => { if (tab.status === 'complete' && !tab.pendingUrl) setTimeout(() => chrome.tabs.get(tabId).then((x) => { if (x.status === 'complete') finish(); }), 300); }).catch(finish);
  });
}

// ---------------------------------------------------------------- the debugger
// Attached when needed, and again after the worker slept (its memory of what
// it attached is gone, the attachment may not be): "already attached" is
// attached.
const attached = new Set();
async function cdp(c, method, params = {}) {
  await c.alive();
  if (canceled.has(c.tabId)) throw new Error(STOPPED);
  if (!attached.has(c.tabId)) await attach(c.tabId);
  try {
    return await sendOrDialog(c.tabId, method, params);
  } catch (e) {
    // Detached under us (the worker's sleep): once more. Not after Cancel in
    // the browser's own "started debugging" bar: that is the person's Stop.
    if (!/not attached|detached/i.test(String(e && e.message))) throw e;
    attached.delete(c.tabId);
    if (canceled.has(c.tabId)) throw new Error(STOPPED);
    await c.alive();
    await attach(c.tabId);
    return sendOrDialog(c.tabId, method, params);
  }
}
// attach: the debugger on a tab, with what it tells of the page switched on:
// its dialogs, its console and its requests (kept while attached, see
// "what the page says" below).
async function attach(tabId) {
  try {
    await chrome.debugger.attach({ tabId }, '1.3');
  } catch (e) {
    if (!/already attached/i.test(String(e && e.message))) throw e;
  }
  attached.add(tabId);
  for (const d of ['Page', 'Runtime', 'Log', 'Network']) await chrome.debugger.sendCommand({ tabId }, d + '.enable', {}).catch(() => {});
}
// sendOrDialog: a command, given up when the page opens a dialog under it
// (a click whose handler calls alert() does not come back until the dialog
// is answered): the agent is told of the dialog instead.
function sendOrDialog(tabId, method, params) {
  if (method === 'Page.handleJavaScriptDialog') return chrome.debugger.sendCommand({ tabId }, method, params);
  const sent = chrome.debugger.sendCommand({ tabId }, method, params);
  let waiter;
  const opened = new Promise((_, reject) => {
    waiter = () => reject(new Error(dialogSays(tabId)));
    if (!dialogWaiters.has(tabId)) dialogWaiters.set(tabId, new Set());
    dialogWaiters.get(tabId).add(waiter);
  });
  sent.catch(() => {});
  return Promise.race([sent, opened]).finally(() => { const w = dialogWaiters.get(tabId); if (w) w.delete(waiter); });
}
async function detach(tabId) { attached.delete(tabId); try { await chrome.debugger.detach({ tabId }); } catch (_) {} }
// Cancel in the browser's "reminal started debugging this browser" bar is
// the person saying stop, in the one control the browser itself offers: the
// run whose tab it was is stopped, as by its Stop. (The bar detaches every
// tab, so every run using the browser then.) canceled holds the tab at once,
// before the run's record is written.
const canceled = new Set();
chrome.debugger.onDetach.addListener((src, reason) => {
  attached.delete(src.tabId);
  if (reason !== 'canceled_by_user' || src.tabId == null) return;
  canceled.add(src.tabId);
  allS().then(async (all) => {
    for (const [k, s] of all) if ((s.tabs || []).includes(src.tabId) && !s.stopped) await stop(k, "the person pressed Cancel in the browser's debugging bar");
  });
});
chrome.tabs.onRemoved.addListener((tabId) => { attached.delete(tabId); canceled.delete(tabId); forget(tabId); });

const FULL_PAGE_MAX = 12000;
async function screenshot(c, a = {}) {
  // JPEG when asked: smaller, and quicker for the browser to make.
  const jpeg = String(a.format || '').toLowerCase() === 'jpeg' || String(a.format || '').toLowerCase() === 'jpg';
  const fmt = jpeg ? { format: 'jpeg', quality: 80 } : { format: 'png' };
  const mimeType = jpeg ? 'image/jpeg' : 'image/png';
  const m = await cdp(c, 'Page.getLayoutMetrics');
  const vp = m.cssVisualViewport || m.visualViewport;
  const w = Math.round(vp.clientWidth), h = Math.round(vp.clientHeight);
  if (a.ref) {
    // One element, as it is drawn.
    const r = await inPage(c, page.rect, a.ref);
    if (r.error) throw new Error(r.error);
    const shot = await cdp(c, 'Page.captureScreenshot', { ...fmt, clip: { x: vp.pageX + r.x, y: vp.pageY + r.y, width: Math.max(1, r.w), height: Math.max(1, r.h), scale: 1 } });
    return { image: shot.data, mimeType, note: `${r.what}, ${Math.round(r.w)}x${Math.round(r.h)} pixels. Not for click coordinates: those are in a screenshot of the visible part.` };
  }
  if (a.fullPage) {
    // The whole page, top to bottom (cut at FULL_PAGE_MAX pixels high).
    const cs = m.cssContentSize || m.contentSize;
    const fw = Math.round(cs.width), fh = Math.min(Math.round(cs.height), FULL_PAGE_MAX);
    const shot = await cdp(c, 'Page.captureScreenshot', { ...fmt, captureBeyondViewport: true, clip: { x: 0, y: 0, width: fw, height: fh, scale: 1 } });
    return { image: shot.data, mimeType, note: `The whole page, ${fw}x${fh} pixels${fh < Math.round(cs.height) ? ' (cut: the page is ' + Math.round(cs.height) + ' high)' : ''}. Not for click coordinates: those are in a screenshot of the visible part.` };
  }
  // The visible part, at CSS pixels: the coordinates browser_click takes.
  const shot = await cdp(c, 'Page.captureScreenshot', { ...fmt, clip: { x: vp.pageX, y: vp.pageY, width: w, height: h, scale: 1 } });
  return { image: shot.data, mimeType, note: `${w}x${h} pixels; click at x, y in these pixels.` };
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
// frameChecks: every frame's frameCheck, and what it means for the tab's
// kept requests (guard, below): a tab that shows, or has shown, something
// only the person does gives out no response bodies from then.
async function frameChecks(c) {
  const rs = await inFrames(c, page.frameCheck);
  if (rs.some((r) => r.result.personOnly || r.result.ever)) sawPersonOnly(c.tabId);
  return rs;
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
  const frames = await frameChecks(c);
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

const CHOOSER_HOLD = 400;
const chooserHold = new Map();
async function click(c, a) {
  const t = await target(c, a);
  const button = ['left', 'right', 'middle'].includes(a.button) ? a.button : 'left';
  const count = a.double ? 2 : 1;
  // A file chooser this click makes the page ask for is kept for
  // browser_file_upload: no file window opens on the person's screen from an
  // agent's click. Only round the click: the person's own click on an
  // upload button in this tab opens their file window as ever.
  choosers.delete(c.tabId);
  await cdp(c, 'Page.setInterceptFileChooserDialog', { enabled: true }).catch(() => {});
  try {
  await cdp(c, 'Input.dispatchMouseEvent', { type: 'mouseMoved', x: t.x, y: t.y });
  for (let i = 1; i <= count; i++) {
    await cdp(c, 'Input.dispatchMouseEvent', { type: 'mousePressed', x: t.x, y: t.y, button, clickCount: i });
    await cdp(c, 'Input.dispatchMouseEvent', { type: 'mouseReleased', x: t.x, y: t.y, button, clickCount: i });
  }
  // The page's asking comes at once if it comes: looked for briefly, and the
  // holding back kept a little longer (CHOOSER_HOLD) for one that is slow.
  // Waited for where the click was on a file field. Elsewhere on a page
  // with one (an upload button presses a hidden file field), the page is
  // asked a question after the click: its answer comes after the page has
  // run the click, and so after its asking for a file, if it asked.
  if (t.fileField) { for (let i = 0; i < 12 && !choosers.has(c.tabId); i++) await new Promise((r) => setTimeout(r, 5)); }
  else if (t.fileish) { for (let i = 0; i < 3 && !choosers.has(c.tabId); i++) await cdp(c, 'Runtime.evaluate', { expression: '0' }).catch(() => {}); }
  } finally {
    const until = Date.now() + CHOOSER_HOLD;
    chooserHold.set(c.tabId, until);
    setTimeout(() => {
      if (chooserHold.get(c.tabId) !== until) return; // a later click holds it now
      chooserHold.delete(c.tabId);
      chrome.debugger.sendCommand({ tabId: c.tabId }, 'Page.setInterceptFileChooserDialog', { enabled: false }).catch(() => {});
    }, CHOOSER_HOLD);
  }
  const chooser = choosers.has(c.tabId) ? ' The page is asking for a file: give it with browser_file_upload.' : '';
  return `Clicked ${t.what} at ${Math.round(t.x)}, ${Math.round(t.y)}.${chooser}`;
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

// ---------------------------------------------------------------- more of the tabs
async function tabsClose(k, want) {
  const tabId = await sessionTab(k, want);
  await detach(tabId);
  await chrome.tabs.remove(tabId);
  const s = await getS(k);
  await putS(k, { tabs: (s.tabs || []).filter((t) => t !== tabId), lastTab: s.lastTab === tabId ? null : s.lastTab });
  return `Closed tab ${tabId}.`;
}
// tabsSelect: the tab the session's next actions go to, shown in its window
// (the window itself is not brought forward: the person may be elsewhere).
async function tabsSelect(k, c) {
  await c.alive();
  const t = await chrome.tabs.update(c.tabId, { active: true });
  await putS(k, { lastTab: c.tabId });
  return JSON.stringify({ tabId: c.tabId, title: t.title, url: t.url });
}

// ---------------------------------------------------------------- the page's dialogs
// An alert, confirm, prompt or "leave this page?" holds the page still until
// it is answered. Kept by tab while open; every tool but browser_handle_dialog
// says it is there.
const dialogs = new Map();
const dialogWaiters = new Map();
const dialogSays = (tabId) => {
  const d = dialogs.get(tabId) || {};
  return `A dialog is open on the page (${d.type || 'dialog'}: ${JSON.stringify(String(d.message || '').slice(0, 300))}). Answer it with browser_handle_dialog.`;
};
async function handleDialog(c, a) {
  await c.alive();
  const d = dialogs.get(c.tabId);
  if (!d) throw new Error('No dialog is open on this tab.');
  const params = { accept: a.accept !== false };
  if (a.promptText != null) params.promptText = String(a.promptText);
  await cdp(c, 'Page.handleJavaScriptDialog', params);
  dialogs.delete(c.tabId);
  return `${params.accept ? 'Accepted' : 'Dismissed'} the ${d.type || 'dialog'} ${JSON.stringify(String(d.message || '').slice(0, 300))}.`;
}

// ---------------------------------------------------------------- what the page says
// Its console and its requests, kept by tab while the debugger is attached
// (from the session's first action on the tab), the newest KEPT of each.
const KEPT = 500;
const logs = new Map(), reqs = new Map(), drags = new Map(), choosers = new Map(), dls = new Map(), guards = new Map(), navAt = new Map(), mainFrame = new Map();
// guard: which of a tab's kept requests never give out their response's
// body. A tab that has shown a sign-in or a payment field (seen by a tool's
// check, by the check made as each page loads, or by the watch rules.js
// keeps in the page for one that comes and goes) gives out none for the
// documents it had then (their loaders), nor for any it loads until it has
// gone to a new page: a single-page app's sign-in form is gone once the
// person is in, and the token it was answered with is still kept.
const guardOf = (tabId) => { if (!guards.has(tabId)) guards.set(tabId, { shown: false, live: new Set(), blocked: new Set() }); return guards.get(tabId); };
function sawPersonOnly(tabId) {
  const g = guardOf(tabId);
  g.shown = true;
  for (const l of g.live) g.blocked.add(l);
}
// loadedCheck: as a page finishes loading in an attached tab, the rules go
// into its frames (their watch starts) and it is looked at once.
async function loadedCheck(tabId) {
  try {
    await chrome.scripting.executeScript({ target: { tabId, allFrames: true }, files: ['rules.js'] });
    const rs = await chrome.scripting.executeScript({ target: { tabId, allFrames: true }, func: page.frameCheck });
    if (rs.some((r) => r && r.result && (r.result.personOnly || r.result.ever))) sawPersonOnly(tabId);
  } catch (_) {}
}
const kept = (map, tabId) => { if (!map.has(tabId)) map.set(tabId, []); return map.get(tabId); };
const push = (map, tabId, v) => { const l = kept(map, tabId); l.push(v); if (l.length > KEPT) l.splice(0, l.length - KEPT); };
function forget(tabId) { for (const m of [logs, reqs, drags, dialogs, dialogWaiters, choosers, dls, guards, navAt, mainFrame]) m.delete(tabId); }
const argText = (o) => (o.value !== undefined ? (typeof o.value === 'string' ? o.value : JSON.stringify(o.value)) : (o.description || o.unserializableValue || o.type || ''));
chrome.debugger.onEvent.addListener((src, method, p) => {
  const tabId = src.tabId;
  if (tabId == null) return;
  switch (method) {
    case 'Page.javascriptDialogOpening':
      dialogs.set(tabId, { type: p.type, message: p.message });
      for (const w of dialogWaiters.get(tabId) || []) w();
      break;
    case 'Page.javascriptDialogClosed': dialogs.delete(tabId); break;
    case 'Runtime.consoleAPICalled':
      push(logs, tabId, { level: p.type === 'warning' ? 'warning' : p.type, text: (p.args || []).map(argText).join(' ').slice(0, 2000) });
      break;
    case 'Runtime.exceptionThrown': {
      const d = p.exceptionDetails || {};
      push(logs, tabId, { level: 'error', text: ((d.exception && d.exception.description) || d.text || 'Uncaught').slice(0, 2000) });
      break;
    }
    case 'Log.entryAdded':
      push(logs, tabId, { level: p.entry.level, text: `${p.entry.text}${p.entry.url ? ' (' + p.entry.url + ')' : ''}`.slice(0, 2000) });
      break;
    case 'Network.requestWillBeSent': {
      const g = guardOf(tabId);
      g.live.add(p.loaderId);
      if (g.shown) g.blocked.add(p.loaderId);
      push(reqs, tabId, { id: p.requestId, method: p.request.method, url: p.request.url, type: p.type || '', loader: p.loaderId });
      break;
    }
    // The tab's own page starting to load something (a link clicked, a form
    // sent): when, for the action that did it to wait for.
    case 'Page.frameRequestedNavigation':
    case 'Page.frameStartedLoading':
      if (!mainFrame.has(tabId) || mainFrame.get(tabId) === p.frameId) navAt.set(tabId, Date.now());
      break;
    case 'Page.frameNavigated':
      if (!p.frame.parentId) mainFrame.set(tabId, p.frame.id);
      // The tab went to a new page: the page it left keeps its bodies shut
      // (a sign-in form there may have come and gone with no tool looking:
      // only that page knew), and what the new one loads is judged anew.
      // Bodies are only ever given for requests of the page now showing.
      if (!p.frame.parentId) {
        const g = guardOf(tabId);
        for (const l of g.live) g.blocked.add(l);
        g.shown = false;
        g.live = new Set([p.frame.loaderId]);
        g.blocked.delete(p.frame.loaderId);
      }
      break;
    case 'Page.loadEventFired': loadedCheck(tabId); break;
    case 'Network.responseReceived': {
      const r = kept(reqs, tabId).findLast((x) => x.id === p.requestId);
      if (r) { r.status = p.response.status; r.mime = p.response.mimeType; }
      break;
    }
    case 'Network.loadingFailed': {
      const r = kept(reqs, tabId).findLast((x) => x.id === p.requestId);
      if (r) r.failed = p.errorText || 'failed';
      break;
    }
    case 'Input.dragIntercepted': drags.set(tabId, p.data); break;
    case 'Page.fileChooserOpened': choosers.set(tabId, { backendNodeId: p.backendNodeId, multiple: p.mode === 'selectMultiple' }); break;
    case 'Page.downloadWillBegin': push(dls, tabId, { guid: p.guid, url: p.url, name: p.suggestedFilename, state: 'inProgress', at: Date.now() }); break;
    case 'Page.downloadProgress': {
      const d = kept(dls, tabId).findLast((x) => x.guid === p.guid);
      if (d) { d.state = p.state; d.received = p.receivedBytes; d.total = p.totalBytes; }
      break;
    }
  }
});

const LEVELS = ['error', 'warning', 'info', 'log', 'debug'];
async function consoleMessages(c, a) {
  await cdp(c, 'Runtime.enable'); // attached, and listening
  let l = kept(logs, c.tabId);
  if (a.level) {
    const upTo = LEVELS.indexOf(String(a.level));
    if (upTo < 0) throw new Error('level is one of: ' + LEVELS.join(', '));
    l = l.filter((m) => { const i = LEVELS.indexOf(m.level); return i >= 0 && i <= upTo; });
  }
  if (a.pattern) { const re = regexOf(a.pattern); l = l.filter((m) => re.test(m.text)); }
  const out = l.slice(-Math.max(1, Math.min(KEPT, Number(a.limit) || 100))).map((m) => `[${m.level}] ${m.text}`);
  if (a.clear) logs.set(c.tabId, []);
  return out.length ? out.join('\n') : 'No console messages (they are kept from this session\'s first action on the tab).';
}
function regexOf(s) { try { return new RegExp(String(s), 'i'); } catch (e) { throw new Error('pattern is not a regular expression: ' + e.message); } }

// The body of a response is the page's own data, as browser_get_page_text's
// is. Request headers and bodies are never handed out: they carry the
// person's cookies, tokens and typed passwords.
const BODY_MAX = 20000;
async function networkRequests(c, a) {
  await cdp(c, 'Network.enable');
  let l = kept(reqs, c.tabId);
  if (a.requestId != null) {
    const r = l.findLast((x) => x.id === String(a.requestId));
    if (!r) throw new Error(`No request ${a.requestId} is kept for this tab.`);
    let body = '';
    // Not on a page with a sign-in or a payment field: what comes back there
    // (a session, a token) is the person's.
    const frames = await frameChecks(c);
    const head = `${r.method} ${r.url}\nstatus: ${r.status ?? (r.failed || 'pending')}\n\n`;
    if (frames.some((f) => f.result.personOnly || f.result.ever)) return head + '(The body is not given: this page has, or had, a sign-in or payment field.)';
    if (guardOf(c.tabId).blocked.has(r.loader)) return head + '(The body is not given: the request is from a page this tab has left, or from one that had a sign-in or payment field. Bodies are given for the page now showing.)';
    try {
      const b = await cdp(c, 'Network.getResponseBody', { requestId: r.id });
      body = b.base64Encoded ? `(${Math.round(b.body.length * 0.75)} bytes, not text)` : (b.body.length > BODY_MAX ? b.body.slice(0, BODY_MAX) + `\n(cut at ${BODY_MAX} characters)` : b.body);
    } catch (e) { body = `(no body kept: ${String((e && e.message) || e).slice(0, 120)})`; }
    return `${r.method} ${r.url}\nstatus: ${r.failed ? r.failed : r.status ?? 'pending'}${r.mime ? '\ntype: ' + r.mime : ''}\n\n${body}`;
  }
  if (a.pattern) { const re = regexOf(a.pattern); l = l.filter((r) => re.test(r.url)); }
  if (a.failed) l = l.filter((r) => r.failed || r.status >= 400);
  const out = l.slice(-Math.max(1, Math.min(KEPT, Number(a.limit) || 100))).map((r) => `${r.id} ${r.method} ${r.status ?? (r.failed || 'pending')}${r.status != null && r.failed ? ' ' + r.failed : ''} ${r.url.slice(0, 300)}${r.type ? ' (' + r.type + ')' : ''}`);
  if (a.clear) reqs.set(c.tabId, []);
  return out.length ? out.join('\n') : 'No requests (they are kept from this session\'s first action on the tab).';
}

// What an agent may do only once the person has switched it on, each with
// its own switch in the extension's button, off as installed: these reach
// past what a person sees an agent do on a page.
const SWITCHES = {
  scripts: 'Running scripts in pages is off in this browser. It is off unless the person switches it on (the reminal button in the browser\'s toolbar): a script can act as them on any site they are signed in to. Ask them if they want it on.',
  uploads: 'Giving files from this computer to pages is off in this browser. It is off unless the person switches it on (the reminal button in the browser\'s toolbar). Ask them if they want it on, or to upload the file themselves.',
  cookies: 'Reading and changing cookies and site storage is off in this browser. It is off unless the person switches it on (the reminal button in the browser\'s toolbar): those hold their sign-ins. Ask them if they want it on.',
};
async function switchedOn(name) {
  if ((await chrome.storage.local.get(name))[name] !== 'on') throw new Error(SWITCHES[name]);
}
// note: a line in the button's panel for what the person should be able to
// see afterwards (a script ran, a file was given), the newest NOTES.
const NOTES = 30;
async function note(c, text) {
  let site = '';
  try { site = new URL((await chrome.tabs.get(c.tabId)).url).host; } catch (_) {}
  const { notes = [] } = await chrome.storage.session.get('notes');
  notes.push({ at: Date.now(), who: c.name || '', text: site ? `${text} on ${site}` : text });
  await chrome.storage.session.set({ notes: notes.slice(-NOTES) });
}

// evaluate: an expression, or a function's result, run in the page as the
// page's own scripts are. A script can do there what the rules keep an
// agent's clicks and typing from, and can act as the person on a site they
// are signed in to: so it is off as installed (its own switch), never run on
// a page with a sign-in, a payment field or a CAPTCHA in any frame, and
// each run is noted in the button's panel.
async function evaluate(c, a) {
  await switchedOn('scripts');
  const code = String(a.expression ?? a.function ?? '');
  if (!code.trim()) throw new Error('expression is required');
  if (code.length > 50000) throw new Error('expression is too long (at most 50000 characters)');
  const frames = await frameChecks(c);
  if (frames.some((r) => r.result.captcha)) throw new Error('Paused: a CAPTCHA is showing on this page. Ask the person to solve it, then continue.');
  if (frames.some((r) => r.result.personOnly)) throw new Error('Paused: this page has a sign-in or payment field, so scripts are not run in it. Ask the person to do that part, then continue.');
  // A function ("() => ...", "async function ...") is called; anything else is an expression.
  const expr = /^\s*(async\s+)?(\(|function\b|[A-Za-z_$][\w$]*\s*=>)/.test(code) ? `(${code})()` : code;
  await note(c, 'ran a script');
  const r = await cdp(c, 'Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true, userGesture: false, timeout: 30000 });
  if (r.exceptionDetails) {
    const d = r.exceptionDetails;
    throw new Error('The script threw: ' + ((d.exception && d.exception.description) || d.text || 'an error').slice(0, 2000));
  }
  const v = r.result || {};
  let out = v.value !== undefined ? (typeof v.value === 'string' ? v.value : JSON.stringify(v.value, null, 2)) : (v.description || v.type || 'undefined');
  if (out === undefined) out = 'undefined';
  return out.length > 100000 ? out.slice(0, 100000) + '\n(cut at 100000 characters)' : out;
}

// ---------------------------------------------------------------- more actions
async function hover(c, a) {
  const t = await target(c, a);
  await cdp(c, 'Input.dispatchMouseEvent', { type: 'mouseMoved', x: t.x, y: t.y });
  return `Moved the mouse over ${t.what} at ${Math.round(t.x)}, ${Math.round(t.y)}.`;
}

// drag: the mouse pressed on one element or point, moved to another in
// steps, and let go. A page that drags with the browser's own drag and drop
// (draggable elements) is handed the drop too.
async function drag(c, a) {
  const from = await target(c, { ref: a.startRef, x: a.startX, y: a.startY });
  await cdp(c, 'Input.setInterceptDrags', { enabled: true }).catch(() => {});
  drags.delete(c.tabId);
  try {
    await cdp(c, 'Input.dispatchMouseEvent', { type: 'mouseMoved', x: from.x, y: from.y });
    await cdp(c, 'Input.dispatchMouseEvent', { type: 'mousePressed', x: from.x, y: from.y, button: 'left', buttons: 1, clickCount: 1 });
    const to = await target(c, { ref: a.endRef, x: a.endX, y: a.endY });
    const steps = 12;
    for (let i = 1; i <= steps; i++) {
      await cdp(c, 'Input.dispatchMouseEvent', { type: 'mouseMoved', x: from.x + ((to.x - from.x) * i) / steps, y: from.y + ((to.y - from.y) * i) / steps, button: 'left', buttons: 1 });
    }
    await new Promise((r) => setTimeout(r, 60));
    const data = drags.get(c.tabId);
    if (data) {
      for (const type of ['dragEnter', 'dragOver', 'drop']) await cdp(c, 'Input.dispatchDragEvent', { type, x: to.x, y: to.y, data });
    }
    await cdp(c, 'Input.dispatchMouseEvent', { type: 'mouseReleased', x: to.x, y: to.y, button: 'left', clickCount: 1 });
    return `Dragged ${from.what} to ${to.what} (${Math.round(to.x)}, ${Math.round(to.y)}).`;
  } finally {
    drags.delete(c.tabId);
    cdp(c, 'Input.setInterceptDrags', { enabled: false }).catch(() => {});
  }
}

async function selectOption(c, a) {
  const values = Array.isArray(a.values) ? a.values.map(String) : a.value != null ? [String(a.value)] : [];
  if (!a.ref || !values.length) throw new Error('Give the dropdown\'s ref and the values (an option\'s text or value).');
  const frames = await frameChecks(c);
  if (frames.some((r) => r.result.captcha)) throw new Error('Paused: a CAPTCHA is showing on this page. Ask the person to solve it, then continue.');
  const r = await inPage(c, page.selectOption, a.ref, values);
  if (r.error) throw new Error(r.error);
  return r.done;
}

// fillForm: several fields in one call, each as its own tool would do it
// (text typed as a keyboard does, a checkbox clicked, a dropdown chosen).
// Stops at the first that cannot be done, and says which were.
async function fillForm(c, a) {
  const fields = Array.isArray(a.fields) ? a.fields : [];
  if (!fields.length) throw new Error('fields is required: [{ref, value}, ...]');
  if (fields.length > 50) throw new Error('at most 50 fields at a time');
  const done = [];
  const frames = await frameChecks(c);
  if (frames.some((r) => r.result.captcha)) throw new Error('Paused: a CAPTCHA is showing on this page. Ask the person to solve it, then continue.');
  for (const f of fields) {
    try {
      const kind = await inPage(c, page.fieldKind, String(f.ref || ''));
      if (kind.error) throw new Error(kind.error);
      if (kind.secret) throw new Error(kind.secret);
      if (kind.kind === 'select') {
        done.push(await selectOption(c, { ref: f.ref, values: Array.isArray(f.value) ? f.value : [f.value] }));
      } else if (kind.kind === 'check') {
        const want = f.value === true || /^(true|on|yes|checked|1)$/i.test(String(f.value));
        if (kind.checked !== want) await click(c, { ref: f.ref });
        done.push(`${want ? 'Checked' : 'Unchecked'} ${kind.what}.`);
      } else if (kind.kind === 'text') {
        const text = String(f.value ?? '');
        if (text.length > 100000) throw new Error('a value is too long (at most 100000 characters)');
        // The field takes the focus with what is in it selected (held to
        // the rules where it is); a field that will not take it that way
        // is clicked as a person would.
        const took = await inPage(c, page.focusField, String(f.ref));
        if (took.error) throw new Error(took.error);
        if (!took.focused) {
          await click(c, { ref: f.ref });
          const focus = await focusedField(c);
          if (focus.refused) throw new Error(focus.refused);
          if (focus.paused) throw new Error(focus.paused);
          await inPage(c, page.selectAll, String(f.ref));
        }
        if (text) await cdp(c, 'Input.insertText', { text });
        else await cdp(c, 'Input.dispatchKeyEvent', { type: 'rawKeyDown', key: 'Delete', code: 'Delete', windowsVirtualKeyCode: 46, nativeVirtualKeyCode: 46 }).then(() => cdp(c, 'Input.dispatchKeyEvent', { type: 'keyUp', key: 'Delete', code: 'Delete', windowsVirtualKeyCode: 46, nativeVirtualKeyCode: 46 }));
        done.push(`Filled ${kind.what} (${text.length} characters).`);
      } else {
        throw new Error(`${kind.what} is not a field that can be filled.`);
      }
    } catch (e) {
      throw new Error(`${String((e && e.message) || e)} (field ${done.length + 1} of ${fields.length}, ${f.ref}; done before it: ${done.length ? done.join(' ') : 'none'})`);
    }
  }
  return done.join('\n');
}

// waitFor: until text is on the page, or gone from it, or an element (ref)
// can be seen; or just a number of seconds.
async function waitFor(c, a) {
  const secs = Math.max(0, Math.min(60, Number(a.time) || 0));
  if (a.text == null && a.textGone == null && !a.ref) {
    if (!secs) throw new Error('Give text, textGone, ref, or time (seconds).');
    await new Promise((r) => setTimeout(r, secs * 1000));
    await c.alive();
    return `Waited ${secs} s.`;
  }
  const limit = Math.max(0.5, Math.min(60, Number(a.timeout) || 10)) * 1000;
  const t0 = Date.now();
  for (;;) {
    let r;
    try { r = await inPage(c, page.waitCheck, a.text == null ? null : String(a.text), a.textGone == null ? null : String(a.textGone), a.ref ? String(a.ref) : null); } catch (e) { r = { ok: false, why: 'the page is loading' }; }
    if (r.ok) return `${r.why} (after ${((Date.now() - t0) / 1000).toFixed(1)} s).`;
    if (Date.now() - t0 >= limit) throw new Error(`Timed out after ${Math.round(limit / 1000)} s: ${r.why}.`);
    await new Promise((res) => setTimeout(res, 250));
  }
}

// find: the lines of the page's tree that hold every word asked for.
async function find(c, query) {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (!words.length) throw new Error('query is required (words from the element\'s role or name, e.g. "button order").');
  const tree = await inPage(c, page.readTree, false);
  const lines = tree.split('\n').slice(1).filter((l) => { const s = l.toLowerCase(); return words.every((w) => s.includes(w)); }).map((l) => l.trim());
  if (!lines.length) return `Nothing on the page matches "${query}".`;
  return lines.slice(0, 40).join('\n') + (lines.length > 40 ? `\n(${lines.length - 40} more: narrow the words)` : '');
}

// resize: the size the page is laid out at, in this tab only (the person's
// window keeps its size). No width and height: back to the window's own.
async function resize(c, a) {
  const w = Math.round(Number(a.width) || 0), h = Math.round(Number(a.height) || 0);
  if (!w && !h) { await cdp(c, 'Emulation.clearDeviceMetricsOverride'); return 'The page is back at the window\'s own size.'; }
  if (w < 200 || h < 200 || w > 4000 || h > 4000) throw new Error('width and height are between 200 and 4000.');
  await cdp(c, 'Emulation.setDeviceMetricsOverride', { width: w, height: h, deviceScaleFactor: 0, mobile: false });
  return `The page is laid out at ${w}x${h}.`;
}

// ---------------------------------------------------------------- files
// fileUpload: files on this computer given to a file field (by ref), or to
// the file chooser the page just asked for. Which files an agent may give is
// decided where the agent runs (reminal's browser tools check each path).
async function fileUpload(c, a) {
  await switchedOn('uploads');
  const paths = (Array.isArray(a.paths) ? a.paths : []).map(String);
  if (!paths.length) throw new Error('paths is required: the files to give.');
  if (paths.some((p) => !p.startsWith('/'))) throw new Error('paths must be absolute.');
  let node, what = 'the file chooser', multiple;
  if (a.ref) {
    const mark = 'u' + Math.random().toString(36).slice(2);
    const r = await inPage(c, page.markFile, String(a.ref), mark);
    if (r.error) throw new Error(r.error);
    what = r.what; multiple = r.multiple;
    try {
      const doc = await cdp(c, 'DOM.getDocument', { depth: -1, pierce: true });
      const seek = (n) => {
        const at = n.attributes || [];
        for (let i = 0; i + 1 < at.length; i += 2) if (at[i] === 'data-reminal-upload' && at[i + 1] === mark) return n;
        for (const k of [...(n.children || []), ...(n.shadowRoots || []), ...(n.contentDocument ? [n.contentDocument] : [])]) { const hit = seek(k); if (hit) return hit; }
        return null;
      };
      const hit = seek(doc.root);
      if (!hit) throw new Error('The file field could not be reached.');
      node = { nodeId: hit.nodeId };
    } finally {
      await inPage(c, page.unmarkFile, mark).catch(() => {});
    }
  } else {
    const ch = choosers.get(c.tabId);
    if (!ch) throw new Error('Give the file field\'s ref, or click the page\'s upload button first.');
    node = { backendNodeId: ch.backendNodeId }; multiple = ch.multiple;
  }
  if (paths.length > 1 && !multiple) throw new Error(`${what} takes one file.`);
  await cdp(c, 'DOM.setFileInputFiles', { files: paths, ...node });
  choosers.delete(c.tabId);
  await note(c, `gave ${paths.map((p) => p.split('/').pop()).join(', ')}`);
  return `Gave ${paths.length} file${paths.length === 1 ? '' : 's'} to ${what}: ${paths.map((p) => p.split('/').pop()).join(', ')}.`;
}

// downloads: what this tab started downloading since the session's first
// action on it, and where the browser put each (the person's own downloads
// are not listed).
async function downloads(c, a) {
  await cdp(c, 'Page.enable');
  const wait = Math.max(0, Math.min(60, Number(a.wait) || 0)) * 1000;
  const t0 = Date.now();
  while (Date.now() - t0 < wait && (!kept(dls, c.tabId).length || kept(dls, c.tabId).some((d) => d.state === 'inProgress'))) {
    await new Promise((r) => setTimeout(r, 250));
    await c.alive();
  }
  const l = kept(dls, c.tabId);
  if (!l.length) return 'This tab has started no downloads (they are kept from this session\'s first action on the tab).';
  const out = [];
  for (const d of l.slice(-20)) {
    let path = '';
    try {
      const found = await chrome.downloads.search({ url: d.url, orderBy: ['-startTime'], limit: 5 });
      const hit = found.find((x) => Math.abs(new Date(x.startTime).getTime() - d.at) < 15000);
      if (hit && hit.filename) path = hit.filename;
    } catch (_) {}
    out.push(`${String(d.name || '(no name)').replace(/[\u0000-\u001f]/g, ' ').slice(0, 200)}: ${d.state === 'completed' ? 'done' : d.state === 'canceled' ? 'cancelled' : 'in progress'}${d.total ? `, ${d.received || 0} of ${d.total} bytes` : ''}${path ? `, at ${path}` : ''} (${d.url.slice(0, 200)})`);
  }
  return out.join('\n');
}

// pdfSave: the page as a PDF, as the browser prints it. Handed back to the
// agent's side, which writes the file.
async function pdfSave(c, a) {
  const r = await cdp(c, 'Page.printToPDF', { printBackground: true, landscape: !!a.landscape });
  return { pdf: r.data };
}

// ---------------------------------------------------------------- cookies and storage
// The site's cookies (those the browser would send to the tab's page,
// httpOnly ones too) and its local and session storage. These hold the
// person's sign-ins: off as installed (its own switch), each use noted in
// the button's panel.
async function siteURL(c) {
  const t = await chrome.tabs.get(c.tabId);
  if (!/^https?:/i.test(t.url || '')) throw new Error('This tab is not on a web page.');
  return t.url;
}
async function cookies(c, a) {
  await switchedOn('cookies');
  const url = await siteURL(c);
  const action = String(a.action || 'list');
  if (action === 'list') {
    const r = await cdp(c, 'Network.getCookies', { urls: [url] });
    await note(c, 'read the cookies');
    const l = (r.cookies || []).filter((k) => !a.name || k.name === String(a.name));
    if (!l.length) return 'No cookies for this page.';
    return l.map((k) => `${k.name}=${String(k.value).slice(0, 500)} (${k.domain}${k.path}${k.httpOnly ? '; httpOnly' : ''}${k.secure ? '; secure' : ''}${k.session ? '; session' : '; expires ' + new Date(k.expires * 1000).toISOString()})`).join('\n');
  }
  if (action === 'set') {
    if (!a.name) throw new Error('name is required');
    const p = { url, name: String(a.name), value: String(a.value ?? '') };
    // For the tab's own site only (the url): never a domain of the caller's.
    for (const k of ['path', 'sameSite']) if (a[k] != null) p[k] = String(a[k]);
    for (const k of ['secure', 'httpOnly']) if (a[k] != null) p[k] = !!a[k];
    if (a.expires != null) p.expires = Number(a.expires);
    const r = await cdp(c, 'Network.setCookie', p);
    if (r && r.success === false) throw new Error('The browser did not take that cookie.');
    await note(c, `set the cookie ${p.name}`);
    return `Set the cookie ${p.name}.`;
  }
  if (action === 'delete' || action === 'clear') {
    if (action === 'delete' && !a.name) throw new Error('name is required');
    const r = await cdp(c, 'Network.getCookies', { urls: [url] });
    const l = (r.cookies || []).filter((k) => action === 'clear' || k.name === String(a.name));
    for (const k of l) await cdp(c, 'Network.deleteCookies', { name: k.name, domain: k.domain, path: k.path });
    await note(c, action === 'clear' ? 'cleared the cookies' : `deleted the cookie ${a.name}`);
    return `Deleted ${l.length} cookie${l.length === 1 ? '' : 's'}.`;
  }
  throw new Error('action is list, set, delete or clear');
}
async function storage(c, a) {
  await switchedOn('cookies');
  await siteURL(c);
  const kind = a.kind === 'session' ? 'session' : 'local';
  const action = String(a.action || 'list');
  if (!['list', 'get', 'set', 'delete', 'clear'].includes(action)) throw new Error('action is list, get, set, delete or clear');
  if (['get', 'set', 'delete'].includes(action) && a.key == null) throw new Error('key is required');
  const r = await inPage(c, page.storage, kind, action, a.key == null ? null : String(a.key), a.value == null ? '' : String(a.value));
  await note(c, `${action === 'list' || action === 'get' ? 'read' : 'changed'} the ${kind} storage`);
  return r;
}

// emulate: how the page is shown in this tab: a dark or light colour scheme,
// print or screen styles, or with the network cut (to see a page offline).
async function emulate(c, a) {
  const said = [];
  if (a.colorScheme != null || a.media != null) {
    const scheme = ['dark', 'light'].includes(a.colorScheme) ? a.colorScheme : '';
    const media = ['print', 'screen'].includes(a.media) ? a.media : '';
    await cdp(c, 'Emulation.setEmulatedMedia', { media, features: [{ name: 'prefers-color-scheme', value: scheme }] });
    said.push(`colour scheme ${scheme || 'as the person has it'}, media ${media || 'as it is'}`);
  }
  if (a.offline != null) {
    await cdp(c, 'Network.emulateNetworkConditions', { offline: !!a.offline, latency: 0, downloadThroughput: -1, uploadThroughput: -1 });
    said.push(a.offline ? 'the network cut for this tab' : 'the network back');
  }
  if (!said.length) throw new Error('Give colorScheme ("dark", "light", "none"), media ("print", "screen", "none") or offline (true, false).');
  return `This tab now has ${said.join('; ')}.`;
}

// ---------------------------------------------------------------- several at once
// batch: several tools in one call, each held to what it is held to on its
// own. It stops at the first that fails, is refused or paused, opens a
// dialog, or leaves the site it began on, and says what ran.
const BATCH_MAX = 20;
async function batch(m, a) {
  const actions = Array.isArray(a.actions) ? a.actions : [];
  if (!actions.length) throw new Error('actions is required: [{tool, args}, ...]');
  if (actions.length > BATCH_MAX) throw new Error(`at most ${BATCH_MAX} actions at a time`);
  const k = keyOf(m);
  const siteNow = async () => {
    const s = await getS(k);
    if (!s || s.lastTab == null) return '';
    try { return new URL((await chrome.tabs.get(s.lastTab)).url).origin; } catch (_) { return ''; }
  };
  const parts = [];
  let site = await siteNow(), stopped = '';
  for (let i = 0; i < actions.length; i++) {
    const step = actions[i];
    if (!step || typeof step !== 'object' || typeof step.tool !== 'string' || (step.args != null && (typeof step.args !== 'object' || Array.isArray(step.args)))) throw new Error(`step ${i + 1} is not {tool, args}`);
    const tool = step.tool.replace(/^browser_/, '');
    const label = `${i + 1}. ${tool}`;
    if (!tool || tool === 'batch' || tool === 'pdf_save') { stopped = `${label}: not a tool a batch can run.`; break; }
    let out;
    try {
      out = await handle({ ...m, tool, args: (actions[i] && actions[i].args) || {} });
    } catch (e) {
      const msg = String((e && e.message) || e);
      if (msg === STOPPED || msg === OFF) throw e;
      stopped = `${label}: ${msg}`;
      break;
    }
    if (out && typeof out === 'object' && out.image) { parts.push({ text: `${label}: ${out.note || 'a screenshot'}` }); parts.push({ image: out.image, mimeType: out.mimeType }); }
    else parts.push({ text: `${label}: ${typeof out === 'string' ? out : JSON.stringify(out)}` });
    const now = await siteNow();
    if (site && now && now !== site && i + 1 < actions.length) { stopped = `after ${label}: the tab left ${site} for ${now}. Look at the page, then go on.`; break; }
    if (!site) site = now;
  }
  if (stopped) parts.push({ text: `Stopped at ${stopped} (${parts.filter((p) => p.text).length} of ${actions.length} ran)` });
  return { parts };
}
