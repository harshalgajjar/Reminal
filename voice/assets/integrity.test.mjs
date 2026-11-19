// The voice frame's integrity check (internal/client/web/voice/integrity.js)
// against a server that lies: whatever it sends (its own Content-Length, an
// extra chunk, a longer body, a gzip-encoded body), a reader that stops once
// it has the bytes it was told to expect never holds a file that has not
// matched its pinned size and SHA-256. A good file still arrives whole.
// Run: node --test voice/assets/integrity.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import http from 'node:http';
import zlib from 'node:zlib';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const frame = process.env.VOICE_FRAME || path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'internal', 'client', 'web', 'voice');
// The frame's files are ES modules named .js, which an older node reads as
// CommonJS: copied as .mjs to load them here.
import fs from 'node:fs';
import os from 'node:os';
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'voice-integrity-'));
fs.writeFileSync(path.join(tmp, 'hashes.mjs'), fs.readFileSync(path.join(frame, 'hashes.js')));
fs.writeFileSync(path.join(tmp, 'integrity.mjs'), fs.readFileSync(path.join(frame, 'integrity.js'), 'utf8').replace("from './hashes.js'", "from './hashes.mjs'"));
const FILE = 'npm/@ricky0123/vad-web@0.0.31/dist/silero_vad_v5.onnx';
const good = crypto.randomBytes(300000);
const bad = Buffer.from(good); bad[1000] ^= 1;

const bodies = {
  good: [[good.subarray(0, 100000), good.subarray(100000)], { 'content-length': good.length }],
  'gzip good': [[zlib.gzipSync(good)], { 'content-encoding': 'gzip' }],
  'wrong body, short Content-Length': [[bad.subarray(0, 1000)], { 'content-length': 1000 }],
  'wrong body, then an extra chunk': [[bad, Buffer.from('xyz')], {}],
  'good body, then more': [[good, Buffer.from('x')], {}],
  'gzip wrong body': [[zlib.gzipSync(bad)], { 'content-encoding': 'gzip' }],
};

let serving = 'good', requests = 0;
const server = http.createServer(async (req, res) => {
  requests++;
  const [chunks, headers] = bodies[serving];
  res.writeHead(200, headers);
  for (const c of chunks) { res.write(c); await new Promise((r) => setTimeout(r, 5)); }
  res.end();
});
await new Promise((r) => server.listen(0, '127.0.0.1', r));
const base = `http://127.0.0.1:${server.address().port}`;

globalThis.self = globalThis;
// The Cache API, as a browser has it, enough for integrity.js: a copy is
// stored only once its body has been read to the end without error.
const stored = new Map();
globalThis.caches = { open: async () => ({
  match: async (key) => (stored.has(String(key)) ? new Response(stored.get(String(key))) : undefined),
  put: async (key, res) => { stored.set(String(key), new Uint8Array(await res.arrayBuffer())); },
}) };
const settle = () => new Promise((r) => setTimeout(r, 50)); // a store finishes after the read
self.location = new URL(base + '/voice/voice.html');
const { PINNED } = await import(pathToFileURL(path.join(tmp, 'hashes.mjs')));
PINNED[FILE] = [crypto.createHash('sha256').update(good).digest('hex'), good.length];
const integrity = await import(pathToFileURL(path.join(tmp, 'integrity.mjs')));
integrity.guardFetch();

// What a reader like transformers.js ends up holding: it stops at Content-Length.
async function readAsTransformersDoes() {
  stored.clear(); // each case starts from nothing kept
  const res = await fetch(`${base}/voice-assets/${FILE}`);
  const want = +res.headers.get('content-length'), reader = res.body.getReader(), parts = [];
  let n = 0;
  try {
    while (n < want) { const { done, value } = await reader.read(); if (done) break; parts.push(value); n += value.length; }
  } catch (e) { return { refused: e.message }; }
  return { held: Buffer.concat(parts) };
}

for (const name of Object.keys(bodies)) {
  test(name, async () => {
    serving = name;
    const got = await readAsTransformersDoes();
    if (name === 'good' || name === 'gzip good') assert.ok(got.held && got.held.equals(good), 'a good file arrives whole');
    else assert.ok(got.refused, `refused before the reader held ${got.held && got.held.length} bytes`);
  });
}

test('a size probe is answered from the pinned size, with no request', async () => {
  const before = requests;
  const probe = await fetch(`${base}/voice-assets/${FILE}`, { headers: { Range: 'bytes=0-0' } });
  assert.equal(probe.status, 206);
  assert.equal(probe.headers.get('content-range'), `bytes 0-0/${good.length}`);
  const head = await fetch(`${base}/voice-assets/${FILE}`, { method: 'HEAD' });
  assert.equal(head.headers.get('content-length'), String(good.length));
  const part = await fetch(`${base}/voice-assets/${FILE}`, { headers: { Range: 'bytes=10-20' } });
  assert.equal(part.status, 416, 'a part of a file cannot be checked');
  assert.equal(requests, before, 'nothing reached the server');
});

test('a checked copy is kept, a refused one is not, and a copy under another pin is not read', async () => {
  stored.clear();
  serving = 'wrong body, then an extra chunk';
  await fetch(`${base}/voice-assets/${FILE}`).then((r) => r.arrayBuffer()).catch(() => {});
  await settle();
  assert.equal(stored.size, 0, 'nothing kept of a refused file');
  serving = 'good';
  assert.ok(Buffer.from(await (await fetch(`${base}/voice-assets/${FILE}`)).arrayBuffer()).equals(good));
  await settle();
  assert.equal(stored.size, 1, 'the checked copy is kept');
  let before = requests;
  assert.ok(Buffer.from(await (await fetch(`${base}/voice-assets/${FILE}`)).arrayBuffer()).equals(good));
  assert.equal(requests, before, 'read from the kept copy, no request');
  // The pin changes (a new version of the file): the old copy is not read.
  const old = PINNED[FILE];
  PINNED[FILE] = [crypto.createHash('sha256').update(bad).digest('hex'), bad.length];
  before = requests;
  serving = 'good'; // still the old file on the server: refused against the new pin
  await assert.rejects((await fetch(`${base}/voice-assets/${FILE}`)).arrayBuffer());
  assert.equal(requests, before + 1, 'went to the server, not the copy kept under the old pin');
  PINNED[FILE] = old;
});

test.after(() => { server.close(); fs.rmSync(tmp, { recursive: true, force: true }); });
