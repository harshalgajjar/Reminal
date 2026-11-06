// run.sh: a session whose Codex ended its turn with background terminals
// still running lists them beside its state; one that left none does not.
import { execSync, spawn } from 'node:child_process';
import { mkdirSync, writeFileSync } from 'node:fs';

let fails = 0;
const ok = (m) => console.log('ok   ' + m);
const fail = (m) => { console.log('FAIL ' + m); fails++; };
const sh = (c) => execSync(c, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
const sleep = (ms) => new Promise(r => setTimeout(r, ms));
// Typed into a session as reminal's MCP send_keys does.
function keys(session, text, enter = true) {
  return new Promise((resolve) => {
    const p = spawn('reminal', ['mcp']);
    let out = '';
    p.stdout.on('data', d => { out += d; if (out.includes('"id":2')) { p.kill(); resolve(out); } });
    const send = (o) => p.stdin.write(JSON.stringify(o) + '\n');
    send({ jsonrpc: '2.0', id: 1, method: 'initialize', params: { protocolVersion: '2024-11-05', capabilities: {}, clientInfo: { name: 'rig', version: '1' } } });
    send({ jsonrpc: '2.0', method: 'notifications/initialized' });
    send({ jsonrpc: '2.0', id: 2, method: 'tools/call', params: { name: 'send_keys', arguments: { session, keys: text, enter } } });
    setTimeout(() => { p.kill(); resolve(out); }, 10000);
  });
}
const listRow = (sid) => sh('reminal list --all').split('\n').find(r => r.includes(sid)) || '';

// Each case: its own model and its own Codex home, pointed at it.
const CASES = [
  { name: 'one', calls: 1, row: /\bdone\s+· 1 background terminal\s/u },
  { name: 'two', calls: 2, row: /\bdone\s+· 2 background terminals\s/u },
  { name: 'none', calls: 0, row: null },
];
let port = 8080;
for (const c of CASES) {
  port++;
  spawn('node', ['/e2e/model.mjs'], { env: { ...process.env, PORT: String(port), CALLS: String(c.calls) }, stdio: 'ignore', detached: true }).unref();
  const home = `/root/codex-${c.name}`;
  const work = `/root/w-${c.name}`;
  mkdirSync(home, { recursive: true });
  mkdirSync(work, { recursive: true });
  writeFileSync(`${home}/config.toml`, `model = "stand-in"
model_provider = "standin"
approval_policy = "never"
sandbox_mode = "danger-full-access"
[model_providers.standin]
name = "stand-in"
base_url = "http://127.0.0.1:${port}/v1"
wire_api = "responses"
env_key = "STANDIN_KEY"
[projects."${work}"]
trust_level = "trusted"
`);
  const sid = sh(`cd ${work} && reminal new codex-${c.name}`).match(/Session:\s+([A-Z0-9]+)/)[1];
  await keys(sid, `STANDIN_KEY=x CODEX_HOME=${home} codex`);
  await sleep(8000);
  await keys(sid, 'start the dev server');
  // Settled: its list row the same for 6s running.
  let row = '', same = 0;
  for (let i = 0; i < 60 && same < 6; i++) {
    await sleep(1000);
    const now = listRow(sid);
    same = now === row ? same + 1 : 0;
    row = now;
  }
  const rowOK = c.row ? c.row.test(row) : / done /.test(row) && !/background/.test(row);
  (rowOK ? ok : fail)(`${c.name}: list row ${JSON.stringify(row.trim())}${c.row ? ', want ' + c.row : ''}`);
}
console.log(fails ? fails + ' failed' : 'all passed');
process.exit(fails ? 1 : 0);
