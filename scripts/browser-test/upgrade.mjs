// reminal upgraded in place (upgrade.sh, in Docker). PHASE=before: the
// release before, its extension loaded in a new profile and used.
// PHASE=after: the same profile, the browser started again with this
// reminal's files at the same path and nothing reloaded by hand. A browser
// keeps an extension's worker script with the profile, so it can start the
// older script on the newer files: every tool then has to work all the
// same, and the code running has to be this reminal's.
import { chromium } from 'playwright';
import fs from 'node:fs';

const HOME = process.env.HOME, Q = process.env.Q, AFTER = process.env.PHASE === 'after';
const EXT = `${HOME}/reminal-extension`;
const PAGES = 'http://127.0.0.1:8088';
const EXT_ID = 'igahbbfpoepnabdbgndnheeieimlhpjh';
const fails = [];
const check = (ok, what) => { console.log((ok ? 'ok   ' : 'FAIL ') + what); if (!ok) fails.push(what); };
const pause = (ms) => new Promise((r) => setTimeout(r, ms));

let n = 0;
async function call(tool, args = {}) {
  const k = String(++n).padStart(3, '0');
  fs.writeFileSync(`${Q}/${k}.tmp`, tool + '\n' + JSON.stringify(args));
  fs.renameSync(`${Q}/${k}.tmp`, `${Q}/${k}.req`);
  for (let i = 0; i < 600; i++) {
    if (fs.existsSync(`${Q}/${k}.res`)) {
      const raw = fs.readFileSync(`${Q}/${k}.res`, 'utf8');
      let j; try { j = JSON.parse(raw); } catch (_) { return { err: 'no answer: ' + raw.slice(0, 200), text: '', content: [] }; }
      const content = (j.result && j.result.content || []).filter((c) => !(c.type === 'text' && /^⚠ reminal/.test(c.text)));
      const text = content.filter((c) => c.type === 'text').map((c) => c.text).join('\n');
      return { err: j.result && j.result.isError ? text : null, text, content };
    }
    await pause(100);
  }
  return { err: 'timed out', text: '', content: [] };
}

if (!AFTER) {
  fs.mkdirSync(`${HOME}/.config/chromium/Default`, { recursive: true });
  fs.writeFileSync(`${HOME}/.config/chromium/Default/Preferences`, JSON.stringify({ extensions: { ui: { developer_mode: true } } }));
}
const onDisk = (fs.readFileSync(`${EXT}/manifest.json`, 'utf8').match(/"version": "([^"]*)"/) || [])[1];
const ctx = await chromium.launchPersistentContext(`${HOME}/.config/chromium`, {
  headless: false, viewport: null, ignoreDefaultArgs: ['--enable-automation'],
  args: [`--disable-extensions-except=${EXT}`, `--load-extension=${EXT}`, '--no-first-run', '--no-default-browser-check'],
});
const workers = [];
ctx.on('serviceworker', (w) => { if (w.url().includes(EXT_ID)) workers.push(w); });
for (const w of ctx.serviceWorkers()) if (w.url().includes(EXT_ID)) workers.push(w);
// What the extension says of itself, asked of its newest worker (one that
// reloaded is a new worker; the old handle answers nothing).
const state = async () => {
  for (let i = 0; i < 20; i++) {
    const w = workers[workers.length - 1];
    const s = w && await Promise.race([w.evaluate(async () => ({ manifest: chrome.runtime.getManifest().version, ...(await chrome.storage.session.get(['code', 'host'])) })).catch(() => null), pause(3000).then(() => null)]);
    if (s) return s;
    await pause(500);
  }
  return {};
};
let st = {};
for (let i = 0; i < 60; i++) { st = await state(); if (st.host && st.host.connected && !st.host.error && (!AFTER || st.code === onDisk)) break; await pause(500); }
console.log(`== ${AFTER ? 'after the upgrade' : 'the release before'}: files ${onDisk}, the extension says ${JSON.stringify(st)}, its worker started ${workers.length} time(s)`);
check(st.host && st.host.connected && !st.host.error, 'the extension is connected to reminal');

let r = await call('browser_tabs_create', { url: `${PAGES}/form.html` });
check(!r.err, `a tab opens (${r.err || 'ok'})`);
r = await call('browser_read_page', { filter: 'interactive' });
const order = ((r.text || '').match(/button "Place order" \[(ref_\d+)\]/) || [])[1];
check(!r.err && order, `read_page reads the page (${r.err || (r.text || '').slice(0, 160).replace(/\n/g, ' | ')})`);
r = await call('browser_get_page_text');
check(!r.err && /Order a pizza/.test(r.text), `get_page_text reads the page (${r.err || (r.text || '').slice(0, 160).replace(/\n/g, ' | ')})`);
if (AFTER) {
  r = await call('browser_screenshot');
  const img = r.content.find((c) => c.type === 'image');
  check(img && Buffer.from(img.data, 'base64').slice(1, 4).toString() === 'PNG', `a screenshot comes back (${r.err || (img ? 'a PNG' : 'no image')})`);
  r = await call('browser_click', { ref: order });
  const page = ctx.pages().find((p) => /form\.html$/.test(p.url()));
  const out = page ? await page.evaluate(() => document.getElementById('out').textContent) : '';
  check(!r.err && /^Ordered: /.test(out), `a click lands (${r.err || r.text}; the page says "${out}")`);
  st = await state();
  check(st.code && st.code === onDisk && st.manifest === onDisk, `the extension's running code is this reminal's (files ${onDisk}, manifest ${st.manifest}, running code ${st.code || 'older: it does not say'})`);
}
await ctx.close();
console.log(fails.length ? `${fails.length} failed` : 'all ok');
process.exit(fails.length ? 1 : 0);
