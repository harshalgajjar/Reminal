// run.sh: `reminal integrate` sets Kilo Code CLI up; its sessions then say
// what it is doing from its plugin, and one whose key is refused reads as
// logged out.
import { execSync, spawn } from 'node:child_process';
import { readFileSync, writeFileSync, mkdirSync, existsSync } from 'node:fs';

let fails = 0;
const ok = (m) => console.log('ok   ' + m);
const fail = (m) => { console.log('FAIL ' + m); fails++; };
const check = (cond, m) => (cond ? ok : fail)(m);
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
const attnOf = (sid) => (JSON.parse(sh('reminal info --all --json')).find(s => s.id === sid) || {}).attn || '';
// The state the session's own plugin last reported, '' with none.
const hookOf = (sid) => {
  const f = sh(`find /root/.reminal -name 'hook-${sid}.state' 2>/dev/null || true`).trim();
  if (!f) return '';
  try { return JSON.parse(readFileSync(f, 'utf8')).state || ''; } catch { return ''; }
};
// Settled: the same reading for 6s running.
async function settle(sid) {
  let got = '', same = 0;
  for (let i = 0; i < 60 && same < 6; i++) {
    await sleep(1000);
    const now = `${attnOf(sid)}|${hookOf(sid)}`;
    same = now === got ? same + 1 : 0;
    got = now;
  }
  const [attn, hook] = got.split('|');
  return { attn, hook };
}
// With no network, a package registry that answers 404: Kilo installs its
// plugin kit into a config dir that has plugins, and would wait on one.
spawn('node', ['-e', "require('http').createServer((q,s)=>{s.writeHead(404);s.end('{}')}).listen(4873,'127.0.0.1')"], { stdio: 'ignore', detached: true }).unref();
const OFFLINE = 'npm_config_registry=http://127.0.0.1:4873/ BUN_CONFIG_REGISTRY=http://127.0.0.1:4873/ KILO_DISABLE_MODELS_FETCH=1 KILO_DISABLE_AUTOUPDATE=1 KILO_DISABLE_LSP_DOWNLOAD=1';

// The person's own config, comments and all: the stand-in as its provider,
// and a command asked about before it runs.
mkdirSync('/root/.config/kilo', { recursive: true });
const theirs = `{
  // the person's own
  "provider": { "standin": { "npm": "@ai-sdk/openai-compatible", "name": "Standin",
    "options": { "baseURL": "http://127.0.0.1:8080/v1", "apiKey": "stand-in" },
    "models": { "gpt-4.1": { "name": "gpt-4.1", "tool_call": true } } } },
  "model": "standin/gpt-4.1",
  "permission": { "bash": "ask" }
}
`;
writeFileSync('/root/.config/kilo/kilo.jsonc', theirs);

// 1. integrate: its server in a kilo.json of its own, and the plugin.
const out = sh('reminal integrate kilo -y 2>&1 || true');
check(/✓ Kilo Code CLI/.test(out), `integrate says it set Kilo Code CLI up: ${JSON.stringify(out.split('\n').filter(l => /Kilo/.test(l)).join(' / '))}`);
let cfg = {};
try { cfg = JSON.parse(readFileSync('/root/.config/kilo/kilo.json', 'utf8')); } catch {}
const entry = (cfg.mcp || {}).reminal || {};
check(entry.type === 'local' && /reminal$/.test((entry.command || [])[0] || '') && (entry.command || [])[1] === 'mcp', `~/.config/kilo/kilo.json has reminal: ${JSON.stringify(entry)}`);
check(readFileSync('/root/.config/kilo/kilo.jsonc', 'utf8') === theirs, 'the person\'s kilo.jsonc is untouched');
check(existsSync('/root/.config/kilo/plugin/reminal.js'), 'the plugin is in ~/.config/kilo/plugin');
check(/reminal.*connected/.test(sh(`${OFFLINE} timeout 90 kilo mcp list 2>&1 || true`).replace(/\x1b\[[0-9;]*m/g, '')), 'kilo mcp list: reminal connected');

spawn('node', ['/e2e/model.mjs'], { env: { ...process.env, PORT: '8080' }, stdio: 'ignore', detached: true }).unref();
async function kiloIn(name) {
  sh(`mkdir -p /root/${name}`);
  const sid = sh(`cd /root/${name} && reminal new ${name}`).match(/Session:\s+([A-Z0-9]+)/)[1];
  await keys(sid, `${OFFLINE} kilo`);
  await sleep(15000);
  return sid;
}

// 2. A turn that ends: done, as its plugin said.
const hello = await kiloIn('hello');
await keys(hello, 'hello there');
let s = await settle(hello);
check(s.attn === 'done' && s.hook === 'done', `a finished turn: attn ${s.attn || '(none)'}, hook ${s.hook || '(none)'}, want done, done`);

// 3. A turn waiting on an approval: needs you, as its plugin said.
const approve = await kiloIn('approve');
await keys(approve, 'run a command please');
s = await settle(approve);
check(s.attn === 'input' && s.hook === 'input', `waiting on an approval: attn ${s.attn || '(none)'}, hook ${s.hook || '(none)'}, want input, input`);

// 4. A key the service refuses: logged out.
const bad = await kiloIn('badkey');
await keys(bad, 'bad key here');
s = await settle(bad);
check(s.attn === 'logged-out', `a refused key: attn ${s.attn || '(none)'}, want logged-out`);

console.log(fails ? fails + ' failed' : 'all passed');
process.exit(fails ? 1 : 0);
