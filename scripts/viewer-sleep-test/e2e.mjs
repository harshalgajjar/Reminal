// e2e.sh: sleep and wake from the viewer, as an owner device of the machine.
import { chromium } from 'playwright';
import { execSync, spawn } from 'node:child_process';
import { existsSync, readFileSync, readdirSync } from 'node:fs';

let fails = 0;
const ok = (m) => console.log('ok   ' + m);
const fail = (m) => { console.log('FAIL ' + m); fails++; };
const check = (c, good, why) => (c ? ok(good) : fail(good + ' — ' + why));
const sh = (c) => execSync(c, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
const sleep = (ms) => new Promise(r => setTimeout(r, ms));
async function until(fn, secs = 30) {
  for (const end = Date.now() + secs * 1000; Date.now() < end; await sleep(300)) { try { if (await fn()) return true; } catch (_) {} }
  return false;
}
const restore = (id, suf) => '/root/.reminal/restore/' + id + suf;
const convs = () => readdirSync('/root/.fake-claude').filter(f => f.endsWith('.conv')).map(f => readFileSync('/root/.fake-claude/' + f, 'utf8')).join('\n');

spawn('reminal', ['relay', '8080'], { detached: true, stdio: 'ignore' }).unref();
if (!(await until(async () => (await fetch('http://127.0.0.1:8080/')).ok, 20))) { fail('the relay did not start'); process.exit(1); }
const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 1280, height: 860 } });
await ctx.addInitScript(() => { try { localStorage.setItem('reminal-tour-v1', 'skipped'); } catch (_) {} });
const p = await ctx.newPage();
const errs = [];
p.on('pageerror', e => errs.push(e.message));
let dialogs = [];
p.on('dialog', d => { dialogs.push(d.message()); d.accept(); });
await p.goto('http://127.0.0.1:8080/');
await p.waitForFunction(() => typeof ownDeviceId === 'function' && typeof queryDirectory === 'function');
sh(`reminal add owner ${await p.evaluate(() => ownDeviceId())} -y`);
spawn('reminal', ['daemon'], { detached: true, stdio: 'ignore' }).unref();
await sleep(1500);
sh('mkdir -p /root/work');
const made = sh('cd /root/work && reminal new work');
const sid = made.match(/Session:\s+([A-Z0-9]+)/)[1], pin = made.match(/PIN:\s*([0-9]+)/)[1];
await p.evaluate(s => connectOwned(s), sid);
check(await until(() => p.evaluate(async () => Object.keys(await loadOwnedMachines()).length === 1), 40), 'the browser owns the machine', 'no machine');
await until(() => p.evaluate(() => document.body.classList.contains('connected')), 20);
await sleep(800);
// A stand-in claude with a conversation in it, typed as a person would.
await p.evaluate(() => term.focus());
await p.keyboard.type('echo the-marker-before-sleep\n', { delay: 10 });
await p.keyboard.type('claude\n', { delay: 10 });
await sleep(2000);
await p.keyboard.type('remember 4242\n', { delay: 10 });
await sleep(2000);
check(convs().includes('you said: remember 4242'), 'claude is running with a conversation', convs());

// A guest, by link and PIN, watching it: through the sleep and the wake.
const gctx = await browser.newContext();
await gctx.addInitScript(() => { try { localStorage.setItem('reminal-tour-v1', 'skipped'); } catch (_) {} });
const g = await gctx.newPage();
g.on('pageerror', e => errs.push('guest: ' + e.message));
await g.goto(`http://127.0.0.1:8080/?s=${sid}#p=${pin}`);
check(await until(() => g.evaluate(() => document.body.classList.contains('connected')), 30), 'a guest viewer is watching it by link and PIN', 'guest not connected');
const screenOf = (pg) => pg.evaluate(() => { const b = term.buffer.active; let t = ''; for (let y = 0; y < b.length; y++) t += b.getLine(y).translateToString(true) + '\n'; return t; });
const key = await p.evaluate(async () => Object.keys(await loadOwnedMachines())[0]);
const listing = () => p.evaluate(async (k) => (await queryDirectory(base64ToU8(b64URLToStd(k)), {})).sessions, key);
const row = async (id) => (await listing()).find(s => s.id === id);

// Sleep from the viewer.
dialogs = []; // the first connect's "remember this machine?" is not the sleep's
await p.evaluate(async ([k, s]) => sleepOnMachine(k, s), [key, await row(sid)]);
check(await until(async () => (await row(sid))?.asleep, 20), 'Sleep from the viewer: the machine lists it asleep', JSON.stringify(await row(sid)));
check(existsSync(restore(sid, '.asleep')) && existsSync(restore(sid, '.sealed')), 'its record is kept, marked asleep', 'no record or mark');
check(!dialogs.length, 'nothing to let go of, so nothing was asked', dialogs.join(' | '));
// Its row: asleep, opened by waking, no rename.
const rowShape = await p.evaluate(([k, s]) => {
  const box = document.createElement('div');
  box.innerHTML = '<span class="machine-count"></span><div class="machine-sessions"></div>';
  renderMachineSessions(box, [s], k);
  const r = box.querySelector('.machine-session');
  return { asleep: r.classList.contains('is-asleep'), chip: r.querySelector('.ms-state')?.textContent, meta: r.querySelector('.ms-meta')?.textContent, buttons: [...r.querySelectorAll('.ms-btn')].map(b => b.title.split(' ')[0]) };
}, [key, await row(sid)]);
check(rowShape.asleep && rowShape.chip === 'asleep' && /open it to wake it/.test(rowShape.meta), 'its row says asleep, open it to wake it', JSON.stringify(rowShape));
check(!rowShape.buttons.some(b => /^Rename/.test(b)) && !rowShape.buttons.some(b => /^Sleep/.test(b)), 'no rename or sleep on a sleeping row', JSON.stringify(rowShape.buttons));

// Wake by opening it: its saved screen shows, and keys typed meanwhile wait
// for claude, then go to it, not to the shell it starts in.
const s0 = await row(sid);
const woke = p.evaluate(async ([k, s]) => wakeOwned(k, s), [key, s0]);
check(await until(() => p.evaluate(() => !document.getElementById('wake-screen').hidden), 10), 'opening it shows "waking"', 'no wake screen');
await woke;
const shown = await p.evaluate(() => document.querySelector('#wake-screen .wake-rows').textContent);
check(/the-marker-before-sleep|remember 4242/.test(shown), 'its last saved screen was shown while it woke', shown.slice(-200));
await p.evaluate(() => term.focus());
await p.keyboard.type('typed-while-waking\n', { delay: 5 });
check(await until(() => convs().includes('you said: typed-while-waking'), 40), 'keys typed while it woke went to claude once it was back', convs().split('\n').slice(-4).join(' | '));
check(await until(async () => !(await row(sid))?.asleep, 10), 'awake again, same id', JSON.stringify(await row(sid)));
check(!existsSync(restore(sid, '.asleep')), 'no longer marked asleep', 'still marked');
check(await p.evaluate(() => document.getElementById('wake-screen').hidden), 'the live terminal replaced the saved screen', 'wake screen still up');
check(await until(async () => (await screenOf(g)).includes('typed-while-waking'), 30), 'the guest viewer, open through it all, reconnected and sees it live', (await screenOf(g)).slice(-300));

// A job in it: asked, and slept anyway only when said yes; the row says what went.
await p.keyboard.type('\x04', { delay: 5 }); await sleep(800);       // leave claude
await p.keyboard.type('sleep 600 &\n', { delay: 5 }); await sleep(800);
dialogs = [];
await p.evaluate(async ([k, s]) => sleepOnMachine(k, s), [key, await row(sid)]);
check(dialogs.some(d => /sleep, running in it/.test(d) && /Sleep anyway/.test(d)), 'a job in it: the viewer asks, naming it', dialogs.join(' | '));
check(await until(async () => (await row(sid))?.asleep, 20), 'said yes: it sleeps', JSON.stringify(await row(sid)));
check(((await row(sid))?.dropped || []).some(d => /sleep, running in it/.test(d)), 'the listing says what it let go of', JSON.stringify(await row(sid)));

// A machine whose reminal is older than sleep says to update it.
check(await p.evaluate(() => dirActionError(Object.assign(new Error('this reminal does not answer dir_sleep'), { reply: { unsupported: true } }))) === 'reminal on that computer is too old for this · run reminal upgrade there', 'an older machine is told to upgrade, not "does not answer dir_sleep"', '');

// Opened from the session dock, a sleeping session is woken first, as from
// Machines: never dialled asleep, where nothing answers.
{
  sh('mkdir -p /root/fromdock');
  const m = sh('cd /root/fromdock && reminal new fromdock');
  const rid = m.match(/Session:\s+([A-Z0-9]+)/)[1];
  await p.evaluate(s => connectOwned(s), rid);
  await until(() => p.evaluate((s) => document.body.classList.contains('connected') && sessionId === s, rid), 20);
  await sleep(800);
  dialogs = [];
  await p.evaluate(async ([k, s]) => sleepOnMachine(k, s), [key, await row(rid)]);
  check(await until(async () => (await row(rid))?.asleep, 20), 'a session in the dock sleeps', JSON.stringify(await row(rid)));
  await p.evaluate((s) => sessdockConnect({ id: s, owned: true }), rid);
  check(await until(() => p.evaluate(() => !document.getElementById('wake-screen').hidden), 10), 'opened from the session dock, it shows "waking"', 'no wake screen');
  const pill = await p.evaluate(() => { const t = document.querySelector('#wake-screen .wake-title'); const cs = getComputedStyle(t); return { text: t.textContent, pos: cs.position, border: cs.borderTopWidth, radius: cs.borderTopLeftRadius }; });
  check(/^Waking .+…$/.test(pill.text) && pill.pos === 'absolute' && pill.border !== '0px' && pill.radius !== '0px', '"Waking …" is a pill in the middle, as "Connecting…" is', JSON.stringify(pill));
  check(await until(async () => !(await row(rid))?.asleep, 30), '…and it wakes', JSON.stringify(await row(rid)));
  check(await until(() => p.evaluate(() => document.getElementById('wake-screen').hidden), 30), '…and the live terminal replaces its saved screen', 'wake screen still up');
}

// A wake whose agent never comes back (here: no claude to start any more):
// the page keeps taking keys. What was held is sent to the shell on Send, or
// dropped on Discard; either way what is typed next runs there.
async function sleepWith(name) {
  sh(`mkdir -p /root/${name}`);
  const m = sh(`cd /root/${name} && reminal new ${name}`);
  const id = m.match(/Session:\s+([A-Z0-9]+)/)[1];
  await p.evaluate(s => connectOwned(s), id);
  await until(() => p.evaluate((s) => document.body.classList.contains('connected') && sessionId === s, id), 20);
  await sleep(800);
  await p.evaluate(() => term.focus());
  await p.keyboard.type('claude\n', { delay: 10 }); await sleep(2000);
  await p.keyboard.type(`about ${name}\n`, { delay: 10 }); await sleep(2000);
  dialogs = [];
  await p.evaluate(async ([k, s]) => sleepOnMachine(k, s), [key, await row(id)]);
  check(await until(async () => (await row(id))?.asleep, 20), `${name} sleeps`, JSON.stringify(await row(id)));
  return id;
}
const lost = await sleepWith('lostsend'), lost2 = await sleepWith('lostdrop');
sh('mv /usr/local/bin/claude /usr/local/bin/claude.away');
const stuckWake = async (id, typed) => {
  await p.evaluate(async ([k, s]) => wakeOwned(k, s), [key, await row(id)]);
  await sleep(4000);
  await p.evaluate(() => term.focus());
  await p.keyboard.type(typed, { delay: 5 });
  return until(() => p.evaluate(() => !document.getElementById('held-chip').hidden && !document.getElementById('held-send').hidden), 10);
};
check(await stuckWake(lost, 'echo sent-from-hold-$((40+2))\n'), 'its agent not back: what is typed is held, and Send is offered', await p.evaluate(() => document.getElementById('held-chip').outerHTML));
await p.click('#held-send');
check(await until(async () => (await screenOf(p)).includes('sent-from-hold-42'), 15), 'Send: what was held ran in the shell', (await screenOf(p)).slice(-300));
await p.keyboard.type('echo then-live-$((50+1))\n', { delay: 5 });
check(await until(async () => (await screenOf(p)).includes('then-live-51'), 15), '…and what is typed next goes straight there', (await screenOf(p)).slice(-300));
check(await stuckWake(lost2, 'echo never-sent\n'), 'another such wake holds what is typed', 'no chip');
await p.click('#held-discard');
await p.keyboard.type('echo after-discard-$((60+3))\n', { delay: 5 });
check(await until(async () => (await screenOf(p)).includes('after-discard-63'), 15), 'Discard stops the wait: what is typed next runs in the shell', (await screenOf(p)).slice(-300));
check(!(await screenOf(p)).includes('never-sent'), '…and what was discarded never ran', (await screenOf(p)).slice(-300));
sh('mv /usr/local/bin/claude.away /usr/local/bin/claude');

// The moon, pressed: at once a spinner and no second press; pressed twice
// fast, still one sleep asked for.
{
  sh('mkdir -p /root/twice');
  const m = sh('cd /root/twice && reminal new twice');
  const tid = m.match(/Session:\s+([A-Z0-9]+)/)[1];
  await p.evaluate(() => {
    window.__sleepAsks = 0;
    const real = queryDirectory;
    // A sleep's first ask is counted; sleep_anyway follows a yes to it.
    queryDirectory = async (mach, req) => { if (req && req.sleep && req.sleep.act === 'sleep') window.__sleepAsks++; return real(mach, req); };
    document.getElementById('recents').open = true;
    renderMachines();
  });
  const moon = (id) => p.locator('.machine-session').filter({ hasText: id }).locator('.ms-btn[title^="Sleep"], .ms-btn.busy');
  check(await until(async () => (await moon(tid).count()) === 1, 20), 'its row has the moon', 'no moon');
  await moon(tid).click();
  const st = await p.evaluate((id) => { const r = [...document.querySelectorAll('.machine-session')].find(x => x.textContent.includes(id)); const b = r && r.querySelector('.ms-btn.busy'); return { busy: !!b, disabled: !!(b && b.disabled), spin: !!(b && b.querySelector('.ms-spin')) }; }, tid);
  check(st.busy && st.disabled && st.spin, 'pressed, the moon turns into a spinner and takes no more presses', JSON.stringify(st));
  await p.evaluate((id) => { const r = [...document.querySelectorAll('.machine-session')].find(x => x.textContent.includes(id)); r.querySelector('.ms-btn.busy, .ms-btn[title^="Sleep"]')?.click(); }, tid); // pressed again, whatever it shows
  check(await until(async () => (await row(tid))?.asleep, 20), '…and it sleeps', JSON.stringify(await row(tid)));
  check(await p.evaluate(() => window.__sleepAsks) === 1, 'pressed twice, one sleep was asked for', String(await p.evaluate(() => window.__sleepAsks)));
  await p.evaluate(() => { document.getElementById('recents').open = false; });
}

// Kill on a sleeping session forgets it.
await p.evaluate(async ([k, id]) => queryDirectory(base64ToU8(b64URLToStd(k)), { kill: id }), [key, sid]);
check(await until(async () => !(await row(sid)), 10), 'kill on a sleeping session forgets it', JSON.stringify(await row(sid)));
check(!existsSync(restore(sid, '.sealed')) && !existsSync(restore(sid, '.asleep')), 'its record and mark are gone', 'left behind');

check(!errs.length, 'no page errors', errs.join(' | '));
await browser.close();
console.log(fails ? fails + ' failed' : 'all passed');
process.exit(fails ? 1 : 0);
