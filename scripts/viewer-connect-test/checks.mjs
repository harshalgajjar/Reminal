import { chromium, devices } from 'playwright';
const BASE = 'http://127.0.0.1:18080';
// OWNER_PKCS8/OWNER_RAW: an Ed25519 key run.sh enrolled as an owner of the
// container, installed below as this browser's device key.
const [A, APIN, B, BPIN, OWNER_PKCS8, OWNER_RAW] = process.argv.slice(2);
const b = await chromium.launch();
const out = [];

// ---- TEST 1: one PIN handshake per connect (was two) ----
{
  const ctx = await b.newContext({ ...devices['Pixel 7'] });
  const p = await ctx.newPage();
  let sends = 0;
  p.on('console', m => { if (/pake_init sent/.test(m.text())) sends++; });
  await p.goto(`${BASE}/?s=${A}&debug=true#p=${APIN}`, { waitUntil: 'domcontentloaded' });
  await p.waitForFunction(() => {
    const r = document.querySelector('.xterm-rows');
    return r && r.innerText.replace(/\s/g, '').length > 0;
  }, { timeout: 30000 }).catch(() => {});
  await p.waitForTimeout(2000);
  out.push(`TEST 1  pake_init sent per connect: ${sends}   ${sends === 1 ? 'PASS (was 2)' : 'FAIL'}`);
  await ctx.close();
}

// ---- TEST 2: a wrong PIN keeps saying PIN mismatch ----
{
  const ctx = await b.newContext({ ...devices['Pixel 7'] });
  const p = await ctx.newPage();
  const t0 = Date.now();
  await p.goto(`${BASE}/?s=${A}&debug=true#p=${BPIN}`, { waitUntil: 'domcontentloaded' });
  const seen = [];
  let prev = null;
  while (Date.now() - t0 < 16000) {
    const s = await p.evaluate(() => document.getElementById('status')?.textContent || '');
    if (s !== prev) { seen.push(`${Date.now() - t0}ms "${s}"`); prev = s; }
    await p.waitForTimeout(100);
  }
  const final = prev;
  out.push(`TEST 2  wrong PIN, status after 16s: "${final}"   ${/PIN mismatch/.test(final) ? 'PASS (no longer overwritten)' : 'FAIL'}`);
  out.push(`        timeline: ${seen.join(' → ')}`);
  await ctx.close();
}

// ---- TEST 3: Share must not hand out the previous session's PIN ----
{
  const ctx = await b.newContext({ ...devices['Pixel 7'] });
  const p = await ctx.newPage();
  await p.goto(`${BASE}/?s=${A}&debug=true#p=${APIN}`, { waitUntil: 'domcontentloaded' });
  await p.waitForFunction(() => {
    const r = document.querySelector('.xterm-rows');
    return r && r.innerText.replace(/\s/g, '').length > 0;
  }, { timeout: 30000 }).catch(() => {});
  const openShare = () => p.evaluate(() => {
    const btn = [...document.querySelectorAll('button,a,div')].find(e => /^\s*Share\s*$/.test(e.textContent || ''));
    if (btn) btn.click();
  });
  const shown = () => p.evaluate(() => ({
    session: document.getElementById('share-session')?.textContent || '',
    pin: document.getElementById('share-pin')?.textContent || '',
  }));
  await openShare(); await p.waitForTimeout(600);
  const first = await shown();
  await p.evaluate(() => { const m = document.getElementById('share-modal'); if (m) m.style.display = 'none'; });
  await p.evaluate(([id, pin]) => {
    const inp = document.getElementById('session-input');
    const pinEl = document.getElementById('pin-input');
    inp.value = id;
    pinEl.value = pin;
    pinEl.dispatchEvent(new Event('input', { bubbles: true }));
    inp.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
    const f = inp.closest('form'); if (f) f.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
  }, [B, BPIN]);
  await p.waitForTimeout(4000);
  await openShare(); await p.waitForTimeout(600);
  const after = await shown();
  const leaked = after.pin === APIN;
  const switched = after.session.toUpperCase() === B.toUpperCase();
  if (!switched) out.push(`        NOTE: did not switch (still on ${after.session}) — test inconclusive`);
  out.push(`TEST 3  on ${first.session} Share showed pin ${first.pin}; after switching to ${after.session} it shows "${after.pin}"`);
  out.push(`        ${leaked ? 'FAIL — still the old session\'s PIN' : 'PASS — old PIN no longer handed out'}`);
  await ctx.close();
}
// ---- TEST 4: keys typed while a PIN-free switch reads the owner key ----
// startConnect awaits the browser's owner key before it dials. Keys typed in
// that window must reach the session being joined: not land in the form
// field, not be dropped as "typed for the session being left", and not go
// down the old session's socket, which is still open when the switch was
// started from the session field rather than from a list.
{
  const ctx = await b.newContext({ ...devices['Desktop Chrome'] });
  // The first-visit tour takes focus; this is not a first visit.
  await ctx.addInitScript(() => { try { localStorage.setItem('reminal-tour-v1', 'skipped'); } catch (_) {} });
  const p = await ctx.newPage();
  await p.goto(`${BASE}/?debug=true`, { waitUntil: 'domcontentloaded' });
  await p.evaluate(async ([pk, raw]) => {
    const bin = s => Uint8Array.from(atob(s), c => c.charCodeAt(0));
    const privateKey = await crypto.subtle.importKey('pkcs8', bin(pk), { name: 'Ed25519' }, false, ['sign']);
    const publicKey = await crypto.subtle.importKey('raw', bin(raw), { name: 'Ed25519' }, true, ['verify']);
    await idbPut(await openOwnDB(), 'device', { privateKey, publicKey });
  }, [OWNER_PKCS8, OWNER_RAW]);
  const live = () => p.waitForFunction(() => inputLive(), null, { timeout: 30000 });
  // The first owner connect asks whether to trust the machine; say yes.
  p.on('dialog', d => d.accept());
  // run.sh reads /tmp/fk-<how> afterwards: the sessions the typed command ran in.
  const run = async (how, start) => {
    await p.goto(`${BASE}/?s=${A}&debug=true#p=${APIN}`, { waitUntil: 'domcontentloaded' });
    await live();
    await p.waitForTimeout(1500);
    // Hold the owner-key read open long enough to type into it.
    await p.evaluate(() => {
      const real = loadDeviceKeyPair;
      window.loadDeviceKeyPair = () => {
        window.loadDeviceKeyPair = real;
        window.fkOpen = true;
        return new Promise(r => setTimeout(r, 4000)).then(() => { window.fkOpen = false; return real(); });
      };
    });
    await p.evaluate(start, B);
    // At a person's pace: keys closer than ~20ms apart are taken as one.
    await p.keyboard.type(`echo $REMINAL_SESSION >> /tmp/fk-${how}`, { delay: 50 });
    await p.keyboard.press('Enter');
    const during = await p.evaluate(() => window.fkOpen ? 'the read was open' : 'AFTER the read (test too slow)');
    await p.waitForTimeout(8000);
    const field = await p.evaluate(() => document.getElementById('session-input').value);
    out.push(`TEST 4${how}  typed while ${during}; session field now "${field}" (verdict from run.sh)`);
  };
  // a: switching from a list (the old session is closed first); the click
  // took focus off the terminal
  await run('a', id => { document.activeElement.blur(); connectOwned(id); });
  // b: switching by typing the id in the session field and pressing Enter
  await run('b', id => {
    const inp = document.getElementById('session-input');
    inp.focus();
    inp.value = id;
    document.getElementById('pin-input').value = '';
    inp.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
  });
  await ctx.close();
}
// ---- TEST 5: two sessions on one machine are known to be on one machine ----
// A switch between sessions keeps the machine's window and app lists only
// when both are known to be on the same machine. Owner connects to A and then
// B (real handshakes, real machine key) must teach the page exactly that, and
// nothing about a session it has not seen.
{
  const ctx = await b.newContext({ ...devices['Desktop Chrome'] });
  await ctx.addInitScript(() => { try { localStorage.setItem('reminal-tour-v1', 'skipped'); } catch (_) {} });
  const p = await ctx.newPage();
  p.on('dialog', d => d.accept());
  await p.goto(`${BASE}/?debug=true`, { waitUntil: 'domcontentloaded' });
  await p.evaluate(async ([pk, raw]) => {
    const bin = s => Uint8Array.from(atob(s), c => c.charCodeAt(0));
    const privateKey = await crypto.subtle.importKey('pkcs8', bin(pk), { name: 'Ed25519' }, false, ['sign']);
    const publicKey = await crypto.subtle.importKey('raw', bin(raw), { name: 'Ed25519' }, true, ['verify']);
    await idbPut(await openOwnDB(), 'device', { privateKey, publicKey });
  }, [OWNER_PKCS8, OWNER_RAW]);
  const live = () => p.waitForFunction(() => inputLive(), null, { timeout: 30000 });
  await p.evaluate(id => connectOwned(id), A); await live();
  await p.evaluate(id => connectOwned(id), B); await live();
  const r = await p.evaluate(([a, bb]) => ({ same: onSameMachine(a, bb), unseen: onSameMachine(a, 'ZZZZ9999') }), [A, B]);
  const ok = r.same === true && r.unseen === false;
  if (!ok) process.exitCode = 1;
  out.push(`TEST 5  after owner connects to ${A} and ${B}: same machine ${r.same}, same as an unseen session ${r.unseen}   ${ok ? 'PASS' : 'FAIL'}`);
  await ctx.close();
}
console.log(out.join('\n'));
await b.close();
