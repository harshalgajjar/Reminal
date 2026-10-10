// A changed band (a window_frame carrying bx/by/bw/bh of an fw×fh picture, cut
// against frame `base`) is drawn only over that very frame: in order, at its
// place, never stretched into the whole picture. One that arrives against any
// other picture (out of order, after a frame it never saw, after a resize) is
// not drawn, and the viewer asks for a whole frame instead (run.sh).
import { chromium } from 'playwright';
import http from 'node:http';
import { readFileSync } from 'node:fs';
const html = readFileSync('/t/index.html');
const srv = http.createServer((q, r) => { r.writeHead(200, { 'content-type': 'text/html' }); r.end(html); }).listen(8099);
const b = await chromium.launch();
const page = await b.newPage();
await page.goto('http://127.0.0.1:8099/');
const out = await page.evaluate(async () => {
  const sent = [];
  sendWin = (type, body) => { sent.push({ type, ...body }); }; // no host: what would go to it
  updatePaneOverlay = () => {};
  const jpeg = async (w, h, rgb, noisy) => {
    const c = new OffscreenCanvas(w, h);
    const x = c.getContext('2d');
    x.fillStyle = `rgb(${rgb.join(',')})`;
    x.fillRect(0, 0, w, h);
    if (noisy) for (let i = 0; i < 4000; i++) { x.fillStyle = `rgb(${i % 256},${(i * 7) % 256},${(i * 13) % 256})`; x.fillRect((i * 37) % w, (i * 53) % h, 3, 3); }
    const bl = await c.convertToBlob({ type: 'image/jpeg', quality: 1 });
    const u8 = new Uint8Array(await bl.arrayBuffer());
    let s = ''; for (const v of u8) s += String.fromCharCode(v);
    return btoa(s);
  };
  const RED = [220, 30, 30], GREEN = [30, 200, 30], BLUE = [30, 30, 220];
  const newPane = () => {
    panes.clear();
    const img = document.createElement('canvas');
    const p = { w: { id: 'w1', w: 0, h: 0 }, img, ctx: img.getContext('2d') };
    panes.set('w1', p);
    sent.length = 0;
    return p;
  };
  const full = async (seq, w, h, rgb, noisy) => ({ id: 'w1', seq, w, h, img: await jpeg(w, h, rgb, noisy) });
  const band = async (seq, base, x, y, w, h, fw, fh, rgb) => ({ id: 'w1', seq, w: fw, h: fh, base, bx: x, by: y, bw: w, bh: h, fw, fh, img: await jpeg(w, h, rgb) });
  const settle = () => new Promise((r) => setTimeout(r, 400));
  const px = (p, x, y) => Array.from(p.ctx.getImageData(x, y, 1, 1).data.slice(0, 3));
  const near = (a, e) => a.every((v, i) => Math.abs(v - e[i]) < 40);
  const asked = () => sent.some((m) => m.type === 'window_ack' && m.key);
  const res = {};

  // In order: the band lands at its place, the rest of the picture stays.
  let p = newPane();
  onWindowFrame(await full(1, 64, 48, RED), false);
  await settle();
  onWindowFrame(await band(2, 1, 16, 16, 16, 16, 64, 48, GREEN), false);
  await settle();
  res.inOrder = { size: [p.img.width, p.img.height], inside: px(p, 20, 20), outside: px(p, 2, 2), asked: asked() };
  res.inOrder.ok = p.img.width === 64 && p.img.height === 48 && near(res.inOrder.inside, GREEN) && near(res.inOrder.outside, RED) && !res.inOrder.asked;

  // Out of order: the band cut against frame 2 arrives before frame 2 does.
  p = newPane();
  onWindowFrame(await full(1, 64, 48, RED), false);
  await settle();
  const b2 = await band(2, 1, 0, 0, 16, 16, 64, 48, GREEN);
  const b3 = await band(3, 2, 32, 16, 16, 16, 64, 48, BLUE);
  onWindowFrame(b3, false);
  onWindowFrame(b2, false);
  await settle();
  res.outOfOrder = { size: [p.img.width, p.img.height], at3: px(p, 36, 20), asked: asked() };
  res.outOfOrder.ok = p.img.width === 64 && p.img.height === 48 && !near(res.outOfOrder.at3, BLUE) && res.outOfOrder.asked;
  // And the whole frame it asked for heals it.
  onWindowFrame(await full(4, 64, 48, GREEN), false);
  await settle();
  res.outOfOrder.healed = near(px(p, 36, 20), GREEN) && near(px(p, 2, 2), GREEN);
  res.outOfOrder.ok = res.outOfOrder.ok && res.outOfOrder.healed;

  // After a resize: a band cut against the picture before it is not drawn.
  p = newPane();
  onWindowFrame(await full(1, 64, 48, RED), false);
  await settle();
  onWindowFrame(await full(2, 96, 48, RED), false);
  await settle();
  onWindowFrame(await band(3, 2, 0, 0, 16, 16, 64, 48, BLUE), false);
  await settle();
  res.afterResize = { size: [p.img.width, p.img.height], corner: px(p, 4, 4), asked: asked() };
  res.afterResize.ok = p.img.width === 96 && p.img.height === 48 && near(res.afterResize.corner, RED) && res.afterResize.asked;

  // A band right behind a whole frame that is slower to decode still lands
  // on top of it, not under it.
  p = newPane();
  onWindowFrame(await full(1, 1100, 800, RED, true), false);
  onWindowFrame(await band(2, 1, 0, 0, 16, 16, 1100, 800, BLUE), false);
  await settle();
  res.behindSlowFull = { size: [p.img.width, p.img.height], corner: px(p, 4, 4) };
  res.behindSlowFull.ok = p.img.width === 1100 && near(res.behindSlowFull.corner, BLUE);

  // Every frame is acked, drawn or not: acks pace the host.
  res.acks = sent.filter((m) => m.type === 'window_ack' && !m.key).map((m) => m.seq);
  return res;
});
console.log(JSON.stringify(out));
const cases = ['inOrder', 'outOfOrder', 'afterResize', 'behindSlowFull'];
let ok = true;
for (const c of cases) { console.log((out[c].ok ? 'PASS  ' : 'FAIL  ') + c); ok = ok && out[c].ok; }
await b.close(); srv.close();
process.exit(ok ? 0 : 1);
