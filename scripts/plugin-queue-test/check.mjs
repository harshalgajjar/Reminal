// run.sh: the plugin integrate wrote, against a slow `reminal hook`, fed a
// turn's events: never two reports at once, and the last one written is the
// last state reported.
import { readFileSync, writeFileSync, chmodSync } from 'node:fs';

const plugin = '/root/.config/opencode/plugin/reminal.js';
const log = '/tmp/hook.log';
// A `reminal hook` that takes its time, as one does on a busy machine, and
// says when it starts and when it has written.
const slow = '/tmp/slow-hook';
writeFileSync(slow, `#!/bin/sh
echo "start $2 $(date +%s%N)" >> ${log}
sleep 0.$(shuf -i 5-25 -n 1)
cat > /dev/null
echo "end $2 $(date +%s%N)" >> ${log}
`);
chmodSync(slow, 0o755);
const src = readFileSync(plugin, 'utf8').replace(/^const BIN = .*$/m, `const BIN = ${JSON.stringify(slow)};`);
writeFileSync('/tmp/plugin.mjs', src);
process.env.REMINAL_SESSION = 'TEST2345';
const { Reminal } = await import('/tmp/plugin.mjs');
const { event } = await Reminal();
const sleep = (ms) => new Promise(r => setTimeout(r, ms));

let fails = 0;
const check = (cond, m) => { console.log((cond ? 'ok   ' : 'FAIL ') + m); if (!cond) fails++; };
for (let turn = 1; turn <= 5; turn++) {
  writeFileSync(log, '');
  const id = `ses_turn${turn}`;
  // As OpenCode and Kilo send a turn: busy, busy again as it goes, then idle
  // and session.idle, a millisecond apart.
  for (let i = 0; i < 4; i++) await event({ event: { type: 'session.status', properties: { sessionID: id, status: { type: 'busy' } } } });
  await event({ event: { type: 'session.status', properties: { sessionID: id, status: { type: 'idle' } } } });
  await event({ event: { type: 'session.idle', properties: { sessionID: id } } });
  for (let i = 0; i < 100 && !(readFileSync(log, 'utf8').match(/^end /gm) || []).length; i++) await sleep(50);
  await sleep(3000);
  const rows = readFileSync(log, 'utf8').trim().split('\n').map(r => r.split(' '));
  let inFlight = 0, most = 0, last = '';
  for (const [what, state] of rows) {
    inFlight += what === 'start' ? 1 : -1;
    most = Math.max(most, inFlight);
    if (what === 'end') last = state;
  }
  check(most === 1, `turn ${turn}: at most one report at a time (${most} at once, ${rows.length / 2} reports)`);
  check(last === 'done', `turn ${turn}: the last one written is done (${last || 'none'})`);
}
console.log(fails ? fails + ' failed' : 'all passed');
process.exit(fails ? 1 : 0);
