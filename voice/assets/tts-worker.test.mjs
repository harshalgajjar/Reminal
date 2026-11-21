// The voice's worker (internal/client/web/voice/tts-worker.js): which voice a
// reply and a clip are made in, with a stand-in for the model. A voice that
// cannot be had (no such voice, or its file not to be fetched) must not
// leave a reply unsaid: the voice in use says it, and the reply ends.
// Run: node --test voice/assets/tts-worker.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const frame = process.env.VOICE_FRAME || path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'internal', 'client', 'web', 'voice');
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'voice-worker-'));
fs.writeFileSync(path.join(tmp, 'tts-worker.mjs'), fs.readFileSync(path.join(frame, 'tts-worker.js'), 'utf8').replace(/from '\.\/([\w-]+)\.js'/g, "from './$1.mjs'"));
// The model: knows two voices, and fails for any other as the real one does.
fs.writeFileSync(path.join(tmp, 'kokoro.mjs'), `
export const made = [];
export async function load() {}
export async function generate(phonemes, { voice = 'af_heart', speed = 1 } = {}) {
  if (!['af_heart', 'af_bella'].includes(voice)) throw new Error('voice ' + voice + ': HTTP 404');
  made.push({ text: phonemes, voice, speed });
  return { audio: new Float32Array(240).fill(0.1), sampling_rate: 24000 };
}
`);
fs.writeFileSync(path.join(tmp, 'g2p.mjs'), 'export async function loadG2P() {}\nexport const phonemize = (t) => t;\n');
fs.writeFileSync(path.join(tmp, 'integrity.mjs'), 'export const describe = (e) => String((e && e.message) || e);\n');

const out = [];
globalThis.postMessage = (m) => out.push(m);
globalThis.onmessage = null;
await import(pathToFileURL(path.join(tmp, 'tts-worker.mjs')));
const { made } = await import(pathToFileURL(path.join(tmp, 'kokoro.mjs')));
const tell = (m) => globalThis.onmessage({ data: m });
const settle = () => new Promise((r) => setTimeout(r, 60));
async function reply(gen, text, own = {}) {
  made.length = 0; out.length = 0;
  for (const w of text.match(/\s*\S+/g)) await tell({ type: 'push', gen, text: w, ...own });
  await tell({ type: 'end', gen });
  await settle();
  return { made: [...made], audio: out.filter((m) => m.type === 'audio' && m.gen === gen), done: out.filter((m) => m.type === 'done' && m.gen === gen), errors: out.filter((m) => m.type === 'error') };
}

test('a reply with no voice of its own is made in the voice in use', async () => {
  const r = await reply(1, 'Hello there.');
  assert.deepEqual(r.made.map((m) => m.voice), ['af_heart']);
  assert.equal(r.audio.length, 1);
  assert.equal(r.done.length, 1);
});

test('a reply made in the voice and at the pace its first words named', async () => {
  const r = await reply(2, 'Hello there. And more.', { voice: 'af_bella', speed: 1.3 });
  assert.ok(r.made.length >= 1 && r.made.every((m) => m.voice === 'af_bella' && m.speed === 1.3), JSON.stringify(r.made));
  assert.equal(r.done.length, 1);
  const after = await reply(3, 'Back to it.');
  assert.deepEqual(after.made.map((m) => m.voice), ['af_heart'], 'the next reply is in the voice in use again');
});

test('a voice that cannot be had: the voice in use says it, all of it, and the reply ends', async () => {
  const r = await reply(4, 'First sentence here. Second sentence here.', { voice: 'xx_nobody' });
  assert.ok(r.made.length >= 1 && r.made.every((m) => m.voice === 'af_heart'), JSON.stringify(r.made));
  assert.equal(r.audio.length, r.made.length, 'its sound comes');
  assert.equal(r.audio.map((a) => a.text).join(' '), 'First sentence here. Second sentence here.', 'none of it is lost');
  assert.equal(r.done.length, 1, 'and it ends');
  assert.equal(r.errors.length, 0, 'with no error to show');
});

test('a clip is made in the voice it was asked for and answered under that name', async () => {
  made.length = 0; out.length = 0;
  await tell({ type: 'clip', text: 'Okay.', v: 'af_bella' });
  await tell({ type: 'clip', text: 'Sure.', v: '' });
  await settle();
  assert.deepEqual(made.map((m) => [m.text, m.voice]), [['Okay.', 'af_bella'], ['Sure.', 'af_heart']]);
  assert.deepEqual(out.filter((m) => m.type === 'clip').map((m) => [m.text, m.v]), [['Okay.', 'af_bella'], ['Sure.', '']]);
});
