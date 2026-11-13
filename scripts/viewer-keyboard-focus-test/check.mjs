// run.sh: a touch screen's keyboard comes up only when asked. On a phone the
// keyboard is up for whatever input has focus, so each case is read off
// document.activeElement: the terminal's (xterm's helper textarea), a remote
// window's (#wkey), or none.
import { chromium } from 'playwright';
import http from 'node:http';
import { readFileSync } from 'node:fs';
const html = readFileSync('/t/index.html');
http.createServer((q, r) => { const p = q.url.split('?')[0]; const a = /^\/assets\/([\w.-]+\.js)$/.exec(p); if (a) { let b; try { b = readFileSync('/t/assets/' + a[1]); } catch (_) { r.writeHead(404); return r.end(); } r.writeHead(200, { 'content-type': 'text/javascript' }); return r.end(b); } if (p === '/reload.js') { r.writeHead(200, { 'content-type': 'text/javascript' }); return r.end(readFileSync('/t/reload.js')); } r.writeHead(200, { 'content-type': 'text/html' }); r.end(html); }).listen(8099);
const b = await chromium.launch();
let fails = 0;
const expect = (what, got, want) => {
  if (got === want) console.log(`ok   ${what}: ${got}`);
  else { console.log(`FAIL ${what}: ${got}, want ${want}`); fails++; }
};
const PHONE = { viewport: { width: 412, height: 915 }, isMobile: true, hasTouch: true, deviceScaleFactor: 2.6 };
const DESK = { viewport: { width: 1280, height: 800 } };
const TERM = 'terminal', WIN = 'window', NONE = 'none';
const focused = (page) => page.evaluate(() => {
  const e = document.activeElement;
  if (e && e.classList.contains('xterm-helper-textarea')) return 'terminal';
  if (e && e.id === 'wkey') return 'window';
  return !e || e === document.body ? 'none' : e.tagName.toLowerCase() + '#' + e.id;
});

async function open(opts) {
  const ctx = await b.newContext(opts);
  await ctx.addInitScript(() => { try { localStorage.setItem('reminal-tour-v1', '1'); } catch (_) {} });
  const page = await ctx.newPage();
  page.on('pageerror', (e) => { console.log('FAIL page error: ' + e.message); fails++; });
  await page.goto('http://127.0.0.1:8099/');
  await page.waitForFunction(() => typeof term === 'object' && term && document.querySelector('.xterm-helper-textarea'));
  return { ctx, page };
}
// A remote window's pane, as if its first frame had come: nothing over the
// canvas. Its keyboard starts off.
async function openPane(page) {
  await page.evaluate(() => {
    sendWin = async () => {};
    openWinView({ id: 'w1', title: 'xterm', w: 640, h: 400 });
    for (const e of document.querySelectorAll('.wpane-load, .wpane-stall')) e.style.display = 'none';
  });
  await page.waitForTimeout(100);
}
async function tapPane(page) {
  const r = await page.locator('.wpane-img').boundingBox();
  await page.touchscreen.tap(r.x + r.width / 2, r.y + r.height / 2);
  await page.waitForTimeout(400);
}
// What a socket coming back runs, the relay's side stubbed.
async function reconnect(page) {
  await page.evaluate(() => {
    cryptoKey = {}; agentLive = true; helloShown = true; reconnectMessagePending = true;
    sendResume = () => {}; sendWin = async () => {};
    startSessionFlow();
  });
  await page.waitForTimeout(200);
}
const blur = (page) => page.evaluate(() => document.activeElement && document.activeElement.blur());
const focusTerminal = (page) => page.evaluate(() => document.querySelector('.xterm-helper-textarea').focus());

{
  const { ctx, page } = await open(PHONE);
  expect('the phone is a touch screen', await page.evaluate(() => matchMedia('(pointer: coarse)').matches), true);
  await ctx.close();
}
{
  // The focus Android keeps after its keyboard is put away, then a tap on a
  // window whose keyboard is off: the tap is outside the terminal.
  const { ctx, page } = await open(PHONE);
  await openPane(page);
  await focusTerminal(page);
  await tapPane(page);
  expect('phone, a window with its keyboard off tapped', await focused(page), NONE);
  await ctx.close();
}
{
  const { ctx, page } = await open(PHONE);
  await blur(page);
  await reconnect(page);
  expect('phone, a reconnect with no keyboard up', await focused(page), NONE);
  await ctx.close();
}
{
  // The keyboard up for the terminal (the viewport shrinks, as the page's
  // interactive-widget=resizes-content has it), then put away by hand: the
  // viewport grows back and nothing blurs on its own.
  const { ctx, page } = await open(PHONE);
  await focusTerminal(page);
  await page.setViewportSize({ width: 412, height: 560 });
  await page.waitForTimeout(800);
  expect('phone, the keyboard up for the terminal', await focused(page), TERM);
  await reconnect(page);
  expect('phone, a reconnect with the keyboard up', await focused(page), TERM);
  await page.setViewportSize({ width: 412, height: 915 });
  await page.waitForTimeout(800);
  expect('phone, the keyboard put away by hand', await focused(page), NONE);
  await ctx.close();
}
{
  const { ctx, page } = await open(PHONE);
  await openPane(page);
  await page.locator('.wpane-kbd').tap();
  await page.waitForTimeout(200);
  expect('phone, a window\'s keyboard turned on', await focused(page), WIN);
  await blur(page);
  await tapPane(page);
  expect('phone, a window with its keyboard on tapped', await focused(page), WIN);
  await ctx.close();
}
{
  const { ctx, page } = await open(DESK);
  await blur(page);
  await reconnect(page);
  expect('desktop, a reconnect', await focused(page), TERM);
  await ctx.close();
}
await b.close();
console.log(fails ? `${fails} failed` : 'all passed');
process.exit(fails ? 1 : 0);
