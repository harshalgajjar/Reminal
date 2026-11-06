// run.sh: `reminal integrate` sets Copilot CLI up; its sessions then say what
// it is doing from its own hooks, and one with no login reads as logged out.
import { execSync, spawn } from 'node:child_process';
import { readFileSync } from 'node:fs';

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
// The state the session's own hooks last wrote, '' with none.
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
// A session at work in its own folder with Copilot CLI up in it, past the
// folder trust question (its first option, "Yes", for this session).
async function copilotIn(name, provider) {
  sh(`mkdir -p /root/${name}`);
  const sid = sh(`cd /root/${name} && reminal new ${name}`).match(/Session:\s+([A-Z0-9]+)/)[1];
  const env = provider ? 'COPILOT_PROVIDER_BASE_URL=http://127.0.0.1:8080/v1 COPILOT_MODEL=gpt-4.1 COPILOT_PROVIDER_API_KEY=stand-in ' : '';
  await keys(sid, `${env}copilot`);
  await sleep(12000);
  await keys(sid, '');
  await sleep(4000);
  return sid;
}

// 1. integrate: its MCP server, through its own CLI, and its hooks.
const out = sh('reminal integrate copilot -y 2>&1 || true');
check(/✓ Copilot CLI/.test(out), `integrate says it set Copilot CLI up: ${JSON.stringify(out.split('\n').filter(l => /Copilot/.test(l)).join(' / '))}`);
let mcp = {};
try { mcp = JSON.parse(readFileSync('/root/.copilot/mcp-config.json', 'utf8')); } catch {}
const entry = (mcp.mcpServers || {}).reminal || {};
check(/reminal$/.test(entry.command || '') && JSON.stringify(entry.args) === '["mcp"]', `~/.copilot/mcp-config.json has reminal: ${JSON.stringify(entry)}`);
check(/\breminal\b/.test(sh('copilot mcp list 2>&1 || true')), 'copilot mcp list names reminal');
let hooks = {};
try { hooks = JSON.parse(readFileSync('/root/.copilot/hooks/reminal.json', 'utf8')); } catch {}
check(hooks.version === 1 && ['userPromptSubmitted', 'permissionRequest', 'agentStop'].every(e => (hooks.hooks || {})[e]), `~/.copilot/hooks/reminal.json: ${Object.keys(hooks.hooks || {}).join(', ') || '(none)'}`);

spawn('node', ['/e2e/model.mjs'], { env: { ...process.env, PORT: '8080' }, stdio: 'ignore', detached: true }).unref();

// 2. A turn that ends: done, as its hook said.
const hello = await copilotIn('hello', true);
await keys(hello, 'hello there');
let s = await settle(hello);
check(s.attn === 'done' && s.hook === 'done', `a finished turn: attn ${s.attn || '(none)'}, hook ${s.hook || '(none)'}, want done, done`);

// 3. A turn waiting on an approval: needs you, as its hook said.
const approve = await copilotIn('approve', true);
await keys(approve, 'run a command please');
s = await settle(approve);
check(s.attn === 'input' && s.hook === 'input', `waiting on an approval: attn ${s.attn || '(none)'}, hook ${s.hook || '(none)'}, want input, input`);

// 4. No login, no provider: logged out.
const out4 = await copilotIn('signedout', false);
s = await settle(out4);
check(s.attn === 'logged-out', `no login: attn ${s.attn || '(none)'}, want logged-out`);

console.log(fails ? fails + ' failed' : 'all passed');
process.exit(fails ? 1 : 0);
