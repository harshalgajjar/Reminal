// run.sh: a session whose Qwen Code ended its turn with monitors still
// running lists them beside its state; one that left none does not.
import { execSync, spawn } from 'node:child_process';
import { mkdirSync, writeFileSync } from 'node:fs';

let fails = 0;
const ok = (m) => console.log('ok   ' + m);
const fail = (m) => { console.log('FAIL ' + m); fails++; };
const sh = (c) => execSync(c, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
const sleep = (ms) => new Promise(r => setTimeout(r, ms));
// Typed into a session as reminal's MCP send_keys does.
function keys(session, text) {
  return new Promise((resolve) => {
    const p = spawn('reminal', ['mcp']);
    let out = '';
    p.stdout.on('data', d => { out += d; if (out.includes('"id":2')) { p.kill(); resolve(out); } });
    const send = (o) => p.stdin.write(JSON.stringify(o) + '\n');
    send({ jsonrpc: '2.0', id: 1, method: 'initialize', params: { protocolVersion: '2024-11-05', capabilities: {}, clientInfo: { name: 'rig', version: '1' } } });
    send({ jsonrpc: '2.0', method: 'notifications/initialized' });
    send({ jsonrpc: '2.0', id: 2, method: 'tools/call', params: { name: 'send_keys', arguments: { session, keys: text, enter: true } } });
    setTimeout(() => { p.kill(); resolve(out); }, 10000);
  });
}
const listRow = (sid) => sh('reminal list --all').split('\n').find(r => r.includes(sid)) || '';

// Its own settings: a provider of its own (the stand-in), and its hooks.
mkdirSync('/root/.qwen', { recursive: true });
writeFileSync('/root/.qwen/settings.json', JSON.stringify({ security: { auth: { selectedType: 'openai' } }, model: { name: 'gpt-4.1' }, ide: { hasSeenNudge: true } }));
sh('reminal integrate qwen -y');

const CASES = [
  { name: 'one', monitors: 1, shell: true, row: /\bdone\s+·.*\b1 shell\b.*\b1 monitor\s/u },
  { name: 'two', monitors: 2, shell: false, row: /\bdone\s+·.*\b2 monitors\s/u },
  { name: 'none', monitors: 0, shell: false, row: null },
];
let port = 8080;
for (const c of CASES) {
  port++;
  spawn('node', ['/e2e/model.mjs'], { env: { ...process.env, PORT: String(port), MONITORS: String(c.monitors), SHELL_TOO: c.shell ? '1' : '' }, stdio: 'ignore', detached: true }).unref();
  sh(`mkdir -p /root/w-${c.name}`);
  const sid = sh(`cd /root/w-${c.name} && reminal new qwen-${c.name}`).match(/Session:\s+([A-Z0-9]+)/)[1];
  await keys(sid, `OPENAI_API_KEY=stand-in OPENAI_BASE_URL=http://127.0.0.1:${port}/v1 OPENAI_MODEL=gpt-4.1 qwen --yolo`);
  await sleep(15000);
  await keys(sid, 'start the server and watch it');
  // Settled: its list row the same for 6s running.
  let row = '', same = 0;
  for (let i = 0; i < 60 && same < 6; i++) {
    await sleep(1000);
    const now = listRow(sid);
    same = now === row ? same + 1 : 0;
    row = now;
  }
  const rowOK = c.row ? c.row.test(row) : / done /.test(row) && !/monitor/.test(row);
  (rowOK ? ok : fail)(`${c.name}: list row ${JSON.stringify(row.trim())}${c.row ? ', want ' + c.row : ''}`);
}
console.log(fails ? fails + ' failed' : 'all passed');
process.exit(fails ? 1 : 0);
