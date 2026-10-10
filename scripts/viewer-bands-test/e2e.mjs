// e2e.sh's viewer: open the xterm's pane, type into it, and check that what
// arrived as changed bands built the window's own picture — the one a whole
// frame shows when the pane is opened afresh.
import { chromium } from 'playwright';
const [S, PIN] = process.argv.slice(2);
const b = await chromium.launch();
const page = await b.newPage({ viewport: { width: 1280, height: 900 } });
await page.goto(`http://127.0.0.1:18080/?s=${S}#p=${PIN}`, { waitUntil: 'domcontentloaded' });
await page.waitForFunction(() => typeof cryptoKey !== 'undefined' && cryptoKey, null, { timeout: 30000 });
const out = await page.evaluate(async () => {
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  const seen = { band: 0, whole: 0, bytesBand: 0, bytesWhole: 0, keyAsks: 0 };
  const orig = onWindowFrame;
  onWindowFrame = (payload, viaDC) => {
    if (payload.img) {
      if (payload.bw) { seen.band++; seen.bytesBand += payload.img.length; } else { seen.whole++; seen.bytesWhole += payload.img.length; }
    }
    return orig(payload, viaDC);
  };
  const origAsk = requestH264Key;
  requestH264Key = (p) => { seen.keyAsks++; return origAsk(p); };
  let list = [];
  for (let i = 0; i < 20 && !list.some((w) => /xterm/i.test(w.app + w.title)); i++) { list = await nextWindowList(); await sleep(250); }
  const w = list.find((x) => /xterm/i.test(x.app + x.title));
  if (!w) return { error: 'no xterm in ' + JSON.stringify(list) };
  openWinView(w);
  const p = panes.get(w.id);
  for (let i = 0; i < 100 && !p.gotFrame; i++) await sleep(100);
  if (!p.gotFrame) return { error: 'no first frame' };
  await sleep(500);
  const text = 'echo the quick brown fox jumps over the lazy dog 0123456789';
  for (const ch of text) { sendWin('window_input', { id: w.id, kind: 'key', text: ch }); await sleep(60); }
  sendWin('window_input', { id: w.id, kind: 'key', key: 'Return' });
  await sleep(2500);
  const grab = (cv) => cv.getContext('2d').getImageData(0, 0, cv.width, cv.height);
  const built = grab(p.img);
  const counts = { ...seen };
  // Opened afresh, the pane's first frame is whole: the window as it is.
  closePane(w.id);
  await sleep(800);
  openWinView(w);
  const q = panes.get(w.id);
  for (let i = 0; i < 100 && !q.gotFrame; i++) await sleep(100);
  await sleep(800);
  const fresh = grab(q.img);
  if (fresh.width !== built.width || fresh.height !== built.height) return { error: `sizes ${built.width}x${built.height} vs ${fresh.width}x${fresh.height}`, counts };
  let diff = 0, worst = 0;
  for (let i = 0; i < built.data.length; i += 4) {
    const d = (Math.abs(built.data[i] - fresh.data[i]) + Math.abs(built.data[i + 1] - fresh.data[i + 1]) + Math.abs(built.data[i + 2] - fresh.data[i + 2])) / 3;
    diff += d; if (d > worst) worst = d;
  }
  return { counts, size: [built.width, built.height], meanDiff: diff / (built.data.length / 4), worst };
});
console.log(JSON.stringify(out));
const ok = !out.error && out.counts.band >= 20 && out.meanDiff < 3;
console.log(ok ? `PASS  ${out.counts.band} bands (${out.counts.bytesBand} b64 bytes) and ${out.counts.whole} whole frames (${out.counts.bytesWhole}); the built picture is the window's (mean diff ${out.meanDiff.toFixed(2)})`
  : 'FAIL');
await b.close();
process.exit(ok ? 0 : 1);
