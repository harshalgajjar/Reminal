// The voice's worker (internal/client/web/voice/tts-worker.js): which voice a
// reply and a clip are made in, and the order its work is done in, with a
// stand-in for the model. Every test has a worker of its own, so each stands
// on its own assertions.
// Run: node --test voice/assets/tts-worker.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const frame = process.env.VOICE_FRAME || path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'internal', 'client', 'web', 'voice');
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'voice-worker-'));
fs.writeFileSync(path.join(tmp, 'g2p.mjs'), 'export async function loadG2P() {}\nexport const phonemize = (t) => t;\n');
fs.writeFileSync(path.join(tmp, 'integrity.mjs'), 'export const describe = (e) => String((e && e.message) || e);\n');
const source = fs.readFileSync(path.join(frame, 'tts-worker.js'), 'utf8');

// A worker of its own: its model knows two voices and fails for any other, as
// the real one does (and for a piece with BOOM in it, whatever the voice);
// making a piece takes a moment, as it does. What it posts goes to `out`.
let n = 0;
const hooks = (globalThis.__voiceWorkers = {});
async function worker() {
  const id = ++n;
  fs.writeFileSync(path.join(tmp, `kokoro-${id}.mjs`), `
export const made = [];
export async function load() {}
export async function generate(phonemes, { voice = 'af_heart', speed = 1 } = {}) {
  if (!['af_heart', 'af_bella'].includes(voice)) throw new Error('voice ' + voice + ': HTTP 404');
  await new Promise((r) => setTimeout(r, 8));
  if (/BOOM/.test(phonemes)) throw new Error('the model failed');
  made.push({ text: phonemes, voice, speed });
  return { audio: new Float32Array(240).fill(0.1), sampling_rate: 24000 };
}
`);
  const w = (hooks[id] = { out: [], onmessage: null });
  // Its messages in and out are this worker's own, not the page's.
  const own = `const __w = globalThis.__voiceWorkers[${id}]; const postMessage = (m) => __w.out.push(m);\n` +
    source.replace(/from '\.\/kokoro\.js'/, `from './kokoro-${id}.mjs'`).replace(/from '\.\/([\w-]+)\.js'/g, "from './$1.mjs'").replace(/\nonmessage = /, '\n__w.onmessage = ');
  assert.ok(own.includes('__w.onmessage = '), 'the worker sets onmessage where this test expects');
  fs.writeFileSync(path.join(tmp, `tts-worker-${id}.mjs`), own);
  await import(pathToFileURL(path.join(tmp, `tts-worker-${id}.mjs`)));
  const { made } = await import(pathToFileURL(path.join(tmp, `kokoro-${id}.mjs`)));
  const out = w.out;
  return {
    made, out,
    tell: (m) => w.onmessage({ data: m }),
    order: () => made.map((m) => m.text),
    audio: () => out.filter((m) => m.type === 'audio').map((m) => m.gen),
    done: () => out.filter((m) => m.type === 'done').map((m) => m.gen),
    errors: () => out.filter((m) => m.type === 'error').map((m) => m.gen),
  };
}
const pause = (ms) => new Promise((r) => setTimeout(r, ms));
// The worker is free: a reply sent now is made and ends.
async function free(w, gen = 999) {
  await w.tell({ type: 'push', gen, text: 'After it all. ' });
  await w.tell({ type: 'end', gen });
  await pause(150);
  return w.order().includes('After it all.') && w.done().includes(gen);
}

// ---------- which voice ----------
test('a reply with no voice of its own is made in the voice in use', async () => {
  const w = await worker();
  await w.tell({ type: 'push', gen: 1, text: 'Hello there. ' });
  await w.tell({ type: 'end', gen: 1 });
  await pause(150);
  assert.deepEqual(w.made.map((m) => m.voice), ['af_heart']);
  assert.deepEqual(w.audio(), [1]);
  assert.deepEqual(w.done(), [1]);
});

test('a reply made in the voice and at the pace its first words named; the next in the voice in use', async () => {
  const w = await worker();
  await w.tell({ type: 'push', gen: 2, text: 'Hello there. And more. ', voice: 'af_bella', speed: 1.3 });
  await w.tell({ type: 'end', gen: 2 });
  await w.tell({ type: 'push', gen: 3, text: 'Back to it. ' });
  await w.tell({ type: 'end', gen: 3 });
  await pause(250);
  assert.deepEqual(w.made.map((m) => [m.voice, m.speed]), [['af_bella', 1.3], ['af_bella', 1.3], ['af_heart', 1]]);
  assert.deepEqual(w.done(), [2, 3]);
});

test('a voice that cannot be had: the voice in use says it, all of it, and the reply ends', async () => {
  const w = await worker();
  await w.tell({ type: 'push', gen: 4, text: 'First sentence here. Second sentence here. ', voice: 'xx_nobody' });
  await w.tell({ type: 'end', gen: 4 });
  await pause(250);
  assert.deepEqual(w.made.map((m) => m.voice), ['af_heart', 'af_heart']);
  assert.equal(w.out.filter((m) => m.type === 'audio').map((a) => a.text).join(' '), 'First sentence here. Second sentence here.', 'none of it is lost');
  assert.deepEqual(w.done(), [4], 'and it ends');
  assert.deepEqual(w.errors(), [], 'with no error to show');
});

test('a clip is made in the voice it was asked for and answered under that name', async () => {
  const w = await worker();
  await w.tell({ type: 'clip', text: 'Okay.', v: 'af_bella' });
  await w.tell({ type: 'clip', text: 'Sure.', v: '' });
  await pause(150);
  assert.deepEqual(w.made.map((m) => [m.text, m.voice]), [['Okay.', 'af_bella'], ['Sure.', 'af_heart']]);
  assert.deepEqual(w.out.filter((m) => m.type === 'clip').map((m) => [m.text, m.v]), [['Okay.', 'af_bella'], ['Sure.', '']]);
});

// ---------- the order of work: controls ----------
// These hold what an ordinary session relies on, and pass on the worker as it
// was before a waiting reply could be given a piece early.
test('control: a reply is made to its end, then the next reply, then a clip asked for meanwhile', async () => {
  const w = await worker();
  await w.tell({ type: 'push', gen: 20, text: 'One one. Two two. ' });
  await pause(3);
  await w.tell({ type: 'push', gen: 21, text: 'Next reply first. Next reply second. ' });
  await w.tell({ type: 'clip', text: 'Sure.', v: '' });
  await w.tell({ type: 'push', gen: 20, text: 'Three three. ' });
  await pause(40);
  await w.tell({ type: 'end', gen: 20 });
  await w.tell({ type: 'end', gen: 21 });
  await pause(250);
  assert.deepEqual(w.order(), ['One one.', 'Two two.', 'Three three.', 'Next reply first.', 'Next reply second.', 'Sure.']);
  assert.deepEqual(w.audio(), [20, 20, 20, 21, 21], 'each piece goes back under its own reply');
  assert.deepEqual(w.done(), [20, 21]);
});

test('control: words arriving one at a time, slower than they are made', async () => {
  const w = await worker();
  for (const word of 'The quick brown fox. It jumps over. The lazy dog. '.match(/\S+\s*/g)) { await w.tell({ type: 'push', gen: 1, text: word }); await pause(15); }
  await w.tell({ type: 'end', gen: 1 });
  await pause(150);
  assert.deepEqual(w.order(), ['The quick brown fox.', 'It jumps over.', 'The lazy dog.']);
  assert.deepEqual(w.done(), [1], 'one done');
  assert.ok(await free(w), 'and the worker is free');
});

test('control: the end arriving while it waits for words', async () => {
  const w = await worker();
  await w.tell({ type: 'push', gen: 1, text: 'A whole sentence. And the start of' });
  await pause(60);                       // the first is made; it waits for the rest
  assert.deepEqual(w.order(), ['A whole sentence.']);
  await w.tell({ type: 'end', gen: 1 });
  await pause(150);
  assert.deepEqual(w.order(), ['A whole sentence.', 'And the start of'], 'what was left is said');
  assert.deepEqual(w.done(), [1]);
  assert.ok(await free(w));
});

test('control: the end arriving while a piece is being made', async () => {
  const w = await worker();
  await w.tell({ type: 'push', gen: 1, text: 'First piece here. Second piece here. ' });
  await pause(3);                        // the first is being made (8 ms)
  await w.tell({ type: 'end', gen: 1 });
  await pause(150);
  assert.deepEqual(w.order(), ['First piece here.', 'Second piece here.']);
  assert.deepEqual(w.done(), [1]);
  assert.ok(await free(w));
});

test('control: a cancel while it waits for words', async () => {
  const w = await worker();
  await w.tell({ type: 'push', gen: 1, text: 'Said before it. And then' });
  await pause(60);
  await w.tell({ type: 'cancel', gen: 1 });
  await pause(100);
  assert.deepEqual(w.order(), ['Said before it.'], 'nothing more of it is made');
  assert.deepEqual(w.done(), [1], 'one done');
  assert.equal(w.out.find((m) => m.type === 'done').cancelled, true);
  assert.ok(await free(w));
});

test('control: a cancel while a piece is being made', async () => {
  const w = await worker();
  await w.tell({ type: 'push', gen: 1, text: 'First piece here. Second piece here. Third piece here. ' });
  await pause(3);
  await w.tell({ type: 'cancel', gen: 1 });
  await pause(100);
  assert.deepEqual(w.audio(), [], 'the piece being made is not sent');
  assert.deepEqual(w.done(), [1]);
  assert.ok(await free(w));
});

test('control: a flush with nothing to flush, and one for a reply that is gone', async () => {
  const w = await worker();
  await w.tell({ type: 'push', gen: 1, text: 'Only this. ' });
  await w.tell({ type: 'flush', gen: 1 });
  await pause(40);
  await w.tell({ type: 'flush', gen: 1 });   // nothing left
  await w.tell({ type: 'flush', gen: 77 });  // no such reply
  await pause(40);
  await w.tell({ type: 'end', gen: 1 });
  await pause(100);
  assert.deepEqual(w.order(), ['Only this.']);
  assert.deepEqual(w.done(), [1]);
  assert.ok(await free(w));
});

test('control: a piece the model fails on ends that reply with an error, and the next is made', async () => {
  const w = await worker();
  await w.tell({ type: 'push', gen: 1, text: 'Fine so far. BOOM here. Never said. ' });
  await w.tell({ type: 'end', gen: 1 });
  await pause(150);
  assert.deepEqual(w.order(), ['Fine so far.']);
  assert.deepEqual(w.errors(), [1]);
  assert.deepEqual(w.done(), [1]);
  assert.ok(await free(w));
});

// ---------- a waiting reply's first words, asked for early ----------
test('first: the waiting reply\'s first piece is made between two of the reply before it, and only that one', async () => {
  const w = await worker();
  await w.tell({ type: 'push', gen: 30, text: 'A one. A two. A three. A four. ' });
  await pause(2);
  await w.tell({ type: 'push', gen: 31, text: 'B first. B second. B third. ', voice: 'af_bella' });
  await w.tell({ type: 'first', gen: 31 });
  await w.tell({ type: 'first', gen: 31 });   // asked twice: still one piece
  await pause(120);
  await w.tell({ type: 'end', gen: 30 });
  await w.tell({ type: 'end', gen: 31 });
  await pause(300);
  assert.deepEqual(w.order(), ['A one.', 'B first.', 'A two.', 'A three.', 'A four.', 'B second.', 'B third.']);
  assert.deepEqual(w.made.filter((m) => m.text.startsWith('B')).map((m) => m.voice), ['af_bella', 'af_bella', 'af_bella'], 'in its own voice, early and after');
  assert.deepEqual(w.audio(), [30, 31, 30, 30, 30, 31, 31], 'each piece under its own reply');
  assert.deepEqual(w.done(), [30, 31]);
});

test('first: asked before its first words are ready, it is made as soon as they are', async () => {
  const w = await worker();
  await w.tell({ type: 'push', gen: 40, text: 'A one. A two. ' });
  await pause(2);
  await w.tell({ type: 'push', gen: 41, text: 'B fir' });       // not a sentence yet
  await w.tell({ type: 'first', gen: 41 });
  await pause(60);                                              // the reply before it waits for words
  assert.deepEqual(w.order(), ['A one.'], 'nothing of it is made from half a word');
  await w.tell({ type: 'push', gen: 41, text: 'st. B second. ' });
  await pause(60);
  assert.deepEqual(w.order(), ['A one.', 'B first.'], 'made while the reply before it is still open and waiting');
  await w.tell({ type: 'end', gen: 40 });
  await w.tell({ type: 'end', gen: 41 });
  await pause(300);
  assert.deepEqual(w.order(), ['A one.', 'B first.', 'A two.', 'B second.']);
  assert.deepEqual(w.done(), [40, 41]);
});

test('first: a waiting reply given up is not made', async () => {
  const w = await worker();
  await w.tell({ type: 'push', gen: 50, text: 'A one. A two. A three. ' });
  await pause(2);
  await w.tell({ type: 'push', gen: 51, text: 'B first. B second. ' });
  await w.tell({ type: 'first', gen: 51 });
  await w.tell({ type: 'cancel', gen: 51 });
  await pause(80);
  await w.tell({ type: 'end', gen: 50 });
  await pause(300);
  assert.deepEqual(w.order(), ['A one.', 'A two.', 'A three.']);
  assert.deepEqual(w.audio(), [50, 50, 50]);
  assert.ok(await free(w));
});

test('first for the reply that has the turn, or one not started, changes nothing', async () => {
  const w = await worker();
  await w.tell({ type: 'first', gen: 99 });                     // no such reply
  await w.tell({ type: 'push', gen: 60, text: 'A one. A two. ' });
  await w.tell({ type: 'first', gen: 60 });                     // the one being made
  await w.tell({ type: 'end', gen: 60 });
  await pause(200);
  assert.deepEqual(w.order(), ['A one.', 'A two.']);
  assert.deepEqual(w.done(), [60]);
  assert.ok(await free(w));
});

test('first: an early piece the model fails on goes back; the reply is made from its first words in its turn, where the failure is dealt with', async () => {
  const w = await worker();
  await w.tell({ type: 'push', gen: 70, text: 'A one. A two. A three. ' });
  await pause(2);
  await w.tell({ type: 'push', gen: 71, text: 'BOOM first. B second. ' });
  await w.tell({ type: 'first', gen: 71 });
  await pause(100);
  assert.deepEqual(w.order(), ['A one.', 'A two.'], 'the reply that has the turn goes on (its last sentence waits for its end)');
  assert.deepEqual(w.errors(), [], 'no error is reported for the waiting reply yet');
  await w.tell({ type: 'end', gen: 70 });
  await w.tell({ type: 'end', gen: 71 });
  await pause(250);
  assert.deepEqual(w.order(), ['A one.', 'A two.', 'A three.'], 'all of the first reply is made');
  assert.deepEqual(w.errors(), [71], 'it fails in its own turn, as any reply does');
  assert.ok(!w.order().includes('B second.'), 'and is not said with its first words missing');
  assert.deepEqual(w.done(), [70, 71]);
  assert.ok(await free(w));
});
