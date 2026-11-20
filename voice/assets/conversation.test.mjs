// The voice frame's conversation (internal/client/web/voice/conversation.js)
// when the agent cuts in on long talk: it says "Sorry," and the rest only if
// the person stops for it. Driven here on a made-up clock, with a stand-in
// for the mic, the text model and the agent's voice.
//
// What a person does: hears "Sorry," (late, where the voice must first make
// it), finishes the word they were on, and stops. That is stopping for it:
// the cut-in is said, not cut off by an answer to the words they said before
// they stopped, and those words go to the agent with their next turn.
// Run: node --test voice/assets/conversation.test.mjs
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
Object.defineProperty(globalThis, 'performance', { value: { now: () => clock }, configurable: true });
function advanceTo(t) {
  let n = 0;
  for (;;) {
    timers.sort((a, b) => a.at - b.at || a.id - b.id);
    if (!timers.length || timers[0].at > t) break;
    if (++n > 100000) throw new Error('timers never settle at ' + clock + ' ms');
    const x = timers.shift();
    clock = Math.max(clock, x.at);
    x.f();
  }
  clock = t;
}

// ---------- the frame, with stand-ins for what it draws, sends and hears ----------
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'voice-conversation-'));
const copy = (name, to) => fs.writeFileSync(path.join(tmp, to), fs.readFileSync(path.join(frame, name), 'utf8').replace(/from '\.\/([\w-]+)\.js'/g, "from './$1.mjs'"));
copy('tuning.js', 'tuning.mjs');
copy('speakable.js', 'speakable.mjs');
copy('conversation.js', 'conversation.mjs');
fs.writeFileSync(path.join(tmp, 'level.mjs'), 'export function micFrame() {}\n');
fs.writeFileSync(path.join(tmp, 'bridge.mjs'), 'export const sent = [];\nexport function send(t, d = {}) { sent.push({ t, ...d }); }\n');
fs.writeFileSync(path.join(tmp, 'speaker.mjs'), `
let gen = 0;
export function newReply(f) { return { gen: ++gen, segs: [], sources: [], playHead: 0, playing: false, synthDone: false, heard: '', ...f }; }
`);
fs.writeFileSync(path.join(tmp, 'panel.mjs'), `
export const rows = [];
const row = (who) => { const r = { who, text: '', note: '', removed: false, remove() { r.removed = true; } }; rows.push(r); return r; };
export const addRow = row, pendingRow = () => row('you');
export function setRowText(r, t) { r.text = t; }
export function setRowNote(r, n) { r.note = n; }
export function setRowHeard(r, a, b) { r.text = a + b; }
export function setRowCut() {}
let cls = '';
export function status(t, c = '') { cls = c; }
export const statusClass = () => cls;
export const msText = (ms) => ms + ' ms';
export function addDivider() {} export function setAgentName() {} export function showHolding() {} export function showMuted() {}
export function timeDuck() {} export function timeStop() {} export function timeLiveText() {} export function timeReply() {}
`);
const talk = await import(pathToFileURL(path.join(tmp, 'conversation.mjs')));
const { sent } = await import(pathToFileURL(path.join(tmp, 'bridge.mjs')));
const { rows } = await import(pathToFileURL(path.join(tmp, 'panel.mjs')));
const { T, FRAME_MS } = await import(pathToFileURL(path.join(tmp, 'tuning.mjs')));

// The agent's voice: makes a line in synthMs, then plays it at ~12 characters
// a second; a made clip ("Sorry,") plays at once.
const voice = {
  natural: true, synthMs: 1200, clipsMade: true,
  said: [], cancelled: [],
  ctx: { get currentTime() { return clock / 1000; } },
  current: null,
  play(r, text) {
    const at = Math.max(clock / 1000, r.playHead), dur = Math.max(0.4, text.length / 12);
    r.segs.push({ text, start: at, end: at + dur, ack: false });
    r.playHead = at + dur; r.playing = true;
    setTimeout(() => talk.replyStarted(r, 'its answer'), (at - clock / 1000) * 1000);
  },
  say(r, text) { this.current = r; this.said.push(text); r.spoken = (r.spoken || '') + text + ' '; setTimeout(() => { if (!this.cancelled.includes(r)) this.play(r, text); }, this.synthMs); },
  cue(r, text) { if (!this.clipsMade) return false; this.current = r; this.said.push(text); r.spoken = text + ' '; this.play(r, text); r.synthDone = true; return true; },
  end(r) { const f = () => (clock / 1000 >= r.playHead ? talk.replyFinished(r) : setTimeout(f, 100)); setTimeout(f, 100); },
  cancel(r) { this.cancelled.push(r); if (this.current === r) this.current = null; },
  fadeOut(r) { this.cancel(r); },
  audible(r) { return !!r && r.playing && clock / 1000 < r.playHead + 0.8; },
  answering(r) { return r.playing; },
  heardWhole(r) { return r.segs.filter((s) => s.end <= clock / 1000).map((s) => s.text).join(' '); },
  nodding: () => false, nod: () => false, ack: () => false, pulse() {}, duck() {}, level: () => 0,
};

// The text model: a final is the words of the last thing the person said.
let words = '';
const stt = {
  postMessage(m) {
    const text = m.final && m.audio.length >= 512 * 3 ? words : ''; // no speech in it, no words
    setTimeout(() => talk.onWords({ type: 'text', id: m.id, final: m.final, text, ms: 1, voiced: m.voiced }), 40);
  },
};

// The person: speech or quiet at the mic for ms.
const loud = new Float32Array(512).fill(0.2), hush = new Float32Array(512);
function mic(ms, speaking, w) {
  if (w) words = w;
  const end = clock + ms;
  while (clock + FRAME_MS <= end) {
    advanceTo(clock + FRAME_MS);
    talk.onFrame({ isSpeech: speaking ? 0.95 : 0.02 }, speaking ? loud : hush);
  }
  advanceTo(end);
}

// Long talk until a part has gone to the agent, then still talking.
function longTalk() {
  sent.length = 0; rows.length = 0; voice.said.length = 0; voice.cancelled.length = 0;

  talk.start({ stt, speaker: voice, turnModel: { slow: true, finished: async () => 0.9 }, nods: () => false });
  mic(13000, true, 'first part of my long story');
  mic(200, false); // a breath: the part goes
  mic(300, true, 'and the bit just before I stopped');
  assert.deepEqual(sent.map((s) => s.t), ['part'], 'a part of the long talk went to the agent');
}
const CUT_IN = 'Hold on, that file was deleted yesterday.';
const said = () => sent.filter((s) => s.t === 'said');

test('stopping a moment after hearing "Sorry," takes the cut-in, even when the voice made it late', (t) => {
  t.after(() => { talk.stop(); timers = []; });
  voice.clipsMade = false; // the cue must be made first: heard 1.2 s after the line arrived
  longTalk();
  talk.agentSaid(CUT_IN);
  assert.deepEqual(voice.said, ['Sorry,'], 'only "Sorry," while they talk');
  mic(1200 + 700 + 1800, true); // made, heard, and they finish what they were saying
  mic(3000, false);             // they stop and listen
  assert.ok(voice.said.includes(CUT_IN), `the cut-in was said (said: ${JSON.stringify(voice.said)})`);
  assert.equal(said().length, 0, `what they said before stopping is not a turn the agent answers: ${JSON.stringify(said())}`);
  assert.ok(!rows.some((r) => /cut-in not taken/.test(r.note)), 'not shown as talked over');
  // Their next turn carries what they said before they stopped.
  mic(1500, true, 'so what did you want to say');
  mic(3000, false);
  assert.equal(said().length, 1, 'the next turn goes');
  assert.match(said()[0].text, /before they stopped for your cut-in they had also said: "and the bit just before I stopped"/);
  assert.match(said()[0].text, /so what did you want to say$/);
  assert.ok(!/talked on over your cut-in/.test(said()[0].text));
});

test('a made "Sorry," plays at once, and stopping after it is taking it', (t) => {
  t.after(() => { talk.stop(); timers = []; });
  voice.clipsMade = true;
  longTalk();
  talk.agentSaid(CUT_IN);
  mic(1500, true, 'so what did you want to say');
  mic(3000, false);
  assert.ok(voice.said.includes(CUT_IN));
  assert.equal(said().length, 0);
});

test('talking on over "Sorry," drops the cut-in; stopping soon after still takes it', (t) => {
  t.after(() => { talk.stop(); timers = []; });
  voice.clipsMade = true;
  longTalk();
  talk.agentSaid(CUT_IN);
  mic(T.cutInWaitMs + 1500, true); // talked over: judged not taken
  assert.ok(rows.some((r) => /cut-in not taken/.test(r.note) && !r.removed), 'shown as not taken');
  assert.ok(!voice.said.includes(CUT_IN));
  mic(3000, false);                // ...and they stop a moment later
  assert.ok(voice.said.includes(CUT_IN), 'taken late');
  assert.ok(!rows.some((r) => /cut-in not taken/.test(r.note) && !r.removed), 'the "not taken" row is gone');
  assert.equal(said().length, 0);
});

test('control: talking right through it, the cut-in stays dropped and the agent is told', (t) => {
  t.after(() => { talk.stop(); timers = []; });
  voice.clipsMade = true;
  longTalk();
  talk.agentSaid(CUT_IN);
  mic(9000, true); // well past any wait for it
  mic(3000, false);
  assert.ok(!voice.said.includes(CUT_IN), 'not said');
  const out = sent.filter((s) => s.t !== 'part' || /talked on/.test(s.text)).map((s) => s.text).join(' | ');
  assert.match(out, /talked on over your cut-in: "Hold on, that file was deleted yesterday\."/);
});
