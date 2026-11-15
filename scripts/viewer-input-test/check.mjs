// run.sh: what a key typed into the viewer's terminal becomes.
import { chromium } from 'playwright';
import http from 'node:http';
import { readFileSync } from 'node:fs';
const html = readFileSync('/t/index.html');
http.createServer((q, r) => { const a = /^\/assets\/([\w.-]+\.js)$/.exec(q.url.split('?')[0]); if (a) { let b; try { b = readFileSync('/t/assets/' + a[1]); } catch (_) { r.writeHead(404); return r.end(); } r.writeHead(200, { 'content-type': 'text/javascript' }); return r.end(b); } if (q.url.split('?')[0] === '/reload.js') { r.writeHead(200, { 'content-type': 'text/javascript' }); return r.end(readFileSync('/t/reload.js')); } r.writeHead(200, { 'content-type': 'text/html' }); r.end(html); }).listen(8099);
const b = await chromium.launch();
let fails = 0;
const ok = (m) => console.log('ok   ' + m);
const fail = (m) => { console.log('FAIL ' + m); fails++; };

async function open(opts) {
  const ctx = await b.newContext(opts);
  await ctx.addInitScript(() => { try { localStorage.setItem('reminal-tour-v1', '1'); } catch (_) {} });
  const page = await ctx.newPage();
  await page.goto('http://127.0.0.1:8099/');
  await page.waitForFunction(() => typeof term === 'object' && term && document.querySelector('.xterm-helper-textarea'));
  // What is sent to the session: sendInput's own output, past every one of
  // its filters (the socket stood in for, the session taken as live).
  await page.evaluate(() => {
    window.__got = '';
    inputLive = () => true;
    deliverInput = (d) => { window.__got += d; };
  });
  return { ctx, page };
}

// ---- A desktop keyboard: every key arrives ----
{
  const { ctx, page } = await open({ viewport: { width: 1100, height: 700 } });
  await page.locator('.xterm-helper-textarea').focus();
  const got = async () => page.evaluate(() => window.__got);
  const typed = async (what, s, delay) => {
    await page.evaluate(() => { window.__got = ''; });
    await page.keyboard.type(s, { delay });
    await page.waitForTimeout(150);
    const g = await got();
    g === s ? ok(`desktop: ${what} arrives whole (${JSON.stringify(g)})`) : fail(`desktop: ${what}: typed ${JSON.stringify(s)}, got ${JSON.stringify(g)}`);
  };
  await typed('a doubled digit typed fast', '44', 0);
  await typed('"$((" typed fast', 'echo $((6*7))', 0);
  await typed('a doubled letter at a quick typist\'s pace', 'hello letter', 25);
  // A key held down: the keyboard repeats it every ~30 ms.
  await page.evaluate(() => { window.__got = ''; });
  for (let i = 0; i < 12; i++) { await page.keyboard.press('Minus'); await page.waitForTimeout(30); }
  await page.waitForTimeout(150);
  const held = await got();
  held === '-'.repeat(12) ? ok('desktop: a key held down (12 repeats, 30 ms apart) arrives 12 times') : fail(`desktop: a held key: 12 repeats gave ${JSON.stringify(held)}`);
  // A desktop IME's composition followed by the same character: not dropped.
  const kept = await page.evaluate(() => {
    const ta = document.querySelector('.xterm-helper-textarea');
    ta.dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true, data: '' }));
    ta.dispatchEvent(new CompositionEvent('compositionend', { bubbles: true, data: 'x' }));
    const e = new InputEvent('beforeinput', { bubbles: true, cancelable: true, inputType: 'insertText', data: 'x' });
    ta.dispatchEvent(e);
    return !e.defaultPrevented;
  });
  kept ? ok('desktop: an insertText after a composition of the same text is let through') : fail('desktop: an insertText after a composition was dropped (the Android filter ran on a desktop)');
  // The same key sent twice within 5 ms, each with its own keydown (an
  // automation, a fast repeat): both go.
  const fast = await page.evaluate(() => {
    window.__got = '';
    const ta = document.querySelector('.xterm-helper-textarea');
    ta.dispatchEvent(new KeyboardEvent('keydown', { bubbles: true, key: 'x' })); sendInput('x');
    ta.dispatchEvent(new KeyboardEvent('keydown', { bubbles: true, key: 'x' })); sendInput('x');
    return window.__got;
  });
  fast === 'xx' ? ok('desktop: a key sent twice within 5 ms, each with its keydown, is sent twice') : fail(`desktop: two keys within 5 ms gave ${JSON.stringify(fast)}`);
  await ctx.close();
}

// ---- Android (Samsung Keyboard's double-fire): still filtered ----
{
  const { ctx, page } = await open({
    viewport: { width: 412, height: 860 }, isMobile: true, hasTouch: true,
    userAgent: 'Mozilla/5.0 (Linux; Android 14; SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Mobile Safari/537.36',
  });
  const r = await page.evaluate(() => {
    const ta = document.querySelector('.xterm-helper-textarea');
    const insert = (d) => {
      const e = new InputEvent('beforeinput', { bubbles: true, cancelable: true, inputType: 'insertText', data: d });
      ta.dispatchEvent(e);
      return e.defaultPrevented;
    };
    const commit = (d) => {
      ta.dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true, data: '' }));
      ta.dispatchEvent(new CompositionEvent('compositionupdate', { bubbles: true, data: d }));
      ta.dispatchEvent(new CompositionEvent('compositionend', { bubbles: true, data: d }));
    };
    // A tap committed through composition, then its duplicate at once.
    commit('a');
    const letterDup = insert('a');
    // The same for punctuation.
    commit('.');
    const punctDup = insert('.');
    return { letterDup, punctDup };
  });
  r.letterDup ? ok('android: a letter\'s double-fire after its composition is dropped') : fail('android: a letter\'s double-fire after its composition got through');
  r.punctDup ? ok('android: a punctuation mark\'s double-fire after its composition is dropped') : fail('android: a punctuation mark\'s double-fire got through');
  // Two insertTexts of the same key in a row (no composition between): a key typed twice.
  await page.waitForTimeout(200);
  const twice = await page.evaluate(() => {
    const ta = document.querySelector('.xterm-helper-textarea');
    const insert = (d) => {
      const e = new InputEvent('beforeinput', { bubbles: true, cancelable: true, inputType: 'insertText', data: d });
      ta.dispatchEvent(e);
      return e.defaultPrevented;
    };
    return [insert('4'), insert('4')];
  });
  !twice[0] && !twice[1] ? ok('android: the same key twice as insertText (no composition) is two keys') : fail(`android: a key typed twice as insertText was dropped (${JSON.stringify(twice)})`);
  // xterm handing one tap to sendInput twice, with no event between (the
  // captured trace): the second is dropped.
  const dbl = await page.evaluate(() => {
    window.__got = '';
    document.querySelector('.xterm-helper-textarea').dispatchEvent(new KeyboardEvent('keydown', { bubbles: true, key: 'Unidentified' }));
    sendInput('.'); sendInput('.');
    return window.__got;
  });
  dbl === '.' ? ok('android: one tap handed to sendInput twice is sent once') : fail(`android: one tap handed twice gave ${JSON.stringify(dbl)}`);
  await ctx.close();
}
// ---- iOS: a held delete key goes on repeating ----
// iOS repeats a held delete key only while the field has text it can see:
// one Backspace, then nothing, on an empty field (iOS Simulator, iOS 17.2).
// What that needs of the page: the field never empty, its spaces kept, and
// every Backspace one \x7f for the shell.
{
  const { ctx, page } = await open({
    viewport: { width: 393, height: 852 }, isMobile: true, hasTouch: true,
    userAgent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_2 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Mobile/15E148 Safari/604.1',
  });
  const r = await page.evaluate(async () => {
    const ta = document.querySelector('.xterm-helper-textarea');
    ta.focus();
    const wait = (ms) => new Promise((res) => setTimeout(res, ms));
    const backspace = (repeat) => {
      const e = new KeyboardEvent('keydown', { bubbles: true, cancelable: true, key: 'Backspace', keyCode: 8, repeat });
      ta.dispatchEvent(e);
      return e;
    };
    const edit = (inputType, data) => {
      ta.dispatchEvent(new InputEvent('beforeinput', { bubbles: true, cancelable: true, inputType, data }));
    };
    const out = { whiteSpace: getComputedStyle(ta).whiteSpace };
    window.__got = '';
    ta.value = '';
    const first = backspace(false);
    out.first = { got: window.__got, field: ta.value, prevented: first.defaultPrevented };
    window.__got = '';
    for (let i = 0; i < 5; i++) { await wait(100); backspace(true); }
    out.repeats = { got: window.__got, field: ta.value };
    window.__got = '';
    edit('insertText', 'a'); edit('insertText', 'b');
    out.typed = { got: window.__got, field: ta.value };
    window.__got = '';
    for (const c of 'ls foo') edit('insertText', c);
    edit('deleteWordBackward', null);
    out.word = { got: window.__got, field: ta.value };
    window.__got = '';
    for (let i = 0; i < 'abls '.length; i++) edit('deleteContentBackward', null);
    out.emptied = { got: window.__got, field: ta.value };
    return out;
  });
  const bs = (n) => '\x7f'.repeat(n);
  r.whiteSpace === 'pre' ? ok('ios: the field keeps its spaces (white-space: pre)') : fail(`ios: the field's white-space is ${r.whiteSpace}: a field of spaces reads as empty`);
  r.first.got === bs(1) && r.first.field === ' ' && r.first.prevented
    ? ok('ios: a Backspace on an empty line is one \\x7f, and leaves the field a space')
    : fail(`ios: a Backspace on an empty line: sent ${JSON.stringify(r.first.got)}, field ${JSON.stringify(r.first.field)}, prevented ${r.first.prevented}`);
  r.repeats.got === bs(5) && r.repeats.field === ' '
    ? ok('ios: a held key\'s five repeats are five \\x7f, the field still a space')
    : fail(`ios: five repeats sent ${JSON.stringify(r.repeats.got)}, field ${JSON.stringify(r.repeats.field)}`);
  r.typed.got === 'ab' && r.typed.field === 'ab'
    ? ok('ios: typing after the space sends what was typed, never the space')
    : fail(`ios: typing "ab" after the space sent ${JSON.stringify(r.typed.got)}, field ${JSON.stringify(r.typed.field)}`);
  r.word.got === 'ls foo' + bs(3) && r.word.field === 'abls '
    ? ok('ios: a held key\'s word delete sends a \\x7f for each character of the word')
    : fail(`ios: a word delete after "ls foo" sent ${JSON.stringify(r.word.got)}, field ${JSON.stringify(r.word.field)}`);
  r.emptied.got === bs(5) && r.emptied.field === ' '
    ? ok('ios: deleting the last character leaves the field a space, so the key goes on repeating')
    : fail(`ios: emptying the line sent ${JSON.stringify(r.emptied.got)}, field ${JSON.stringify(r.emptied.field)}`);
  await ctx.close();
}
await b.close();
console.log(fails ? `FAIL (${fails})` : 'PASS');
process.exit(fails);
