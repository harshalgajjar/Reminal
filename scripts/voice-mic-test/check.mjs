// The voice mic, as a person finds it (run.sh): on a desktop the header's
// mic is shown (the key bar, and its mic, are not) and a tap opens the voice
// panel; on a phone the key bar's mic does. On a session whose reminal
// predates voice (host_info without "voice"), a tap opens nothing and says
// why. The page is served alone: the session is stood in for by calling
// what app.js calls on host_info.
import { chromium } from 'playwright';
import http from 'node:http';
import { readFileSync } from 'node:fs';
const html = readFileSync('/t/index.html');
const srv = http.createServer((q, r) => {
  const p = q.url.split('?')[0];
  const a = /^\/assets\/([\w.-]+\.js)$/.exec(p);
  if (a) { let b; try { b = readFileSync('/t/assets/' + a[1]); } catch (_) { r.writeHead(404); return r.end(); } r.writeHead(200, { 'content-type': 'text/javascript' }); return r.end(b); }
  if (p === '/reload.js') { r.writeHead(200, { 'content-type': 'text/javascript' }); return r.end(readFileSync('/t/reload.js')); }
  // The voice frame's own files (web/voice), as the binary serves them.
  const v = /^\/voice\/([\w.-]+)$/.exec(p);
  if (v) { let b; try { b = readFileSync('/v/' + v[1]); } catch (_) { r.writeHead(404); return r.end(); } r.writeHead(200, { 'content-type': TYPES[v[1].split('.').pop()] || 'text/plain' }); return r.end(b); }
  if (p.startsWith('/voice-assets/')) { r.writeHead(404); return r.end(); } // no models here
  r.writeHead(200, { 'content-type': 'text/html' }); r.end(html);
}).listen(8099);
const TYPES = { html: 'text/html', js: 'text/javascript', txt: 'text/plain' };
const NEED = 'Voice needs a newer reminal on that machine';
const SIZES = [
  ['desktop', { viewport: { width: 1280, height: 800 } }, '#voice-btn-desk', '#voice-btn'],
  ['phone', { viewport: { width: 412, height: 800 }, isMobile: true, hasTouch: true }, '#voice-btn', '#voice-btn-desk'],
];
const b = await chromium.launch();
const fails = [];
const check = (ok, what) => { console.log((ok ? 'ok   ' : 'FAIL ') + what); if (!ok) fails.push(what); };
for (const [size, opts, mic, other] of SIZES) {
  for (const voice of [true, false]) {
    const ctx = await b.newContext(opts);
    const page = await ctx.newPage();
    await page.goto('http://127.0.0.1:8099/');
    await page.evaluate((voice) => {
      // No host here: a session is stood in for.
      sendWin = () => {};
      sessionId = 'TESTSESS';
      document.body.classList.add('connected');
      reminalVoice.hostInfo(voice ? { attn: '', voice: true } : { attn: '' });
    }, voice);
    await page.addStyleTag({ content: '.tour-block,.tour-ring,.tour-card,.tour-mock{display:none!important}' });
    const name = `${size}, ${voice ? 'a reminal with voice' : 'an older reminal'}`;
    check(await page.isVisible(mic), `${name}: ${mic} is shown`);
    check(!(await page.isVisible(other)), `${name}: ${other} is not`);
    await page.click(mic);
    await page.waitForTimeout(2700); // past the panel's sizing wait
    const panel = await page.evaluate(() => { const p = document.getElementById('voice-panel'); return p ? (p.hidden ? 'hidden' : 'shown') : 'none'; });
    const dock = await page.evaluate(() => { const d = document.getElementById('voicedock'); return d && d.classList.contains('open') ? d.textContent : ''; });
    if (voice) {
      check(panel === 'shown' && await page.isVisible('#voice-panel iframe'), `${name}: a tap opens the voice panel (${panel})`);
    } else {
      check(panel === 'none', `${name}: a tap opens no voice panel (${panel})`);
      check(dock.includes(NEED), `${name}: it says "${NEED}" (${JSON.stringify(dock)})`);
      check(await page.getAttribute(mic, 'title') === NEED, `${name}: the mic's title says so too`);
    }
    if (process.env.SHOTS) await page.screenshot({ path: `${process.env.SHOTS}/${size}-${voice ? 'voice' : 'old'}.png` });
    await ctx.close();
  }
}
// A notice from the session (TypeSpeech {notice}: what was said was not typed
// in) goes from the viewer (reminalVoice.speech, as app.js calls it) to the
// frame, whose conversation says why, out loud and in its log. The frame's
// conversation is started with a stand-in speaker (no models load here): its
// own module, the one the frame's voice.js uses, imported in the frame.
{
  const ctx = await b.newContext({ viewport: { width: 1280, height: 800 } });
  const page = await ctx.newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(String(e).slice(0, 200)));
  await page.goto('http://127.0.0.1:8099/');
  await page.evaluate(() => {
    sendWin = () => {};
    sessionId = 'TESTSESS';
    document.body.classList.add('connected');
    reminalVoice.hostInfo({ attn: '', voice: true });
  });
  await page.click('#voice-btn-desk');
  const fh = await page.waitForSelector('#voice-panel iframe');
  const fr = await fh.contentFrame();
  await fr.waitForSelector('#log');
  await fr.evaluate(async () => {
    const talk = await import('/voice/conversation.js');
    window.__said = [];
    const quiet = () => {};
    talk.start({ speaker: {
      say: (r, line) => window.__said.push(line), cancel: quiet, end: quiet, ack: quiet, nod: quiet, duck: quiet, fadeOut: quiet, pulse: quiet,
      audible: () => false, answering: () => false, nodding: () => false, heardWhole: () => true, natural: () => false, current: () => null, level: () => 0, ctx: null,
    } });
  });
  const said = (why) => page.evaluate((why) => reminalVoice.speech('', why), why);
  await said('asking');
  await page.waitForTimeout(300);
  let got = await fr.evaluate(() => ({ said: window.__said.slice(), log: document.getElementById('log').innerText }));
  const ASKING = 'It is asking on screen. Answer there; I did not type that in.';
  check(got.said.includes(ASKING), `a notice "asking" is said out loud (${JSON.stringify(got.said)})`);
  check(got.log.includes(ASKING), 'and shown in the conversation');
  await said('no-agent');
  await page.waitForTimeout(300);
  got = await fr.evaluate(() => window.__said.slice());
  check(got.some((l) => /No agent is running there/.test(l)), `a notice "no-agent" is said too (${JSON.stringify(got)})`);
  check(errors.length === 0, `no page errors (${JSON.stringify(errors)})`);
  if (process.env.SHOTS) await page.screenshot({ path: `${process.env.SHOTS}/notice.png` });
  await ctx.close();
}
// The first-visit tour (a fresh browser: nothing stored) never covers voice
// in use: it waits while the panel is open, and starts once it is closed. It
// shows the mic as a step of its own, its spotlight on the mic shown: the
// header's on a desktop, the key bar's on a phone.
const firstVisit = async (opts) => {
  const ctx = await b.newContext(opts);
  const page = await ctx.newPage();
  await page.goto('http://127.0.0.1:8099/');
  await page.evaluate(() => {
    sendWin = () => {};
    sessionId = 'TESTSESS';
    document.body.classList.add('connected');
    reminalVoice.hostInfo({ attn: '', voice: true });
  });
  return { ctx, page };
};
const tourCard = (page) => page.evaluate(() => !!document.querySelector('.tour-card'));
{
  const { ctx, page } = await firstVisit({ viewport: { width: 1280, height: 800 } });
  await page.evaluate(() => document.getElementById('voice-btn-desk').click());
  await page.waitForTimeout(2500);
  check(!(await tourCard(page)), 'first visit: the tour waits while the voice panel is open');
  await page.evaluate(() => document.getElementById('voice-btn-desk').click()); // closed
  await page.waitForFunction(() => !!document.querySelector('.tour-card'), null, { timeout: 5000 }).catch(() => {});
  check(await tourCard(page), 'first visit: the tour starts once the panel is closed');
  await ctx.close();
}
for (const [size, opts, mic] of SIZES) {
  const { ctx, page } = await firstVisit(opts);
  await page.waitForFunction(() => !!document.querySelector('.tour-card'), null, { timeout: 5000 }).catch(() => {});
  let title = '';
  for (let i = 0; i < 25 && title !== 'Talk to the agent'; i++) {
    await page.evaluate(() => document.querySelector('.tour-card [data-t="next"]')?.click());
    await page.waitForTimeout(450);
    title = await page.evaluate(() => document.querySelector('.tour-card .tour-title')?.textContent || '');
  }
  check(title === 'Talk to the agent', `${size}: the tour has a step for the mic`);
  await page.waitForTimeout(800); // the spotlight moves to it
  const lit = await page.evaluate((mic) => {
    const ring = document.querySelector('.tour-ring').getBoundingClientRect(), m = document.querySelector(mic).getBoundingClientRect();
    return ring.left <= m.left + 1 && ring.right >= m.right - 1 && ring.top <= m.top + 1 && ring.bottom >= m.bottom - 1;
  }, mic);
  check(lit, `${size}: its spotlight is on ${mic}`);
  if (process.env.SHOTS) await page.screenshot({ path: `${process.env.SHOTS}/tour-${size}.png` });
  await ctx.close();
}
// Voice on, and the person switches to a session whose reminal predates
// voice: voice stops, and the line saying why stays in sight (no hover on a
// phone, and they may have looked away) until their next tap.
for (const [size, opts, mic] of SIZES) {
  const ctx = await b.newContext(opts);
  await ctx.addInitScript(() => { try { localStorage.setItem('reminal-tour-v1', 'done'); } catch (_) {} });
  const page = await ctx.newPage();
  await page.goto('http://127.0.0.1:8099/');
  await page.evaluate(() => {
    sendWin = () => {};
    sessionId = 'TESTSESS';
    document.body.classList.add('connected');
    reminalVoice.hostInfo({ attn: '', voice: true });
  });
  await page.click(mic);
  const fr = await (await page.waitForSelector('#voice-panel iframe')).contentFrame();
  await fr.waitForSelector('#log');
  // Voice running, as the frame reports it to the viewer.
  await fr.evaluate(() => window.parent.postMessage({ src: 'reminal-voice', t: 'state', running: true, cls: 'hear', text: 'Listening' }, location.origin));
  await page.waitForFunction((mic) => document.querySelector(mic).classList.contains('live'), mic);
  await page.evaluate(() => { sessionId = 'OLDSESS'; reminalVoice.sessionChanged(); reminalVoice.hostInfo({ attn: '' }); });
  await page.waitForTimeout(5000);
  const note = () => page.evaluate(() => { const d = document.getElementById('voicedock'); return d && d.classList.contains('open') && getComputedStyle(d).display !== 'none' ? d.textContent : ''; });
  check((await note()).includes(NEED), `${size}: switched with voice on, the line is still in sight after 5 s`);
  check(await page.evaluate(() => document.getElementById('voice-panel').hidden), `${size}: and the panel is closed`);
  if (process.env.SHOTS) await page.screenshot({ path: `${process.env.SHOTS}/switched-${size}.png` });
  await page.mouse.click(200, 400);
  await page.waitForTimeout(300);
  check((await note()) === '', `${size}: the next tap puts it away`);
  await ctx.close();
}
await b.close(); srv.close();
console.log(fails.length ? `${fails.length} failed` : 'all ok');
process.exit(fails.length ? 1 : 0);
