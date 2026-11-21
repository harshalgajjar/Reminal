// The voice frame's speaker (internal/client/web/voice/speaker.js) and its
// short clips ("Okay.", "Mm-hm.", "Sorry,"): made once for each voice and
// kept, and for a new voice made a moment later and not while a reply is
// being made, so a change of voice does not hold up the next words.
// Driven on a made-up clock, with a stand-in for the voice's worker.
// Run: node --test voice/assets/speaker.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const frame = process.env.VOICE_FRAME || path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'internal', 'client', 'web', 'voice');

// ---------- a made-up clock ----------
let clock = 0, timers = [], nextId = 1;
globalThis.setTimeout = (f, ms = 0) => { const id = nextId++; timers.push({ id, at: clock + Math.max(0, ms), f }); return id; };
globalThis.clearTimeout = (id) => { timers = timers.filter((x) => x.id !== id); };
globalThis.setInterval = (f, ms) => { const id = nextId++; const tick = () => { f(); timers.push({ id, at: clock + ms, f: tick }); }; timers.push({ id, at: clock + ms, f: tick }); return id; };
globalThis.clearInterval = globalThis.clearTimeout;
function advance(ms) {
  const until = clock + ms;
  for (;;) {
    timers.sort((a, b) => a.at - b.at || a.id - b.id);
    if (!timers.length || timers[0].at > until) break;
    const x = timers.shift();
    clock = Math.max(clock, x.at);
    x.f();
  }
  clock = until;
}

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'voice-speaker-'));
for (const name of ['tuning', 'speakable', 'speaker']) {
  fs.writeFileSync(path.join(tmp, name + '.mjs'), fs.readFileSync(path.join(frame, name + '.js'), 'utf8').replace(/from '\.\/([\w-]+)\.js'/g, "from './$1.mjs'"));
}
// The loudness helpers, without the frame's messaging behind them.
fs.writeFileSync(path.join(tmp, 'level.mjs'), 'export const levelOf = () => 0;\nexport const rmsOf = (x) => { let s = 0; for (const v of x) s += v * v; return Math.sqrt(s / Math.max(1, x.length)); };\n');
const { Speaker, CLIPS } = await import(pathToFileURL(path.join(tmp, 'speaker.mjs')));
const ALL = [...CLIPS.cue, ...CLIPS.ack, ...CLIPS.nod];

// The audio context, as far as the speaker uses it here.
const node = () => ({ gain: { value: 1, setTargetAtTime() {}, setValueAtTime() {}, linearRampToValueAtTime() {}, cancelScheduledValues() {} }, connect() {}, disconnect() {}, fftSize: 0, getFloatTimeDomainData() {} });
const ctx = { currentTime: 0, destination: {}, createGain: node, createAnalyser: node,
  createBuffer: (ch, len, rate) => ({ length: len, duration: len / rate, copyToChannel() {} }) };

// The voice's worker: what it was asked, and an answer to each clip asked for.
function open(voice) {
  const asked = [];
  const tts = { postMessage: (m) => asked.push(m) };
  const s = new Speaker({ started() {}, heard() {}, finished() {}, failed() {} });
  const clipsAsked = (v) => asked.filter((m) => m.type === 'clip' && m.v === v).map((m) => m.text);
  const made = (v) => { for (const m of asked.filter((m) => m.type === 'clip' && m.v === v && !m.answered)) { m.answered = true; s.onWorker({ type: 'clip', text: m.text, v, pcm: new Float32Array(2400).fill(0.2), rate: 24000 }); } };
  return s.open({ ctx, natural: true, tts, speed: 1, pace: 1, voice }).then(() => ({ s, asked, clipsAsked, made }));
}
const count = (c) => c.cue.length + c.ack.length + c.nod.length;

test('the clips are made once for the voice it starts in, at once', async () => {
  const { s, clipsAsked, made } = await open('a');
  assert.deepEqual(clipsAsked('a'), ALL);
  made('a');
  assert.equal(count(s.clips), ALL.length);
  s.close();
});

test('a new voice: its clips are asked for a moment later, not at the change', async () => {
  const { s, asked, clipsAsked, made } = await open('a');
  made('a');
  s.setVoice('b');
  assert.deepEqual(asked.filter((m) => m.type === 'voice').map((m) => m.v), ['b'], 'the voice is told of the change at once');
  assert.deepEqual(clipsAsked('b'), [], 'no clip is asked for at the change');
  assert.equal(count(s.clips), 0, 'the old voice\'s clips are not played in the new one');
  advance(1400);
  assert.deepEqual(clipsAsked('b'), []);
  advance(200);
  assert.deepEqual(clipsAsked('b'), ALL, 'asked for once it has been in use a moment');
  made('b');
  assert.equal(count(s.clips), ALL.length);
  s.close();
});

test('a voice that comes back has its clips still, and nothing is made again', async () => {
  const { s, asked, clipsAsked, made } = await open('a');
  made('a');
  s.setVoice('b'); advance(2000); made('b');
  const before = asked.filter((m) => m.type === 'clip').length;
  s.setVoice('a');
  assert.equal(count(s.clips), ALL.length, 'there at once');
  advance(5000);
  assert.equal(asked.filter((m) => m.type === 'clip').length, before, 'nothing asked for again');
  s.setVoice('b');
  assert.equal(count(s.clips), ALL.length);
  advance(5000);
  assert.equal(asked.filter((m) => m.type === 'clip').length, before);
  assert.deepEqual(clipsAsked('a'), ALL);
  s.close();
});

test('a voice used for a moment and left never has clips made', async () => {
  const { s, clipsAsked, made } = await open('a');
  made('a');
  s.setVoice('b'); advance(500);
  s.setVoice('c'); advance(500);
  s.setVoice('a'); advance(5000);
  assert.deepEqual(clipsAsked('b'), []);
  assert.deepEqual(clipsAsked('c'), []);
  s.close();
});

test('no clips are asked for while a reply is being made', async () => {
  const { s, clipsAsked } = await open('a');
  s.setVoice('b');
  s.current = { gen: 1, segs: [], heard: '', synthDone: false };
  advance(6000);
  assert.deepEqual(clipsAsked('b'), [], 'held while the reply\'s words are still being made');
  s.current.synthDone = true;
  advance(1100);
  assert.deepEqual(clipsAsked('b'), ALL, 'asked for once it is made');
  s.close();
});

test('a clip made in a voice no longer in use is kept for that voice', async () => {
  const { s, made } = await open('a');
  s.setVoice('b');
  made('a'); // the first voice's clips arrive after the change
  assert.equal(count(s.clips), 0, 'not taken for the voice in use');
  s.setVoice('a');
  assert.equal(count(s.clips), ALL.length, 'there when its voice is back');
  s.close();
});

// The stand-in above answers a clip under the voice it was asked for, as the
// worker does (tts-worker.js clip()): this holds the two to the same thing.
test('opened with no voice named: its clips are still its clips', async () => {
  const { s, asked, made } = await open(undefined);
  assert.deepEqual(asked.filter((m) => m.type === 'clip').map((m) => m.v), ALL.map(() => ''), 'asked for under no name');
  made('');
  assert.equal(count(s.clips), ALL.length, 'kept where they are looked for');
  // The worker's own lines: what it sends back is what it was asked with.
  const worker = fs.readFileSync(path.join(frame, 'tts-worker.js'), 'utf8');
  assert.match(worker, /postMessage\(\{ type: 'clip', text, v, pcm/, 'the worker answers a clip with the voice it was asked for');
  assert.match(worker, /say\(text, \{ voice: v \|\| voice, speed \}\)/, 'and makes it in that voice, or the one in use');
  s.close();
});

// ---------- a reply made ahead ----------
test('a reply with a voice of its own has every word sent to the voice with it', async () => {
  const { s, asked } = await open('a');
  const r = { gen: 7, fmt: new (await import(pathToFileURL(path.join(tmp, 'speakable.mjs')))).Speakable(), segs: [], sources: [], playHead: 0, voice: 'b', pace: 1.3 };
  s.say(r, 'Hello there.');
  const pushes = asked.filter((m) => m.type === 'push');
  assert.ok(pushes.length >= 2 && pushes.every((m) => m.gen === 7 && m.voice === 'b' && m.speed === 1.3), JSON.stringify(pushes));
  const plain = { gen: 8, fmt: r.fmt.constructor ? new r.fmt.constructor() : r.fmt, segs: [], sources: [], playHead: 0 };
  s.say(plain, 'Hi.');
  assert.ok(asked.filter((m) => m.type === 'push' && m.gen === 8).every((m) => !('voice' in m) && !('speed' in m)), 'a plain reply names neither');
  s.close();
});

test('made ahead: its sound is kept, not played, until it is taken', async () => {
  const { s, asked } = await open('a');
  const played = [];
  s.play = (r, buf, text) => { played.push(text); r.playing = true; };
  const p = s.prepare('Second speaker here.', { voice: 'b' });
  assert.ok(p && p.voice === 'b');
  assert.ok(asked.some((m) => m.type === 'push' && m.gen === p.gen && m.voice === 'b'), 'asked of the voice at once');
  s.onWorker({ type: 'audio', gen: p.gen, text: 'Second speaker here.', pcm: new Float32Array(2400).fill(0.2), rate: 24000 });
  assert.deepEqual(played, [], 'not played while it waits');
  assert.notEqual(s.current, p);
  s.take(p);
  assert.deepEqual(played, ['Second speaker here.'], 'played when taken');
  assert.equal(s.current, p);
  s.onWorker({ type: 'audio', gen: p.gen, text: 'More.', pcm: new Float32Array(2400).fill(0.2), rate: 24000 });
  assert.deepEqual(played, ['Second speaker here.', 'More.'], 'what comes after plays as it comes');
  s.close();
});

test('made ahead: a second line joins it; dropped, the voice is told to stop making it', async () => {
  const { s, asked } = await open('a');
  const p = s.prepare('One.', { voice: 'b' });
  assert.equal(s.prepare(' Two.', { voice: 'b' }, p), p);
  assert.equal(new Set(asked.filter((m) => m.type === 'push' && m.voice === 'b').map((m) => m.gen)).size, 1, 'one reply');
  s.drop(p);
  assert.ok(asked.some((m) => m.type === 'cancel' && m.gen === p.gen));
  s.onWorker({ type: 'audio', gen: p.gen, text: 'One.', pcm: new Float32Array(2400).fill(0.2), rate: 24000 }); // late: ignored
  s.close();
});

test('made ahead: only so much waits (four replies, or half a minute of sound)', async () => {
  const { s } = await open('a');
  const ps = [1, 2, 3, 4].map((i) => s.prepare('Line ' + i + '.', { voice: 'b' }));
  assert.ok(ps.every(Boolean));
  assert.equal(s.prepare('Line 5.', { voice: 'b' }), null, 'a fifth is not made ahead');
  s.close();
  const again = await open('a');
  const p = again.s.prepare('Long.', { voice: 'b' });
  again.s.onWorker({ type: 'audio', gen: p.gen, text: 'Long.', pcm: new Float32Array(24000 * 31).fill(0.2), rate: 24000 });
  assert.equal(again.s.prepare('After it.', { voice: 'b' }), null, 'nor more once half a minute waits');
  again.s.close();
});

test('the device\'s voice makes nothing ahead', async () => {
  const s = new Speaker({ started() {}, heard() {}, finished() {}, failed() {} });
  await s.open({ ctx, natural: false, tts: null, speed: 1, pace: 1 });
  assert.equal(s.prepare('Hello.', { voice: 'b' }), null);
  s.close();
});
