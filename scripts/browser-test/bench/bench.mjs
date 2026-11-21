// The same small tasks through reminal's browser tools and through
// Playwright MCP, each over one long-lived MCP connection: the time each
// takes and how much text comes back to the agent (what it pays for in
// tokens). Run by run.sh, in Docker.
//   node bench.mjs ours|playwright <out.json>
import { spawn } from 'node:child_process';
import fs from 'node:fs';

const WHO = process.argv[2], OUT = process.argv[3];
const PAGES = 'http://127.0.0.1:8088';
const RUNS = Number(process.env.RUNS || 5);

const cmd = WHO === 'ours'
  ? ['reminal', ['mcp']]
  : ['node', ['/pwmcp/node_modules/@playwright/mcp/cli.js', '--headless', '--isolated', '--executable-path', process.env.CHROME, '--no-sandbox']];
const proc = spawn(cmd[0], cmd[1], { stdio: ['pipe', 'pipe', 'ignore'] });
let buf = '', nextId = 0;
const waiting = new Map();
proc.stdout.on('data', (d) => {
  buf += d;
  for (let i; (i = buf.indexOf('\n')) >= 0;) {
    const line = buf.slice(0, i); buf = buf.slice(i + 1);
    try { const m = JSON.parse(line); if (m.id != null && waiting.has(m.id)) { waiting.get(m.id)(m); waiting.delete(m.id); } } catch (_) {}
  }
});
const rpc = (method, params) => new Promise((resolve) => { const id = ++nextId; waiting.set(id, resolve); proc.stdin.write(JSON.stringify({ jsonrpc: '2.0', id, method, params }) + '\n'); });
let chars = 0, calls = 0;
async function tool(name, args = {}) {
  const r = await rpc('tools/call', { name, arguments: args });
  const content = (r.result && r.result.content) || [];
  const text = content.filter((c) => c.type === 'text' && !/^⚠ reminal/.test(c.text)).map((c) => c.text).join('\n');
  chars += text.length; calls++;
  if (r.error || (r.result && r.result.isError)) throw new Error(`${name}: ${r.error ? r.error.message : text}`.slice(0, 300));
  return text;
}

// The two sets of tools, under one set of names.
const ours = {
  open: (url) => tool('browser_tabs_create', { url }),
  go: (url) => tool('browser_navigate', { url }),
  snapshot: () => tool('browser_read_page', { filter: 'interactive' }),
  ref: (snap, role, name) => (snap.match(new RegExp(`${role} "${name}"[^\\n]*\\[(ref_\\d+)\\]`)) || [])[1],
  type: (ref, what, text) => tool('browser_type', { ref, text }),
  select: (ref, what, value) => tool('browser_select_option', { ref, values: [value] }),
  click: (ref, what) => tool('browser_click', { ref }),
  fill: (fields) => tool('browser_fill_form', { fields: fields.map((f) => ({ ref: f.ref, value: f.value })) }),
  wait: (text) => tool('browser_wait_for', { text }),
  find: (q) => tool('browser_find', { query: q }),
  text: () => tool('browser_get_page_text'),
};
const playwright = {
  open: (url) => tool('browser_navigate', { url }),
  go: (url) => tool('browser_navigate', { url }),
  snapshot: () => tool('browser_snapshot'),
  ref: (snap, role, name) => (snap.match(new RegExp(`${role} "${name}"[^\\n]*\\[ref=(\\w+)\\]`)) || [])[1],
  type: (ref, what, text) => tool('browser_type', { element: what, target: ref, text }),
  select: (ref, what, value) => tool('browser_select_option', { element: what, target: ref, values: [value] }),
  click: (ref, what) => tool('browser_click', { element: what, target: ref }),
  fill: (fields) => tool('browser_fill_form', { fields: fields.map((f) => ({ name: f.what, type: f.type, target: f.ref, value: String(f.value) })) }),
  wait: (text) => tool('browser_wait_for', { text }),
  find: () => tool('browser_snapshot'), // it has no find: the agent reads the page again
  text: () => tool('browser_snapshot'), // nor a text reader
};
const B = WHO === 'ours' ? ours : playwright;
const must = (v, what) => { if (!v) throw new Error('not found: ' + what); return v; };

const TASKS = {
  // Order a pizza: read the form, type a name, choose a size, order, read the answer.
  'order a pizza (field by field)': async () => {
    await B.go(`${PAGES}/form.html`);
    const s = await B.snapshot();
    await B.type(must(B.ref(s, 'textbox', 'Name on the order'), 'name'), 'Name on the order', 'Ada Lovelace');
    await B.select(must(B.ref(s, 'combobox', 'Size'), 'size'), 'Size', 'Large');
    await B.click(must(B.ref(s, 'button', 'Place order'), 'order'), 'Place order');
    const t = await B.text();
    if (!/Ordered: Large for Ada Lovelace/.test(t)) throw new Error('the order did not go through');
  },
  // A longer form, filled in one call.
  'fill six fields (one call)': async () => {
    await B.go(`${PAGES}/tools.html`);
    const s = await B.snapshot();
    await B.fill([
      { what: 'First name', type: 'textbox', ref: must(B.ref(s, 'textbox', 'First name'), 'first'), value: 'Ada' },
      { what: 'City', type: 'textbox', ref: must(B.ref(s, 'textbox', 'City'), 'city'), value: 'Paris' },
      { what: 'Plan', type: 'combobox', ref: must(B.ref(s, 'combobox', 'Plan'), 'plan'), value: 'Plus' },
      { what: 'Newsletter', type: 'checkbox', ref: must(B.ref(s, 'checkbox', 'Newsletter'), 'news'), value: true },
    ]);
    const t = await B.text();
    if (!/first\W{1,6}Ada\W{1,8}city\W{1,6}Paris\W{1,8}plan\W{1,6}p\b/.test(t)) throw new Error('the form was not filled: ' + (t.match(/.{0,80}first.{0,120}/) || [t.slice(-200)])[0]);
  },
  // Press a button, wait for what it loads, find the next thing to press.
  'wait for a result, find a button': async () => {
    await B.go(`${PAGES}/tools.html`);
    const s = await B.snapshot();
    await B.click(must(B.ref(s, 'button', 'Load'), 'load'), 'Load');
    await B.wait('Loaded results');
    const f = await B.find('button ask');
    if (!/button "Ask"/.test(f)) throw new Error('the button was not found');
  },
  // Move through three pages, reading each.
  'three pages, read each': async () => {
    for (const p of ['form.html', 'toggle.html', 'tools.html']) { await B.go(`${PAGES}/${p}`); await B.text(); }
  },
};

await rpc('initialize', { protocolVersion: '2024-11-05', capabilities: {}, clientInfo: { name: 'bench', version: '1' } });
proc.stdin.write(JSON.stringify({ jsonrpc: '2.0', method: 'notifications/initialized' }) + '\n');
if (process.env.SCHEMAS) fs.writeFileSync(process.env.SCHEMAS, JSON.stringify(((await rpc('tools/list', {})).result.tools || []).filter((t) => /^browser_(type|click|select_option|fill_form|wait_for|navigate)$/.test(t.name)).map((t) => ({ name: t.name, schema: t.inputSchema })), null, 1));
const t0 = Date.now();
await B.open(`${PAGES}/form.html`);
const first = Date.now() - t0;
const out = { who: WHO, firstPageMs: first, tasks: {} };
for (const [name, run] of Object.entries(TASKS)) {
  const ms = [], ch = [], n = []; let failed = 0, why = '';
  for (let i = 0; i < RUNS; i++) {
    chars = 0; calls = 0;
    const t = Date.now();
    try { await run(); ms.push(Date.now() - t); ch.push(chars); n.push(calls); } catch (e) { failed++; why = String(e.message || e); }
  }
  const med = (l) => (l.length ? [...l].sort((a, b) => a - b)[Math.floor(l.length / 2)] : null);
  out.tasks[name] = { ms: med(ms), chars: med(ch), calls: med(n), failed, why };
}
fs.writeFileSync(OUT, JSON.stringify(out, null, 2));
proc.kill();
process.exit(0);
