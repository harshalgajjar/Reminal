// The viewer's side of a snapshot in parts, driven in the page as served with
// no host behind it (run-parts.sh): parts are gathered and painted once;
// output that comes meanwhile is played after the paint, what the snapshot
// already shows dropped; a part that never comes is never a half paint — the
// plain snapshot is asked for instead; parts for another viewer are ignored.
// Paced (snap_ack): the screen painted the moment it comes, output during the
// history's download played as it comes, each part acknowledged, the history
// painted once with that output again on top; a history that never completes
// or cannot be unpacked leaves the screen live, with no plain snapshot asked.
import { chromium } from 'playwright';
import http from 'node:http';
import { readFileSync } from 'node:fs';
const html = readFileSync('/t/index.html');
const srv = http.createServer((q, r) => { const a = /^\/assets\/([\w.-]+\.js)$/.exec(q.url.split('?')[0]); if (a) { let b; try { b = readFileSync('/t/assets/' + a[1]); } catch (_) { r.writeHead(404); return r.end(); } r.writeHead(200, { 'content-type': 'text/javascript' }); return r.end(b); } if (q.url.split('?')[0] === '/reload.js') { r.writeHead(200, { 'content-type': 'text/javascript' }); return r.end(readFileSync('/t/reload.js')); } r.writeHead(200, { 'content-type': 'text/html' }); r.end(html); }).listen(8099);
const b = await chromium.launch();
const page = await b.newPage();
await page.goto('http://127.0.0.1:8099/');
const out = await page.evaluate(async () => {
  // No host: payloads arrive as a sealed frame's do once opened (openedFrame),
  // and what would be written to the terminal or sent to the host is
  // recorded.
  cryptoKey = {};
  const painted = [], resumes = [];
  const real = { enqueueData, flushPendingChunks };
  enqueueData = (bytes) => painted.push(new TextDecoder().decode(bytes));
  // The paced repaint writes past enqueueData (no prediction matching).
  flushPendingChunks = () => { for (const c of pendingChunks) painted.push(new TextDecoder().decode(c)); pendingChunks.length = 0; };
  ws = { readyState: WebSocket.OPEN, send: (t) => { const m = JSON.parse(t); resumes.push(m.type === 'snapshot_ack' ? { type: m.type, ...JSON.parse(m.data) } : m); } };
  encrypt = async (_k, s) => s; // an acknowledgement's payload, readable here
  const enc = (s) => new TextEncoder().encode(s);
  const gzip = async (s) => new Uint8Array(await new Response(new Blob([enc(s)]).stream().pipeThrough(new CompressionStream('gzip'))).arrayBuffer());
  const part = (id, i, n, chunk, to = viewerId, extra = {}) => {
    const h = enc(JSON.stringify({ id, i, n, to, ...extra }));
    const b = new Uint8Array(4 + h.length + chunk.length);
    new DataView(b.buffer).setUint32(0, h.length);
    b.set(h, 4); b.set(chunk, 4 + h.length);
    return openedFrame(b);
  };
  const data = (seq, s) => handleMessage({ type: 'data', seq, data: openedFrame(enc(s)) });
  const reset = () => { painted.length = 0; resumes.length = 0; lastSeq = 0; awaitingSnapshotUntil = Date.now() + 15000; snapAsm = null; snapLive = null; snapPartsOff = false; };
  const label = (p) => p === '\x1b[!p\x1b[r' ? 'RESET' : p.startsWith('\x1b[2J') ? (p.includes('line 1\r') ? 'HISTORY' : 'SCREEN') : p;
  const screenPart = (id, more) => part(id, 0, 0, enc('\x1b[2J\x1b[3JSCREEN'), viewerId, { screen: true, more });
  const res = {};

  // In two parts, with output arriving between them.
  reset();
  const snap = '\x1b[2J\x1b[3J' + Array.from({ length: 50 }, (_, i) => 'line ' + (i + 1)).join('\r\n');
  const gz = await gzip(snap), cut = Math.floor(gz.length / 2);
  await handleMessage({ type: 'snapshot_part', seq: 100, data: part('A', 0, 2, gz.slice(0, cut)) });
  await data(99, 'OLD');   // already in the snapshot: dropped
  await data(101, 'NEW1'); // after it: played after the paint
  const before = painted.length;
  await handleMessage({ type: 'snapshot_part', seq: 100, data: part('A', 1, 2, gz.slice(cut)) });
  await data(102, 'NEW2');
  res.twoParts = { paintedBeforeLastPart: before, order: painted.map((p) => p.startsWith('\x1b[2J') ? 'SNAPSHOT' : p), lastSeq };

  // Parts for another viewer.
  reset();
  await handleMessage({ type: 'snapshot_part', seq: 100, data: part('B', 0, 1, gz, 'v-someone-else') });
  res.otherViewer = { painted: painted.length, assembling: !!snapAsm };

  // A part that never comes.
  reset();
  await handleMessage({ type: 'snapshot_part', seq: 100, data: part('C', 0, 2, gz.slice(0, cut)) });
  await data(101, 'MEANWHILE');
  snapPartsGiveUp(); // what the wait's timer calls
  res.missingPart = { painted: painted.length, resume: resumes[resumes.length - 1] || null, lastSeq, assembling: !!snapAsm, partsOff: snapPartsOff };
  // A part that unpacks to far more than any snapshot (gzip of 32 MiB of
  // zeros, a few dozen KB): not painted, the plain snapshot asked for.
  reset();
  const bomb = new Uint8Array(await new Response(new Blob([new Uint8Array(32 << 20)]).stream().pipeThrough(new CompressionStream('gzip'))).arrayBuffer());
  await handleMessage({ type: 'snapshot_part', seq: 100, data: part('D', 0, 1, bomb) });
  res.bomb = { gz: bomb.length, painted: painted.length, resume: resumes[resumes.length - 1] || null, partsOff: snapPartsOff };

  // Paced: what the viewer asks for.
  reset();
  sendResume();
  res.pacedResume = resumes[0] || null;

  // Paced: the screen, output while the history comes, the history.
  reset();
  await handleMessage({ type: 'snapshot_part', seq: 100, data: screenPart('P', true) });
  const afterScreen = painted.map(label);
  await data(99, 'OLD');    // already in the screen: dropped
  await data(101, 'LIVE1'); // played at once, and kept
  const afterLive1 = painted.map(label);
  await handleMessage({ type: 'snapshot_part', seq: 100, data: part('P', 0, 2, gz.slice(0, cut), viewerId, { ack: true }) });
  await handleMessage({ type: 'snapshot_part', seq: 100, data: part('P', 0, 2, gz.slice(0, cut), viewerId, { ack: true }) }); // again: changes nothing
  await data(102, 'LIVE2');
  await handleMessage({ type: 'snapshot_part', seq: 100, data: part('P', 1, 2, gz.slice(cut), viewerId, { ack: true }) });
  await data(103, 'LIVE3'); // after the repaint: played once
  res.paced = { afterScreen, afterLive1, order: painted.map(label), acks: resumes.map((r) => r.type + ':' + r.snap + ':' + r.seq), lastSeq, live: !!snapLive };

  // Paced: parts of a snapshot whose screen was not painted here.
  reset();
  await handleMessage({ type: 'snapshot_part', seq: 100, data: part('Q', 0, 1, gz, viewerId, { ack: true }) });
  res.pacedStray = { painted: painted.length, sent: resumes.length };

  // Paced: a screen with nothing to follow keeps nothing.
  reset();
  await handleMessage({ type: 'snapshot_part', seq: 100, data: screenPart('R', false) });
  await data(101, 'LIVE');
  res.pacedNoHistory = { order: painted.map(label), live: !!snapLive };

  // Paced: a history that cannot be unpacked — the screen stays, live; no
  // plain snapshot asked for.
  reset();
  await handleMessage({ type: 'snapshot_part', seq: 100, data: screenPart('S', true) });
  await handleMessage({ type: 'snapshot_part', seq: 100, data: part('S', 0, 1, bomb, viewerId, { ack: true }) });
  await data(101, 'STILL');
  res.pacedBomb = { order: painted.map(label), resumes: resumes.filter((r) => r.type === 'resume').length, live: !!snapLive, partsOff: snapPartsOff };

  // Paced: more output kept than any snapshot — the history is given up.
  reset();
  await handleMessage({ type: 'snapshot_part', seq: 100, data: screenPart('T', true) });
  keepForSnapshot(101, new Uint8Array(SNAP_MAX_BYTES + 1));
  res.pacedOverKept = { live: !!snapLive };

  // Paced, in the real terminal: a scroll region set during the download
  // (ESC[3;10r, as `printf` in the shell would) must not keep the history in
  // its rows — the repaint puts the terminal back as a snapshot assumes it.
  reset();
  enqueueData = real.enqueueData; flushPendingChunks = real.flushPendingChunks;
  const settle = () => new Promise((ok) => term.write('', ok));
  term.reset();
  await handleMessage({ type: 'snapshot_part', seq: 100, data: screenPart('G', true) });
  await data(101, '\x1b[3;10r');
  await handleMessage({ type: 'snapshot_part', seq: 100, data: part('G', 0, 1, gz, viewerId, { ack: true }) });
  await new Promise((ok) => requestAnimationFrame(() => ok()));
  await settle();
  const seen = new Set();
  const buf = term.buffer.active;
  for (let i = 0; i < buf.length; i++) { const m = (buf.getLine(i)?.translateToString(true) || '').match(/^line (\d+)$/); if (m) seen.add(m[1]); }
  res.pacedGapRegion = { lines: seen.size };
  // The modes a program set at its start (bracketed paste, cursor keys,
  // focus, the mouse) are still set after the join's soft reset.
  reset();
  term.reset();
  await new Promise((ok) => term.write('\x1b[?2004h\x1b[?1h\x1b[?1004h\x1b[?1002h\x1b[?1006h', ok));
  const mouseBefore = term.modes.mouseTrackingMode;
  await handleMessage({ type: 'snapshot_part', seq: 100, data: screenPart('M', true) });
  await handleMessage({ type: 'snapshot_part', seq: 100, data: part('M', 0, 1, gz, viewerId, { ack: true }) });
  await new Promise((ok) => requestAnimationFrame(() => ok()));
  await settle();
  const md = term.modes;
  res.modesKept = { paste: md.bracketedPasteMode, keys: md.applicationCursorKeysMode, focus: md.sendFocusMode, mouse: md.mouseTrackingMode, mouseBefore };
  // An opened frame is only what the page opened itself: an object a message
  // carries in its JSON as if it were one ({"__plain": [...]}) is not taken
  // as decrypted output, whatever it holds.
  {
    reset();
    const before = painted.length;
    let threw = false;
    try { await decrypt({}, { __plain: [70, 79, 82, 71, 69, 68] }); } catch (_) { threw = true; }
    try { await handleMessage({ type: 'data', seq: 200, data: { __plain: [70, 79, 82, 71, 69, 68] } }); } catch (_) {}
    try { await handleMessage({ type: 'data', seq: 201, data: { __plain: Array.from(enc('FORGED')) } }); } catch (_) {}
    res.notOpened = { threw, painted: painted.slice(before).filter((p) => p.includes('FORGED')).length };
  }
  return res;
});
console.log(JSON.stringify(out));
const t = out.twoParts, o = out.otherViewer, m = out.missingPart;
const checks = [
  ['nothing painted before the last part', t.paintedBeforeLastPart === 0],
  ['the snapshot painted once, then only what came after it', JSON.stringify(t.order) === JSON.stringify(['SNAPSHOT', 'NEW1', 'NEW2'])],
  ['parts for another viewer are not painted or gathered', o.painted === 0 && !o.assembling],
  ['a missing part: nothing painted', m.painted === 0 && !m.assembling],
  ['a missing part: the plain snapshot asked for (resume from 0, no parts)', m.resume && m.resume.type === 'resume' && m.resume.from_seq === 0 && !m.resume.snap && m.partsOff],
  ['a part unpacking past the cap (gzip bomb): not painted, the plain snapshot asked for', out.bomb.painted === 0 && out.bomb.resume && out.bomb.resume.from_seq === 0 && !out.bomb.resume.snap && out.bomb.partsOff],
  ['paced: the resume asks for the screen first (snap_ack)', out.pacedResume && out.pacedResume.snap === 'gz+parts' && out.pacedResume.snap_ack === true],
  ['paced: the screen painted the moment it comes', JSON.stringify(out.paced.afterScreen) === '["SCREEN"]'],
  ['paced: output during the download played at once', JSON.stringify(out.paced.afterLive1) === '["SCREEN","LIVE1"]'],
  ['paced: the history painted once (after a reset), the output since again on top, nothing lost or doubled after', JSON.stringify(out.paced.order) === JSON.stringify(['SCREEN', 'LIVE1', 'LIVE2', 'RESET', 'HISTORY', 'LIVE1', 'LIVE2', 'LIVE3'])],
  ['paced: each part acknowledged with how many are in (a repeat changes nothing)', JSON.stringify(out.paced.acks) === JSON.stringify(['snapshot_ack:P:1', 'snapshot_ack:P:2'])],
  ['paced: done, nothing kept', !out.paced.live && out.paced.lastSeq === 103],
  ['paced: parts without their screen are not painted or acknowledged', out.pacedStray.painted === 0 && out.pacedStray.sent === 0],
  ['paced: a screen with no history to follow keeps nothing', JSON.stringify(out.pacedNoHistory.order) === '["SCREEN","LIVE"]' && !out.pacedNoHistory.live],
  ['paced: a history that cannot be unpacked leaves the screen live, no plain snapshot asked', JSON.stringify(out.pacedBomb.order) === '["SCREEN","STILL"]' && out.pacedBomb.resumes === 0 && !out.pacedBomb.live && !out.pacedBomb.partsOff],
  ['paced: more output kept than a snapshot holds gives the history up', !out.pacedOverKept.live],
  ['paced: a scroll region set during the download does not swallow the history (all 50 lines in the terminal)', out.pacedGapRegion.lines === 50],
  ['the modes a program set (bracketed paste, cursor keys, focus, the mouse) survive a join\'s soft reset', out.modesKept.paste && out.modesKept.keys && out.modesKept.focus && out.modesKept.mouse === out.modesKept.mouseBefore],
  ['data as {"__plain": …} in a message is not taken as opened: decrypt refuses it, nothing painted', out.notOpened.threw && out.notOpened.painted === 0],
];
for (const [n, ok] of checks) console.log((ok ? 'PASS  ' : 'FAIL  ') + n);
await b.close(); srv.close();
process.exit(checks.every(([, ok]) => ok) ? 0 : 1);
