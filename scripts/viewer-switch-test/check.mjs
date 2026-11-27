// Switching sessions (run.sh): to a session on another machine, or one not
// known to be on the same machine, the Windows and Apps panels drop the
// machine being left and say they are loading; a window-list request made
// there is settled with no answer (null), never the new machine's list, so an
// app launch begun there sends nothing to the new machine. Between two
// sessions on the same machine, the lists stay, so the panels open at once.
// A machine's directory listing a session another machine verifiably holds
// does not move it.
import { chromium } from 'playwright';
import http from 'node:http';
import { readFileSync } from 'node:fs';
const html = readFileSync('/t/index.html');
// The page as built: its own files by their paths (the terminal library
// among them: without it the page's script stops part way), the page itself
// for the rest.
const types = { js: 'text/javascript', css: 'text/css', json: 'application/json', html: 'text/html', wasm: 'application/wasm' };
const srv = http.createServer((q, r) => {
  const path = q.url.split('?')[0];
  if (/^\/[\w./-]+\.\w+$/.test(path) && !path.includes('..')) {
    try { const b = readFileSync('/t' + path); r.writeHead(200, { 'content-type': types[path.split('.').pop()] || 'application/octet-stream' }); return r.end(b); } catch (_) {}
  }
  r.writeHead(200, { 'content-type': 'text/html' }); r.end(html);
}).listen(8099);
const b = await chromium.launch();
const page = await b.newPage();
await page.goto('http://127.0.0.1:8099/');
const out = await page.evaluate(async () => {
  // No host or relay here: nothing is sent, no socket is opened.
  const sent = [];
  sendWin = (type, body) => { sent.push({ type, ...body }); };
  openSocket = () => {};
  const home = (sid, key) => { if (typeof noteSessionHome === 'function') noteSessionHome(sid, key); };
  home('MACAAAA1', 'bWFjLWtleQ'); home('MACAAAA2', 'bWFjLWtleQ'); home('CLOUDBB1', 'Y2xvdWQta2V5');
  const macWindows = { windows: [{ id: 'w1', app: 'Docker Desktop', title: 'Containers' }, { id: 'w2', app: 'Safari', title: 'News' }] };
  const macApps = { apps: [{ name: 'Finder' }, { name: 'Safari' }] };
  const shown = () => document.getElementById('windows-list').textContent;
  const apps = () => document.getElementById('apps-list').textContent;
  const onMac = () => {
    connectToSession('MACAAAA1', '123456');
    document.getElementById('windows').open = true;
    onWindowList(macWindows); onAppList(macApps);
  };
  const res = {};
  for (const [name, to] of [['other machine', 'CLOUDBB1'], ['unknown machine', 'ZZZZ9999'], ['same machine', 'MACAAAA2']]) {
    onMac();
    let answered;
    nextWindowList().then((w) => { answered = w; });
    sent.length = 0;
    let launched = false;
    launchApp({ id: 'safari', name: 'Safari' }).then(() => { launched = true; });
    connectToSession(to, '123456');
    const r = { windows: winList.length, apps: appList.length, list: shown(), appsList: apps() };
    onWindowList({ windows: [{ id: 'c1', app: 'Chromium', title: 'New Tab' }] });
    await new Promise((x) => setTimeout(x, 50));
    r.waiter = answered === undefined ? 'unsettled' : answered === null ? 'null' : answered.map((w) => w.id).join(',');
    r.launchSettled = launched;
    r.appOpenSent = sent.some((m) => m.type === 'app_open');
    pendingLaunch = null;
    res[name] = r;
  }
  // A verified home is not moved by a directory listing; an unknown one is filled.
  const dirList = [{ id: 'CLOUDBB1' }, { id: 'NEWMAC01' }];
  if (typeof noteDirectorySessions === 'function') noteDirectorySessions('bWFjLWtleQ', dirList);
  else for (const x of dirList) noteSessionHome(x.id, 'bWFjLWtleQ'); // what a directory reply did before
  res.directory = { cloudStillElsewhere: !onSameMachine('MACAAAA1', 'CLOUDBB1'), newFilled: onSameMachine('MACAAAA1', 'NEWMAC01') };
  return res;
});
console.log(JSON.stringify(out, null, 1));
const fails = [];
// A frame sealed as the session is left: send() and sendWin() check the
// socket, then await its sealing, then send — the socket closed meanwhile
// (null) threw "Cannot read properties of null (reading 'send')", and one
// opened meanwhile, the next session's, was sent a frame sealed for the last.
const race = await page.evaluate(async () => {
  const key = await crypto.subtle.generateKey({ name: 'AES-GCM', length: 256 }, false, ['encrypt', 'decrypt']);
  const sock = () => ({ readyState: WebSocket.OPEN, sent: [], send(f) { this.sent.push(f); } });
  const r = { threw: [], onNext: 0 };
  const settle = async (p) => { try { await p; } catch (e) { r.threw.push(String(e)); } };
  for (const go of [() => send({ type: 'resize', cols: 80, rows: 24 }), () => send({ type: 'data', data: btoa('x') }), () => sendWin('window_list', {})]) {
    authed = true; cryptoKey = key;
    ws = sock(); let p = go(); ws = null; await settle(p);
    ws = sock(); p = go(); const next = sock(); ws = next; await settle(p);
    r.onNext += next.sent.length;
  }
  ws = null; cryptoKey = null; authed = false;
  return r;
});
// An upload, and a paced snapshot's acknowledgement, across a switch: each
// chunk of a file was sealed with the key current then and sent on the
// socket current then — so a switch mid-file sent the rest of it to the next
// session, sealed for that session, and a batch went on with its other files
// there; an ack sealed for the session left went on the next one's socket.
const left = await page.evaluate(async () => {
  const keyA = await crypto.subtle.generateKey({ name: 'AES-GCM', length: 256 }, false, ['encrypt', 'decrypt']);
  const keyB = await crypto.subtle.generateKey({ name: 'AES-GCM', length: 256 }, false, ['encrypt', 'decrypt']);
  const kinds = (k) => k.sent.map(f => JSON.parse(f).type);
  // sockA switches the viewer to sockB (and its key) once it has sent `after` frames.
  const pair = (after) => {
    const b = { readyState: WebSocket.OPEN, sent: [], send(f) { this.sent.push(f); } };
    const a = { readyState: WebSocket.OPEN, sent: [], send(f) { this.sent.push(f); if (this.sent.length === after) { ws = b; cryptoKey = keyB; } } };
    authed = true; cryptoKey = keyA; ws = a;
    return { a, b };
  };
  const file = (n, size) => new File([new Uint8Array(size)], n);
  const r = {};
  window.prompt = () => '1h';
  // Not left: every chunk, and every file of a batch, sent.
  let p = pair(1000);
  await uploadFile(file('big.bin', 3 * UPLOAD_CHUNK_BYTES + 1), 0);
  await uploadBatch([file('one.bin', 10), file('two.bin', 10)]);
  r.stayed = kinds(p.a).filter(t => t === 'upload').length;
  // A file too large for an upload, mid-batch: said (an alert before, the
  // upload toast now), and the rest still sent.
  const alerts = []; window.alert = (m) => alerts.push(String(m));
  p = pair(1000);
  const mb = new Uint8Array(1 << 20);
  const huge = new File(Array.from({ length: (UPLOAD_MAX_BYTES >> 20) + 1 }, () => mb), 'huge.bin');
  await uploadBatch([file('one.bin', 10), huge, file('three.bin', 10)]);
  r.afterHuge = kinds(p.a).filter(t => t === 'upload').length; r.hugeSaid = alerts.some(m => /too large/.test(m)) || /too large/.test((document.getElementById('upload-toasts') || {}).textContent || '');
  p = pair(1);
  await uploadFile(file('big.bin', 3 * UPLOAD_CHUNK_BYTES + 1), 0);
  r.fileOnA = kinds(p.a).length; r.fileOnB = kinds(p.b).filter(t => t === 'upload').length;
  p = pair(1);
  await uploadBatch([file('one.bin', 10), file('two.bin', 10), file('three.bin', 10)]);
  r.batchOnA = kinds(p.a).length; r.batchOnB = kinds(p.b).filter(t => t === 'upload').length;
  // A batch begun while a switch is under way (switchingTo set; the page's
  // session still the one on its socket, with its key): it goes to that
  // session, and none of it to the one switched to when the switch lands
  // mid-batch.
  p = pair(1); switchingTo = 'BBBB2222';
  await uploadBatch([file('one.bin', 10), file('two.bin', 10)]);
  switchingTo = '';
  r.midSwitchOnA = kinds(p.a).filter(t => t === 'upload').length; r.midSwitchOnB = kinds(p.b).filter(t => t === 'upload').length;
  p = pair(1);
  const dt = new DataTransfer();
  dt.items.add(new File([new Uint8Array(10)], 'a.png', { type: 'image/png' }));
  dt.items.add(new File([new Uint8Array(10)], 'b.png', { type: 'image/png' }));
  document.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }));
  await new Promise(res => setTimeout(res, 300));
  r.pasteOnA = kinds(p.a).length; r.pasteOnB = kinds(p.b).filter(t => t === 'upload').length;
  // A paced snapshot: its screen, then a part — acknowledged as its sealing
  // is awaited, the session left meanwhile.
  p = pair(-1);
  await onPacedPart({ seq: 1 }, { screen: true, more: true, id: 'snapA' }, new Uint8Array([65]));
  const ack = onPacedPart({ seq: 2 }, { id: 'snapA', n: 3, i: 0 }, new Uint8Array([66]));
  ws = p.b; cryptoKey = keyB;
  await ack;
  r.ackOnB = kinds(p.b).filter(t => t === 'snapshot_ack').length;
  ws = null; cryptoKey = null; authed = false; snapLive = null;
  return r;
});
if (left.afterHuge !== 2 || !left.hugeSaid) fails.push('a too-large file mid-batch: ' + left.afterHuge + ' of the 2 others sent' + (left.hugeSaid ? '' : ', and it was not said'));
if (left.stayed !== 6) fails.push('an upload whose session was not left sent ' + left.stayed + ' of 6 chunks');
if (left.fileOnA !== 1 || left.fileOnB) fails.push('a file whose session was left mid-upload: ' + left.fileOnB + ' chunk(s) went to the next session (' + left.fileOnA + ' on its own)');
if (left.batchOnA !== 1 || left.batchOnB) fails.push('a batch whose session was left: ' + left.batchOnB + ' file(s) went to the next session');
if (left.midSwitchOnA !== 1 || left.midSwitchOnB) fails.push('a batch begun while a switch was under way: ' + left.midSwitchOnA + ' file(s) to the session it was begun on, ' + left.midSwitchOnB + ' to the one switched to');
if (left.pasteOnA !== 1 || left.pasteOnB) fails.push('pasted images whose session was left: ' + left.pasteOnB + ' went to the next session');
if (left.ackOnB) fails.push('a snapshot part acknowledged as its session was left: the ack went on the next one\'s socket');

if (race.threw.length) fails.push('a frame sealed as its session was left threw: ' + race.threw[0]);
if (race.onNext) fails.push(race.onNext + ' frame(s) sealed for the session left were sent on the next one\'s socket');

for (const k of ['other machine', 'unknown machine']) {
  const r = out[k];
  if (r.windows !== 0 || /Docker Desktop|Safari/.test(r.list)) fails.push(k + ': the machine left still listed in Windows');
  if (!/Loading windows/.test(r.list)) fails.push(k + ': Windows does not say it is loading');
  if (r.apps !== 0 || /Finder|Safari/.test(r.appsList)) fails.push(k + ': the machine left still listed in Apps');
  if (r.waiter !== 'null') fails.push(k + ': a window-list request made on the machine left was ' + (r.waiter === 'unsettled' ? 'never settled' : 'answered with ' + r.waiter));
  if (!r.launchSettled) fails.push(k + ': an app launch begun on the machine left never settled');
  if (r.appOpenSent) fails.push(k + ': an app launch begun on the machine left was sent to the new one');
}
if (out['same machine'].windows !== 2 || out['same machine'].apps !== 2) fails.push('same machine: its lists were dropped');
if (!out.directory.cloudStillElsewhere) fails.push('a directory listing moved a verified session to its machine');
if (!out.directory.newFilled) fails.push('a directory listing did not fill in an unknown session');
console.log(fails.length ? 'FAIL  ' + fails.join('; ') : 'PASS  a switch to another (or unknown) machine drops the machine left; to the same machine keeps it');
await b.close(); srv.close();
process.exit(fails.length ? 1 : 0);
