// The browser feature as a person and an agent meet it (rig.sh, in Docker).
// Chromium runs with reminal's extension, in a profile of its own; the
// stand-in agent in a reminal session (agent.sh) calls the browser_* tools;
// this script answers the consent window as the person would, and checks
// what each tool did, and each rule.
import { chromium } from 'playwright';
import fs from 'node:fs';
import { execSync } from 'node:child_process';

const ID = process.env.ID, HOME = process.env.HOME;
const EXT = `${HOME}/.reminal/chrome-extension`;
const PAGES = 'http://127.0.0.1:8088';
const EXT_ID = 'igahbbfpoepnabdbgndnheeieimlhpjh';
const GIF = !!process.env.GIF;
const fails = [];
const check = (ok, what) => { console.log((ok ? 'ok   ' : 'FAIL ') + what); if (!ok) fails.push(what); };
const pause = (ms) => new Promise((r) => setTimeout(r, ms));
const slow = (ms) => (GIF ? pause(ms) : Promise.resolve());

// A tool call, made by the stand-in agent inside the session.
let n = 0;
async function call(tool, args = {}) {
  const k = String(++n).padStart(3, '0');
  fs.writeFileSync(`/tmp/q/${k}.tmp`, tool + '\n' + JSON.stringify(args));
  fs.renameSync(`/tmp/q/${k}.tmp`, `/tmp/q/${k}.req`);
  for (let i = 0; i < 1300; i++) {
    if (fs.existsSync(`/tmp/q/${k}.res`)) {
      const raw = fs.readFileSync(`/tmp/q/${k}.res`, 'utf8');
      let j; try { j = JSON.parse(raw); } catch (_) { return { err: 'no answer: ' + raw.slice(0, 200), content: [] }; }
      const content = (j.result && j.result.content || []).filter((c) => !(c.type === 'text' && /^⚠ reminal/.test(c.text)));
      const text = content.filter((c) => c.type === 'text').map((c) => c.text).join('\n');
      return { err: j.result && j.result.isError ? text : null, text, content };
    }
    await pause(100);
  }
  return { err: 'timed out', content: [] };
}

const ctx = await chromium.launchPersistentContext(`${HOME}/.config/chromium`, {
  headless: false, viewport: null, ignoreDefaultArgs: ['--enable-automation'],
  args: [`--disable-extensions-except=${EXT}`, `--load-extension=${EXT}`, '--no-first-run', '--no-default-browser-check'],
});
// The browser's window fills the screen; the consent window keeps its own size
// (a size flag would apply to every window).
{
  const first = ctx.pages()[0] || await ctx.newPage();
  const cdp = await ctx.newCDPSession(first);
  const { windowId } = await cdp.send('Browser.getWindowForTarget');
  await cdp.send('Browser.setWindowBounds', { windowId, bounds: { left: 0, top: 0, width: 1280, height: 800 } });
}
let sw = ctx.serviceWorkers().find((w) => w.url().includes(EXT_ID));
if (!sw) sw = await ctx.waitForEvent('serviceworker', { predicate: (w) => w.url().includes(EXT_ID), timeout: 20000 });
check(sw.url().startsWith(`chrome-extension://${EXT_ID}/`), `the extension loaded with its fixed id (${sw.url()})`);
const store = (k) => sw.evaluate((k) => chrome.storage.session.get(k).then((o) => o[k]), k);
let host = null;
for (let i = 0; i < 60 && !(host && host.connected); i++) { host = await store('host'); await pause(250); }
check(host && host.connected, `it reached reminal through the native host (${JSON.stringify(host)})`);
const groups = () => sw.evaluate(() => chrome.tabGroups.query({}).then((g) => g.map((x) => x.title)));

// The consent window, answered as the person would: after looking at it.
async function answerConsent(allow) {
  const p = await ctx.waitForEvent('page', { predicate: (pg) => pg.url().includes('/consent.html'), timeout: 20000 });
  await p.waitForLoadState();
  await p.bringToFront();
  const words = await p.textContent('#who');
  await pause(GIF ? 2200 : 1300); // its buttons work only after a second with the focus
  await p.click(allow ? '#allow' : '#deny');
  return words;
}
const page = (re) => ctx.pages().find((pg) => re.test(pg.url()));

// 1. The control: the person says no. Nothing is done, and no tab group made.
{
  const r = call('browser_tabs_context');
  const words = await answerConsent(false);
  check(/"web helper" \(\w+\), a reminal session on this computer \(.+\), wants to use your browser/.test(words), `the window names the session and the computer: ${JSON.stringify(words)}`);
  const res = await r;
  check(res.err && /did not allow/.test(res.err), `denied: the agent is told (${res.err})`);
  check((await groups()).length === 0, 'denied: no tab group was made');
  const again = await call('browser_tabs_context');
  check(again.err && /did not allow/.test(again.err), 'denied: asked again, it is still no (not asked twice)');
  // The person forgets the denial in the extension's button, so it may ask again.
  const pop = await ctx.newPage();
  await pop.goto(`chrome-extension://${EXT_ID}/popup.html`);
  await pop.click('text=Forget');
  await pop.close();
}

// 2. The person allows: a tab in the session's own group.
let tabId;
{
  const r = call('browser_tabs_create', { url: `${PAGES}/form.html` });
  await answerConsent(true);
  const res = await r;
  try { tabId = JSON.parse(res.text).tabId; } catch (_) {}
  check(!res.err && tabId > 0, `allowed: a tab opened (${res.err || res.text})`);
  const g = await groups();
  check(g.length === 1 && g[0] === 'reminal: web helper', `in a tab group of the session's own (${JSON.stringify(g)})`);
  await slow(1200);
}

// 3. Screenshot, reading, typing, clicking.
{
  const res = await call('browser_screenshot');
  const img = res.content.find((c) => c.type === 'image');
  const png = img && Buffer.from(img.data, 'base64');
  const ok = png && png.slice(1, 4).toString() === 'PNG';
  check(ok, `a screenshot comes back as a PNG image (${png ? png.length : 0} bytes, ${ok ? png.readUInt32BE(16) + 'x' + png.readUInt32BE(20) : '-'})`);
  const tree = (await call('browser_read_page', { filter: 'interactive' })).text || '';
  const name = (tree.match(/textbox "Name on the order" \[(ref_\d+)\]/) || [])[1];
  const order = (tree.match(/button "Place order" \[(ref_\d+)\]/) || [])[1];
  check(name && order, `read_page gives refs (name ${name}, order ${order})`);
  check(/password: the person types it/.test(tree) && !/value="hunter/.test(tree), 'a password field is named as one, its value never read');
  let r = await call('browser_type', { ref: name, text: 'Ada Lovelace' });
  check(!r.err, `typed into the name field, though the page has a sign-in box in its header (${r.err || r.text})`);
  await slow(600);
  r = await call('browser_click', { ref: order });
  check(!r.err, `clicked Place order (${r.err || r.text})`);
  const shown = await page(/form\.html/).evaluate(() => !!document.getElementById('__reminal_indicator'));
  check(shown, 'the page shows the agent is acting on it');
  const text = (await call('browser_get_page_text')).text || '';
  check(/Ordered: Medium for Ada Lovelace/.test(text), 'the order went through, as the page says');
  r = await call('browser_key', { key: 'End' });
  check(!r.err, `a key pressed (${r.err || r.text})`);
  r = await call('browser_scroll', { direction: 'up', amount: 2 });
  check(!r.err, `scrolled (${r.err || r.text})`);
  await slow(1500);
}

// 4. The rules: sign-in forms, passwords and payment fields, CAPTCHAs.
{
  let r = await call('browser_navigate', { url: `${PAGES}/login.html` });
  check(!r.err && /Sign in/.test(r.text), `navigated (${r.err || r.text})`);
  const tree = (await call('browser_read_page')).text || '';
  check(/sign-in form: actions inside it are paused/.test(tree), 'reading a sign-in page works, and says actions there are paused');
  const user = (tree.match(/textbox "Email" \[(ref_\d+)\]/) || [])[1];
  r = await call('browser_click', { ref: user });
  check(r.err && /Paused: this is part of a sign-in form/.test(r.err), `an action in a sign-in form is paused (${r.err})`);
  await page(/login\.html/).click('#pass'); // the person clicks into the password field
  r = await call('browser_type', { text: 'hunter2' });
  check(r.err && /Refused: reminal never types into password/.test(r.err), `typing into a password field is refused (${r.err})`);
  check(await page(/login\.html/).inputValue('#pass') === '', 'and nothing went into it');
  await slow(1200);

  await call('browser_navigate', { url: `${PAGES}/pay.html` });
  const pt = (await call('browser_read_page')).text || '';
  const card = (pt.match(/textbox "Card number" \[(ref_\d+)\]/) || [])[1];
  r = await call('browser_type', { ref: card, text: '4111111111111111' });
  check(r.err && /Refused/.test(r.err), `typing into a card number field is refused (${r.err})`);

  await call('browser_navigate', { url: `${PAGES}/captcha.html` });
  const ct = (await call('browser_read_page')).text || '';
  check(/A CAPTCHA is showing/.test(ct), 'reading a CAPTCHA page works, and says so');
  const next = (ct.match(/button "Continue" \[(ref_\d+)\]/) || [])[1];
  r = await call('browser_click', { ref: next });
  check(r.err && /Paused: a CAPTCHA is showing/.test(r.err), `an action with a CAPTCHA showing is paused (${r.err})`);
  await slow(1200);
}

// 5. Only its own tabs; never the browser's own pages.
{
  const other = await ctx.newPage();
  await other.goto(`${PAGES}/form.html?mine`);
  const otherId = await sw.evaluate(() => chrome.tabs.query({}).then((t) => (t.find((x) => /mine/.test(x.url)) || {}).id));
  let r = await call('browser_navigate', { tabId: otherId, url: `${PAGES}/login.html` });
  check(r.err && /not in this session's tab group/.test(r.err), `a tab of the person's own is refused (${r.err})`);
  check(other.url().includes('mine'), 'and it was not touched');
  await other.close();
  r = await call('browser_navigate', { url: 'chrome://settings' });
  check(r.err && /may not use the browser's own pages/.test(r.err), `chrome:// is refused (${r.err})`);
}

// 6. The person stops it in the extension's button: asked again next time.
{
  const pop = await ctx.newPage();
  await pop.goto(`chrome-extension://${EXT_ID}/popup.html`);
  check(/web helper/.test(await pop.textContent('#list')), 'the button lists the session');
  await slow(1000);
  await pop.click('text=Stop');
  await pop.close();
  check((await groups()).length === 0, 'Stop: its tab group is undone (the tabs stay)');
  const r = call('browser_tabs_create', { url: `${PAGES}/form.html` });
  await answerConsent(true);
  check(!(await r).err, 'after Stop it asks again before doing anything');
}

// 7. The session ends: its consent and its group go with it.
{
  execSync(`reminal kill -y ${ID}`, { stdio: "pipe" });
  let s = null;
  for (let i = 0; i < 40; i++) { s = await store('sessions'); if (!s || !s[ID]) break; await pause(500); }
  check(!s || !s[ID], 'the session ended: its consent is gone');
  check((await groups()).length === 0, 'and its tab group');
}

await ctx.close();
console.log(fails.length ? `${fails.length} failed` : 'all ok');
process.exit(fails.length ? 1 : 0);
