// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// run.sh: the real viewer, mirroring the probe window. Phases (each written
// to /shared/phase for procstat.py): the viewer joined with no pane open;
// the probe mirrored while still; changes the probe makes on its own after a
// few still seconds (passive); latency trials (a key or a click sent the
// way the pane sends one, then the first paint whose centre shows the
// probe's new colour); the probe mirrored while it moves. The latency and
// moving phases run again at each CPU throttle rate in THROTTLE (a slow
// phone). Last, inputs close together (rapid): a five-notch scroll and three
// clicks back to back. Writes /shared/viewer.json.
const { chromium } = require('playwright');
const fs = require('fs');
const join = fs.readFileSync('/shared/join.txt', 'utf8').trim();
const TRIALS = +(process.env.TRIALS || 40);
const SECS = +(process.env.PHASE_SECS || 15);
const RATES = (process.env.THROTTLE || '1').split(',').map(Number);
const phase = (n) => fs.writeFileSync('/shared/phase', n);
const sleep = (ms) => new Promise(r => setTimeout(r, ms));
(async () => {
  const out = { join: join.replace(/#.*/, '#…'), phases: {}, trials: {}, notes: [] };
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await b.newPage({ viewport: { width: 1280, height: 860 } });
  await page.addInitScript(() => { try { localStorage.setItem('reminal-tour-v1', 'done'); } catch (_) {} });
  // NORTC=1: a browser with no WebRTC at all, so frames AND input go over
  // the relay. P2P=0 alone only skips the wait: the peer-to-peer channel
  // still comes up and carries the input.
  if (process.env.NORTC === '1') await page.addInitScript(() => { try { delete window.RTCPeerConnection; window.RTCPeerConnection = undefined; window.webkitRTCPeerConnection = undefined; } catch (_) {} });
  const cdp = await page.context().newCDPSession(page);
  await cdp.send('Performance.enable');
  const metrics = async () => Object.fromEntries((await cdp.send('Performance.getMetrics')).metrics.map(m => [m.name, m.value]));
  // Main-thread busy time over a span, per second of wall time.
  const busy = (a, z, secs) => ({
    taskMsPerSec: +((z.TaskDuration - a.TaskDuration) * 1000 / secs).toFixed(1),
    scriptMsPerSec: +((z.ScriptDuration - a.ScriptDuration) * 1000 / secs).toFixed(1),
    layoutMsPerSec: +(((z.LayoutDuration - a.LayoutDuration) + (z.RecalcStyleDuration - a.RecalcStyleDuration)) * 1000 / secs).toFixed(1),
    jsHeapMB: +(z.JSHeapUsedSize / 1048576).toFixed(1),
  });
  out.h264Decode = await page.evaluate(async () => {
    try { return !!(window.VideoDecoder && (await VideoDecoder.isConfigSupported({ codec: 'avc1.4d0028', optimizeForLatency: true })).supported); } catch (_) { return false; }
  });
  await page.goto(join);
  await page.waitForFunction(() => agentLive && cryptoKey, null, { timeout: 30000 });

  phase('viewer-nopane');
  let m0 = await metrics(); await sleep(SECS * 1000);
  out.phases['viewer-nopane'] = busy(m0, await metrics(), SECS);

  const list = await page.evaluate(() => nextWindowList());
  const wins = (list && (list.windows || list)) || [];
  const w = (Array.isArray(wins) ? wins : []).find(x => /latency-probe/.test((x.title || '') + (x.app || '')));
  if (!w) { out.error = 'probe window not listed'; out.list = JSON.stringify(list).slice(0, 400); fs.writeFileSync('/shared/viewer.json', JSON.stringify(out, null, 1)); await b.close(); process.exit(1); }
  await page.evaluate((w) => openWinView(w), w);
  await page.waitForFunction((id) => { const x = panes.get(id); return x && x.gotFrame; }, w.id, { timeout: 30000 });
  // The peer-to-peer path is the normal one; wait for it (P2P=0 skips the
  // wait, to measure the relay path on purpose), and say how it ended.
  const t0p = Date.now();
  out.p2p = process.env.P2P === '0' || process.env.NORTC === '1' ? false : await page.waitForFunction(() => rtcActive(), null, { timeout: +(process.env.P2P_WAIT_MS || 45000) }).then(() => true, () => false);
  out.p2pAfterMs = Date.now() - t0p;
  out.rtcState = await page.evaluate(() => {
    try { return { pc: typeof pc !== 'undefined' && pc ? { conn: pc.connectionState, ice: pc.iceConnectionState, sig: pc.signalingState } : null, active: rtcActive() }; } catch (e) { return String(e); }
  });
  if (process.env.REQUIRE_P2P === '1' && !out.p2p) { out.error = 'no peer-to-peer path: ' + JSON.stringify(out.rtcState); fs.writeFileSync('/shared/viewer.json', JSON.stringify(out, null, 1)); await b.close(); process.exit(1); }
  out.inputChannel = await page.evaluate(() => !!(rtcInput && rtcInput.readyState === 'open'));
  // Every draw on the pane's canvas, with the colour at its centre.
  await page.evaluate((id) => {
    const p = panes.get(id); const ctx = p.ctx; const orig = ctx.drawImage.bind(ctx);
    window.__draws = []; window.__lastColor = 0;
    ctx.drawImage = function (...a) {
      const r = orig(...a);
      try {
        const cv = p.img; const d = ctx.getImageData(cv.width >> 1, cv.height >> 1, 1, 1).data;
        window.__lastColor = (d[0] + d[1] + d[2]) / 3 > 128 ? 1 : 0;
        window.__draws.push([performance.now(), window.__lastColor]);
        if (window.__draws.length > 20000) window.__draws.splice(0, 10000);
      } catch (_) {}
      return r;
    };
  }, w.id);
  await sleep(1500);
  out.drawsAfterHook = await page.evaluate(() => window.__draws.length);
  out.codec = await page.evaluate((id) => { const p = panes.get(id); return p && (p.decoder || p.h264 ? 'h264' : 'jpeg'); }, w.id);
  out.winStats = await page.evaluate(() => (typeof __winStats === 'function' ? __winStats() : null));

  phase('mirror-static');
  m0 = await metrics(); let d0 = await page.evaluate(() => __draws.length); await sleep(SECS * 1000);
  out.phases['mirror-static'] = { ...busy(m0, await metrics(), SECS), paintsPerSec: +((await page.evaluate(() => __draws.length) - d0) / SECS).toFixed(1) };

  // A change the window makes on its own after it has been still a while
  // (no input to wake anything): the probe flips itself, and the time to
  // the pane showing it is read against the probe's log (summarize.py).
  phase('passive');
  const passive = [];
  for (let i = 0; i < +(process.env.PASSIVE_TRIALS || 8); i++) {
    await sleep(4000 + Math.random() * 4000); // still for 4–8 s first
    const want = await page.evaluate(() => 1 - window.__lastColor);
    const w0 = Date.now();
    fs.writeFileSync('/shared/selfflip', '');
    const t1 = await page.evaluate(async (want) => {
      const t0 = performance.now();
      return await new Promise((res) => {
        (function chk() {
          if (window.__lastColor === want) return res(performance.timeOrigin + performance.now());
          if (performance.now() - t0 > 5000) return res(null);
          setTimeout(chk, 2);
        })();
      });
    }, want);
    passive.push({ w0, paintWall: t1 });
  }
  out.passive = passive;

  for (const rate of RATES) {
    const tag = rate === 1 ? '' : '@' + rate + 'x';
    await cdp.send('Emulation.setCPUThrottlingRate', { rate });
    phase('latency' + tag);
    const trials = [];
    // The frames drawn over the trials: over the relay each is a billed message.
    const draws0 = await page.evaluate(() => { window.__drawn = 0; const d = window.__draws; const push = d.push.bind(d); d.push = (...a) => { window.__drawn++; return push(...a); }; return 0; });
    for (let i = 0; i < TRIALS; i++) {
      const kind = i % 2 ? 'click' : 'key';
      trials.push(await page.evaluate(async ({ id, kind }) => {
        const want = 1 - window.__lastColor;
        const t0 = performance.now(), w0 = Date.now();
        if (kind === 'click') sendWin('window_input', { id, kind: 'click', x: 0.5, y: 0.6 });
        else sendWin('window_input', { id, kind: 'key', text: 'a' });
        const t1 = await new Promise((res) => {
          const until = t0 + 3000;
          (function chk() {
            const d = window.__draws;
            let i = Math.max(0, d.length - 1); while (i > 0 && d[i - 1][0] >= t0) i--;
            for (; i < d.length; i++) if (d[i][0] >= t0 && d[i][1] === want) return res(d[i][0]);
            if (performance.now() > until) return res(null);
            setTimeout(chk, 2);
          })();
        });
        return { kind, w0, g2g: t1 === null ? null : +(t1 - t0).toFixed(1), paintWall: t1 === null ? null : performance.timeOrigin + t1 };
      }, { id: w.id, kind }));
      await sleep(250 + Math.random() * 250);
      if (trials[trials.length - 1].g2g === null) await sleep(1500); // let a lost flip settle
    }
    out.trials['latency' + tag] = trials;
    (out.framesPerTrial = out.framesPerTrial || {})['latency' + tag] = +((await page.evaluate(() => window.__drawn)) / TRIALS).toFixed(2);

    fs.writeFileSync('/shared/anim', '');
    await sleep(2000);
    phase('mirror-moving' + tag);
    m0 = await metrics();
    await page.evaluate(() => { window.__draws.length = 0; });
    await sleep(SECS * 1000);
    const gaps = await page.evaluate(() => { const d = window.__draws; const g = []; for (let i = 1; i < d.length; i++) g.push(d[i][0] - d[i - 1][0]); return { n: d.length, g }; });
    fs.rmSync('/shared/anim', { force: true });
    const g = gaps.g.sort((a, b) => a - b);
    const mean = g.reduce((s, x) => s + x, 0) / Math.max(1, g.length);
    const sd = Math.sqrt(g.reduce((s, x) => s + (x - mean) ** 2, 0) / Math.max(1, g.length));
    out.phases['mirror-moving' + tag] = { ...busy(m0, await metrics(), SECS), fps: +(gaps.n / SECS).toFixed(1), gapMeanMs: +mean.toFixed(1), gapSdMs: +sd.toFixed(1), gapP95Ms: +(g[Math.floor(g.length * 0.95)] || 0).toFixed(1) };
    await sleep(1500);
  }
  await cdp.send('Emulation.setCPUThrottlingRate', { rate: 1 });

  // Inputs that come close together: a five-notch scroll in one message, and
  // three clicks sent back to back. What counts is when the LAST one reached
  // the window (summarize.py reads probe.log).
  phase('rapid');
  const rapid = { scroll: [], clicks: [] };
  for (let i = 0; i < +(process.env.RAPID_TRIALS || 10); i++) {
    rapid.scroll.push(await page.evaluate((id) => {
      const w0 = Date.now();
      sendWin('window_input', { id, kind: 'scroll', x: 0.5, y: 0.6, dx: 0, dy: (Math.random() < 0.5 ? 1 : -1) * 200 });
      return { w0 };
    }, w.id));
    await sleep(2500);
    rapid.clicks.push(await page.evaluate((id) => {
      const w0 = Date.now();
      for (let k = 0; k < 3; k++) sendWin('window_input', { id, kind: 'click', x: 0.5, y: 0.6 });
      return { w0 };
    }, w.id));
    await sleep(2500);
  }
  out.rapid = rapid;
  phase('done');
  fs.writeFileSync('/shared/viewer.json', JSON.stringify(out, null, 1));
  await b.close();
})().catch((e) => { fs.writeFileSync('/shared/viewer.json', JSON.stringify({ error: String(e && e.stack || e) })); process.exit(1); });
