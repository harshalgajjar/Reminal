// run.sh: Claude Code and Qwen Code, each set two jobs to run later; their
// sessions list them beside "done", apart from work running.
import { execSync, spawn } from 'node:child_process';
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';

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
const listRow = (sid) => sh('reminal list --all').split('\n').find(r => r.includes(sid)) || '';
const info = (sid) => JSON.parse(sh('reminal info --all --json')).find(s => s.id === sid) || {};

spawn('node', ['/e2e/model.mjs'], { env: { ...process.env, PORT: '8080' }, stdio: 'ignore', detached: true }).unref();
mkdirSync('/root/w', { recursive: true });

// Claude Code: its hooks from integrate, its first-run questions answered
// in its own config (onboarding done, this folder trusted, the stand-in's
// key accepted), CronCreate allowed.
const KEY = 'sk-ant-api03-standin-0000000000000000000000000000000000000000';
sh('reminal integrate claude -y');
const cj = JSON.parse(readFileSync('/root/.claude.json', 'utf8'));
Object.assign(cj, { hasCompletedOnboarding: true, theme: 'dark', customApiKeyResponses: { approved: [KEY.slice(-20)], rejected: [] } });
cj.projects = { ...(cj.projects || {}), '/root/w': { hasTrustDialogAccepted: true, hasCompletedProjectOnboarding: true } };
writeFileSync('/root/.claude.json', JSON.stringify(cj));
const cs = JSON.parse(readFileSync('/root/.claude/settings.json', 'utf8'));
cs.permissions = { allow: ['CronCreate'] };
writeFileSync('/root/.claude/settings.json', JSON.stringify(cs));
// Qwen Code: the stand-in as its provider, its hooks from integrate.
mkdirSync('/root/.qwen', { recursive: true });
writeFileSync('/root/.qwen/settings.json', JSON.stringify({ security: { auth: { selectedType: 'openai' } }, model: { name: 'gpt-4.1' }, ide: { hasSeenNudge: true } }));
sh('reminal integrate qwen -y');

const CASES = [
  { name: 'claude', start: `ANTHROPIC_API_KEY=${KEY} ANTHROPIC_BASE_URL=http://127.0.0.1:8080 DISABLE_AUTOUPDATER=1 claude` },
  { name: 'qwen', start: 'OPENAI_API_KEY=stand-in OPENAI_BASE_URL=http://127.0.0.1:8080/v1 OPENAI_MODEL=gpt-4.1 qwen --yolo' },
];
for (const c of CASES) {
  const sid = sh(`cd /root/w && reminal new ${c.name}`).match(/Session:\s+([A-Z0-9]+)/)[1];
  await keys(sid, c.start);
  await sleep(15000);
  await keys(sid, 'schedule the build checks');
  // Settled: its list row the same for 6s running.
  let row = '', same = 0;
  for (let i = 0; i < 60 && same < 6; i++) {
    await sleep(1000);
    const now = listRow(sid);
    same = now === row ? same + 1 : 0;
    row = now;
  }
  check(/\bdone\s+· 2 scheduled\s/u.test(row), `${c.name}: list row ${JSON.stringify(row.trim())}, want done · 2 scheduled`);
  const s = info(sid);
  check(s.scheduled?.count === 2 && !s.other && !s.shells && !s.agents, `${c.name}: info --json scheduled ${JSON.stringify(s.scheduled)}, nothing running (other ${JSON.stringify(s.other)})`);
}
console.log(fails ? fails + ' failed' : 'all passed');
process.exit(fails ? 1 : 0);
