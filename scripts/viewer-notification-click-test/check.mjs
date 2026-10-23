// run.sh: a tapped notification, the service worker's side and the page's.
import { chromium } from 'playwright';
import http from 'node:http';
import vm from 'node:vm';
import { readFileSync } from 'node:fs';

let fails = 0;
const ok = (m) => console.log('ok   ' + m);
const fail = (m) => { console.log('FAIL ' + m); fails++; };
const check = (c, good, why) => (c ? ok(good) : fail(good + ' — ' + why));

// ---- the service worker, with what it is given by the browser stood in -----
{
  const handlers = {};
  let wins = [];
  const opened = [];
  const self = {
    addEventListener: (t, f) => { handlers[t] = f; },
    skipWaiting: () => {},
    registration: { showNotification: async () => {}, getNotifications: async () => [] },
    clients: { matchAll: async () => wins, openWindow: async (u) => { opened.push(u); }, claim: async () => {} },
  };
  vm.runInNewContext(readFileSync('/t/sw.js', 'utf8'), { self, caches: {}, Response, URL, Date, String, Number, encodeURIComponent, JSON });
  const tap = async (data) => {
    let closed = false, p;
    handlers.notificationclick({ notification: { data, close: () => { closed = true; } }, waitUntil: (x) => { p = x; } });
    await p;
    return closed;
  };
  const win = () => { const w = { posted: [], focused: 0 }; w.postMessage = (m) => w.posted.push(m); w.focus = async () => { w.focused++; }; return w; };

  const a = win(); wins = [a];
  const closed = await tap({ url: '/?x=1#y', at: 5 });
  check(closed && a.focused === 1, 'a tap closes the notification and focuses the open page', JSON.stringify({ closed, a }));
  check(a.posted.length === 1 && a.posted[0].type === 'notification-url' && a.posted[0].url === '/?x=1#y', 'the open page is told where it points', JSON.stringify(a.posted));
  check(!opened.length, 'and no new window is opened', JSON.stringify(opened));

  const b = win(); wins = [b];
  await tap({ url: '/', at: 0 });
  check(b.focused === 1 && b.posted.length === 1 && b.posted[0].url === '/', 'a machine alert (/) focuses the page, as before, and says /', JSON.stringify(b));
  const c = win(); wins = [c];
  await tap(undefined);
  check(c.focused === 1 && c.posted.length === 1 && c.posted[0].url === '/', 'a notification with no data says /', JSON.stringify(c));

  wins = [];
  await tap({ url: '/?x=2' });
  check(opened.length === 1 && opened[0] === '/?x=2', 'with no page open, it opens one there, as before', JSON.stringify(opened));
}

// ---- the page ---------------------------------------------------------------
http.createServer((q, r) => {
  const p = q.url.split('?')[0];
  let f = null;
  try { f = readFileSync('/t' + (p === '/' ? '/index.html' : p)); } catch (_) {}
  if (!f) { r.writeHead(404); return r.end(); }
  r.writeHead(200, { 'content-type': p.endsWith('.js') ? 'text/javascript' : p.endsWith('.css') ? 'text/css' : 'text/html' });
  r.end(f);
}).listen(8099);

const browser = await chromium.launch();
const ctx = await browser.newContext();
const page = await ctx.newPage();
const errs = [];
page.on('pageerror', (e) => errs.push(e.message));
await page.goto('http://127.0.0.1:8099/');
await page.waitForFunction(() => typeof onNotificationURL === 'function' && typeof openAlerts === 'function', null, { timeout: 30000 });
const send = (url) => page.evaluate(async (url) => {
  navigator.serviceWorker.dispatchEvent(new MessageEvent('message', { data: { type: 'notification-url', url } }));
  await new Promise(r => setTimeout(r, 50));
}, url);

// No handler: the page stays as it is (a machine's alert).
const before = await page.evaluate(() => location.href);
await send('/');
await send('/?x=1');
check((await page.evaluate(() => location.href)) === before, 'with nothing to take it, the page stays where it is', await page.evaluate(() => location.href));

await page.evaluate(() => {
  window.__got = [];
  onNotificationURL(() => { throw new Error('a broken handler'); });
  onNotificationURL((u) => { __got.push('a:' + u.pathname + u.search + u.hash); return u.searchParams.has('a'); });
  onNotificationURL((u) => { __got.push('b:' + u.pathname + u.search); return true; });
});
await send('/?a=1#n');
check(JSON.stringify(await page.evaluate(() => __got)) === JSON.stringify(['a:/?a=1#n']), 'its handler is given it, as a URL on this site; once one takes it, the rest are not asked', JSON.stringify(await page.evaluate(() => __got)));
await page.evaluate(() => { __got.length = 0; });
await send('/?b=1');
check(JSON.stringify(await page.evaluate(() => __got)) === JSON.stringify(['a:/?b=1', 'b:/?b=1']), 'one that does not take it passes it on, past a handler that throws', JSON.stringify(await page.evaluate(() => __got)));
await page.evaluate(() => { __got.length = 0; });
for (const u of ['//evil.example/x', 'https://evil.example/', 'javascript:alert(1)', 42, null]) await send(u);
check((await page.evaluate(() => __got.length)) === 0, 'anything but a path on this site is dropped', JSON.stringify(await page.evaluate(() => __got)));
const took = await page.evaluate(() => [routeNotificationURL('/?a=1'), routeNotificationURL('//x')]);
check(took[0] === true && took[1] === false, 'routeNotificationURL says whether it was taken', JSON.stringify(took));
check((await page.evaluate(() => location.href)) === before, 'and the page itself was never moved by it', await page.evaluate(() => location.href));
check(!errs.length, 'no page errors', errs.join(' | '));
await browser.close();
console.log(fails ? fails + ' failed' : 'all passed');
process.exit(fails ? 1 : 0);
