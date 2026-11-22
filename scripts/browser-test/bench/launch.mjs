// The browser for the bench's "ours" side: Chromium with reminal's extension,
// developer mode on, kept open until /tmp/bench.done is there.
import { chromium } from 'playwright';
import fs from 'node:fs';
const HOME = process.env.HOME, EXT = `${HOME}/reminal-extension`;
fs.mkdirSync(`${HOME}/.config/chromium/Default`, { recursive: true });
fs.writeFileSync(`${HOME}/.config/chromium/Default/Preferences`, JSON.stringify({ extensions: { ui: { developer_mode: true } } }));
const ctx = await chromium.launchPersistentContext(`${HOME}/.config/chromium`, { headless: false, viewport: null, ignoreDefaultArgs: ['--enable-automation'],
  args: [`--disable-extensions-except=${EXT}`, `--load-extension=${EXT}`, '--no-first-run', '--no-default-browser-check'] });
// Up once the extension has reached reminal.
const sw = ctx.serviceWorkers().find((w) => w.url().includes('background.js')) || await ctx.waitForEvent('serviceworker', { timeout: 45000 });
// Its button's panel asks it to connect if it has not yet (as a person
// opening it would).
const pop = await ctx.newPage();
await pop.goto(`chrome-extension://${new URL(sw.url()).host}/popup.html`).catch(() => {});
for (let i = 0; i < 240; i++) {
  const host = await sw.evaluate(() => chrome.storage.session.get('host').then((o) => o.host)).catch(() => null);
  if (host && host.connected) break;
  await new Promise((r) => setTimeout(r, 250));
}
await pop.close().catch(() => {});
fs.writeFileSync('/tmp/bench.browser', 'up');
while (!fs.existsSync('/tmp/bench.done')) await new Promise((r) => setTimeout(r, 500));
await ctx.close();
