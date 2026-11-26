// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// A TCP proxy for the viewer's leg to the relay, as from a phone: every chunk
// is held DELAY ms in each direction (RTT = 2 x DELAY), and with RATE (kbit/s,
// 0 = no limit) each direction carries no more than that. Strictly in order:
// one queue a direction, drained by one timer at a time (timers of different
// lengths that fall due together do not fire in the order they were set).
import net from 'node:net';
const [listen, target, delay, rate = 0] = process.argv.slice(2).map(Number);
net.createServer((a) => {
  const b = net.connect(target, '127.0.0.1');
  a.setNoDelay(true); b.setNoDelay(true);
  const pipe = (from, to) => {
    const q = []; let free = 0, running = false, ended = false;
    const drain = () => {
      if (!q.length) { running = false; if (ended) to.destroy(); return; }
      running = true;
      const wait = Math.max(0, q[0].at - Date.now());
      setTimeout(() => { const e = q.shift(); if (!to.destroyed) to.write(e.d); drain(); }, wait);
    };
    from.on('data', (d) => {
      const now = Date.now(), start = Math.max(now, free);
      free = start + (rate ? d.length * 8 / rate : 0);
      q.push({ d, at: free + delay });
      if (!running) drain();
    });
    from.on('close', () => { ended = true; if (!running) to.destroy(); }); from.on('error', () => {});
  };
  pipe(a, b); pipe(b, a);
}).listen(listen, '127.0.0.1');
