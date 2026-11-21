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
  await new Promise((r) => setTimeout(r, 8)); // making takes a moment, as it does
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
const settle = () => new Promise((r) => setTimeout(r, 300));
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

// ---------- the order of work ----------
// The control for any change to how the worker takes its turns: an ordinary
// session's work comes out in the order it does today. A reply is made to
// its end before the next reply starts, and a clip is made only when no
// reply is open.
test('control: a reply is made to its end, then the next reply, then a clip asked for meanwhile', async () => {
  made.length = 0; out.length = 0;
  await tell({ type: 'push', gen: 20, text: 'One one. Two two. ' });
  await new Promise((r) => setTimeout(r, 3));
  await tell({ type: 'push', gen: 21, text: 'Next reply first. Next reply second. ' });  // the next reply's words arrive early
  await tell({ type: 'clip', text: 'Sure.', v: '' });                                    // and a clip is asked for
  await tell({ type: 'push', gen: 20, text: 'Three three. ' });
  await new Promise((r) => setTimeout(r, 40));
  await tell({ type: 'end', gen: 20 });
  await tell({ type: 'end', gen: 21 });
  await new Promise((r) => setTimeout(r, 200));
  assert.deepEqual(made.map((m) => m.text), ['One one.', 'Two two.', 'Three three.', 'Next reply first.', 'Next reply second.', 'Sure.']);
  assert.deepEqual(out.filter((m) => m.type === 'audio').map((m) => m.gen), [20, 20, 20, 21, 21], 'each piece goes back under its own reply');
  assert.deepEqual(out.filter((m) => m.type === 'done').map((m) => m.gen), [20, 21]);
});

// ---------- a waiting reply's first words, asked for early ----------
const order = () => made.map((m) => m.text);
const pause = (ms) => new Promise((r) => setTimeout(r, ms));

test('first: the waiting reply\'s first piece is made between two of the reply before it, and only that one', async () => {
  made.length = 0; out.length = 0;
  await tell({ type: 'push', gen: 30, text: 'A one. A two. A three. A four. ' });
  await pause(2);
  await tell({ type: 'push', gen: 31, text: 'B first. B second. B third. ', voice: 'af_bella' });
  await tell({ type: 'first', gen: 31 });
  await tell({ type: 'first', gen: 31 });   // asked twice: still one piece
  await pause(120);
  await tell({ type: 'end', gen: 30 });
  await tell({ type: 'end', gen: 31 });
  await pause(300);
  assert.deepEqual(order(), ['A one.', 'B first.', 'A two.', 'A three.', 'A four.', 'B second.', 'B third.']);
  assert.deepEqual(made.filter((m) => m.text.startsWith('B')).map((m) => m.voice), ['af_bella', 'af_bella', 'af_bella'], 'in its own voice, early and after');
  assert.deepEqual(out.filter((m) => m.type === 'audio').map((m) => m.gen), [30, 31, 30, 30, 30, 31, 31], 'each piece under its own reply');
  assert.deepEqual(out.filter((m) => m.type === 'done').map((m) => m.gen), [30, 31]);
});

test('first: asked before its first words are ready, it is made as soon as they are', async () => {
  made.length = 0; out.length = 0;
  await tell({ type: 'push', gen: 40, text: 'A one. A two. ' });
  await pause(2);
  await tell({ type: 'push', gen: 41, text: 'B fir' });       // not a sentence yet
  await tell({ type: 'first', gen: 41 });
  await pause(60);                                            // the reply before it waits for words
  assert.deepEqual(order(), ['A one.'], 'nothing of it is made from half a word');
  await tell({ type: 'push', gen: 41, text: 'st. B second. ' });
  await pause(60);
  assert.deepEqual(order(), ['A one.', 'B first.'], 'made while the reply before it is still open and waiting');
  await tell({ type: 'end', gen: 40 });
  await tell({ type: 'end', gen: 41 });
  await pause(300);
  assert.deepEqual(order(), ['A one.', 'B first.', 'A two.', 'B second.']);
});

test('first: a waiting reply given up is not made', async () => {
  made.length = 0; out.length = 0;
  await tell({ type: 'push', gen: 50, text: 'A one. A two. A three. ' });
  await pause(2);
  await tell({ type: 'push', gen: 51, text: 'B first. B second. ' });
  await tell({ type: 'first', gen: 51 });
  await tell({ type: 'cancel', gen: 51 });
  await pause(80);
  await tell({ type: 'end', gen: 50 });
  await pause(300);
  assert.deepEqual(order(), ['A one.', 'A two.', 'A three.']);
  assert.deepEqual(out.filter((m) => m.type === 'audio').map((m) => m.gen), [50, 50, 50]);
});

test('first for the reply that has the turn, or one not started, changes nothing', async () => {
  made.length = 0; out.length = 0;
  await tell({ type: 'first', gen: 99 });                     // no such reply
  await tell({ type: 'push', gen: 60, text: 'A one. A two. ' });
  await tell({ type: 'first', gen: 60 });                     // the one being made
  await tell({ type: 'end', gen: 60 });
  await pause(300);
  assert.deepEqual(order(), ['A one.', 'A two.']);
  assert.deepEqual(out.filter((m) => m.type === 'done').map((m) => m.gen), [60]);
});
