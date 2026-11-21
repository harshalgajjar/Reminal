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
const { rows, statusClass } = await import(pathToFileURL(path.join(tmp, 'panel.mjs')));
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

// The text model: the words of each stretch of speech in the audio it is
// given, in order. Each stretch is marked in the audio itself (its loudness),
// so a final of part of the talk has only that part's words.
const phrases = [''];
const requests = []; // every final asked for: {id, audio}
const stt = {
  postMessage(m) {
    let text = '';
    if (m.final) requests.push(m);
    if (m.final) {
      const seen = [];
      for (let i = 0; i < m.audio.length; i += 512) {
        const k = Math.round((m.audio[i] - 0.2) * 1000);
        if (k > 0 && seen[seen.length - 1] !== k) seen.push(k);
      }
      text = seen.map((k) => phrases[k]).join(' ');
    }
    setTimeout(() => talk.onWords({ type: 'text', id: m.id, final: m.final, text, ms: 1, voiced: m.voiced }), 40);
  },
};

// The person: saying w (or keeping on with what they were saying), or quiet,
// at the mic for ms.
const hush = new Float32Array(512);
// Speech: a short gap between words every sixth frame (its mark kept in
// its first sample), as speech has.
let loud = hush, gap = hush, nth = 0;
const speech = () => (++nth % 6 === 0 ? gap : loud);
function mic(ms, speaking, w) {
  if (w) { phrases.push(w); loud = new Float32Array(512).fill(0.2 + (phrases.length - 1) / 1000); gap = new Float32Array(512); gap[0] = loud[0]; }
  const end = clock + ms;
  while (clock + FRAME_MS <= end) {
    advanceTo(clock + FRAME_MS);
    talk.onFrame({ isSpeech: speaking ? 0.95 : 0.02 }, speaking ? speech() : hush);
  }
  advanceTo(end);
}

// Long talk until a part has gone to the agent, then still talking.
function longTalk() {
  sent.length = 0; rows.length = 0; voice.said.length = 0; voice.cancelled.length = 0;

  talk.start({ stt, speaker: voice, turnModel: { slow: true, finished: async () => 0.9 }, nods: () => false });
  mic(13000, true, 'first part of my long story');
  mic(200, false); // a breath: the part goes
  mic(1000, true, 'and the bit just before I stopped');
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
  mic(1200 + 700 + 1800, true, 'which I said after it came in'); // made, heard, and they finish what they were saying
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
  assert.ok(!/after it came in/.test(said()[0].text), 'what they said after it came in is dropped');
  assert.ok(!sent.some((s) => /after it came in/.test(s.text)), 'and never sent');
  // What they said before it was transcribed cut at a gap between words, ending in silence.
  const cut = requests.find((m) => /:-/.test(m.id));
  assert.ok(cut, 'a final of its own for what came before it');
  const tail = cut.audio.subarray(cut.audio.length - 6400);
  assert.ok(tail.every((x) => x === 0), 'it ends in 400 ms of silence');
  const last = cut.audio.subarray(cut.audio.length - 6400 - 512, cut.audio.length - 6400);
  assert.ok(last.slice(1).every((x) => Math.abs(x) < 0.21) && last.slice(-480).every((x, i) => Math.abs(x) <= 0.21 * (480 - i) / 480 + 1e-6), 'faded out');
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
  assert.ok(!sent.some((s) => /talking on over it|after it came in/.test(s.text)), 'what they said after it came in is never sent');
});

test('talking on over "Sorry," drops the cut-in; stopping soon after still takes it', (t) => {
  t.after(() => { talk.stop(); timers = []; });
  voice.clipsMade = true;
  longTalk();
  talk.agentSaid(CUT_IN);
  mic(T.cutInWaitMs + 1500, true, 'talking on over it'); // talked over: judged not taken
  assert.ok(rows.some((r) => /cut-in not taken/.test(r.note) && !r.removed), 'shown as not taken');
  assert.ok(!voice.said.includes(CUT_IN));
  mic(3000, false);                // ...and they stop a moment later
  assert.ok(voice.said.includes(CUT_IN), 'taken late');
  assert.ok(!rows.some((r) => /cut-in not taken/.test(r.note) && !r.removed), 'the "not taken" row is gone');
  assert.equal(said().length, 0);
  assert.ok(!sent.some((s) => /talking on over it|after it came in/.test(s.text)), 'what they said after it came in is never sent');
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

test('a cut-in right after a part: stopping for it sends nothing of what came after', (t) => {
  t.after(() => { talk.stop(); timers = []; });
  voice.clipsMade = true;
  sent.length = 0; rows.length = 0; voice.said.length = 0;
  talk.start({ stt, speaker: voice, turnModel: { slow: true, finished: async () => 0.9 }, nods: () => false });
  mic(13000, true, 'first part of my long story');
  mic(200, false);
  talk.agentSaid(CUT_IN); // in answer to the part, before another word
  mic(1500, true, 'um what was I');
  mic(3000, false);
  assert.ok(voice.said.includes(CUT_IN));
  assert.deepEqual(sent.map((s) => s.t), ['part']);
  mic(1500, true, 'okay tell me');
  mic(3000, false);
  assert.equal(said().length, 1);
  assert.equal(said()[0].text, 'okay tell me', 'no note: nothing before it came in, and the rest dropped');
});

test('control: not taken, everything they said is sent', (t) => {
  t.after(() => { talk.stop(); timers = []; });
  voice.clipsMade = true;
  longTalk();
  talk.agentSaid(CUT_IN);
  mic(9000, true, 'and I kept right on talking');
  mic(3000, false);
  assert.match(said().map((s) => s.text).join(' '), /and the bit just before I stopped and I kept right on talking/);
});

test('lines the agent says after a cut-in was talked over are never lost', (t) => {
  t.after(() => { talk.stop(); timers = []; });
  voice.clipsMade = true;
  longTalk();
  talk.agentSaid(CUT_IN);
  mic(T.cutInWaitMs + 1000, true, 'talking on over it'); // judged not taken
  talk.agentSaid('Also, the tests are failing.');          // inside the late window: joins it
  const row = rows.find((r) => /cut-in not taken/.test(r.note));
  assert.match(row.text, /deleted yesterday\. Also, the tests are failing\./, 'shown with the cut-in');
  mic(T.cutInLateMs + 2000, true);                        // talking on, no pause, past the window
  talk.agentSaid('One more thing.');                       // after it: a new cut-in
  assert.equal(voice.said.filter((s) => s === 'Sorry,').length, 2, 'a new "Sorry," for it');
  mic(1500, true);
  mic(3000, false);                                       // stops for the new one
  assert.ok(voice.said.includes('One more thing.'), 'the new cut-in is said');
  mic(1500, true, 'right go on');                         // their next turn carries the note
  mic(3000, false);
  const told = sent.map((s) => s.text).join(' | ');
  assert.match(told, /talked on over your cut-in: "Hold on, that file was deleted yesterday\. Also, the tests are failing\."/, `the agent is told of both: ${told}`);
});

// With a turn model fast enough to wait for, which answers "not finished"
// (its promise is let run between frames).
async function micA(ms, speaking, w) {
  if (w) { phrases.push(w); loud = new Float32Array(512).fill(0.2 + (phrases.length - 1) / 1000); gap = new Float32Array(512); gap[0] = loud[0]; }
  const end = clock + ms;
  while (clock + FRAME_MS <= end) {
    advanceTo(clock + FRAME_MS);
    talk.onFrame({ isSpeech: speaking ? 0.95 : 0.02 }, speaking ? speech() : hush);
    await new Promise((r) => setImmediate(r));
  }
  advanceTo(end);
}

test('a fast turn model saying "not finished" as they stop for it sends nothing said after it came in', async (t) => {
  t.after(() => { talk.stop(); timers = []; });
  voice.clipsMade = true;
  sent.length = 0; rows.length = 0; voice.said.length = 0;
  talk.start({ stt, speaker: voice, turnModel: { slow: false, finished: async () => 0.2 }, nods: () => false });
  await micA(13000, true, 'first part of my long story');
  await micA(400, false);                                   // "not finished": the part goes
  assert.deepEqual(sent.map((s) => s.t), ['part']);
  await micA(13000, true, 'the second stretch of it');      // 12 s and more since that part
  talk.agentSaid(CUT_IN);
  await micA(1500, true, 'which I said after it came in');
  await micA(3000, false);                                  // they stop: "not finished" again
  assert.ok(voice.said.includes(CUT_IN), 'the cut-in is said');
  assert.ok(!sent.some((s) => /after it came in/.test(s.text)), `nothing said after it came in is sent: ${JSON.stringify(sent)}`);
  await micA(1500, true, 'okay tell me');
  await micA(3000, false);
  const all = sent.map((s) => s.text).join(' | ');
  assert.ok(!/after it came in/.test(all), all);
  assert.match(all, /before they stopped for your cut-in they had also said: "the second stretch of it"/);
});

// A sound with no words in it (a cough, a breath): loud enough to start a
// turn, too quiet for any word.
function sound(ms) {
  loud = new Float32Array(512).fill(0.1); gap = new Float32Array(512); gap[0] = 0.1;
  mic(ms, true);
}
const HELD = 'The build passed.';

test('a line held while they made a sound with no words is said once the sound ends', (t) => {
  t.after(() => { talk.stop(); timers = []; });
  sent.length = 0; rows.length = 0; voice.said.length = 0; voice.cancelled.length = 0;
  talk.start({ stt, speaker: voice, turnModel: { slow: true, finished: async () => 0.9 }, nods: () => false });
  sound(300);
  talk.agentSaid(HELD);                     // while they cough: held, not said over them
  assert.ok(!voice.said.includes(HELD), 'not said over them');
  assert.ok(rows.some((r) => /not read out: you were talking/.test(r.note) && !r.removed), 'shown as held');
  sound(300);
  mic(3000, false);                         // the turn ends with no words
  assert.ok(voice.said.includes(HELD), `the held line is said (said: ${JSON.stringify(voice.said)})`);
  assert.ok(rows.some((r) => r.who === 'agent' && !r.removed && r.text.includes(HELD)), 'its row stays on screen');
  assert.equal(said().length, 0, 'nothing is sent for a turn with no words');
  assert.notEqual(statusClass(), 'think', 'not left on "Thinking"');
  mic(3000, false);
  assert.ok(rows.some((r) => r.who === 'agent' && !r.removed && r.text === HELD), 'and shown as heard');
});

test('control: a line held while they said something stays on screen, unsaid, and their turn goes', (t) => {
  t.after(() => { talk.stop(); timers = []; });
  sent.length = 0; rows.length = 0; voice.said.length = 0; voice.cancelled.length = 0;
  talk.start({ stt, speaker: voice, turnModel: { slow: true, finished: async () => 0.9 }, nods: () => false });
  mic(600, true, 'what about the tests');
  talk.agentSaid(HELD);
  mic(600, true);
  mic(3000, false);
  assert.ok(!voice.said.includes(HELD), 'not read out after they spoke: it answers them anew');
  const row = rows.find((r) => /not read out: you were talking/.test(r.note));
  assert.ok(row && !row.removed && row.text === HELD, 'left on screen, marked not read out');
  assert.equal(said().length, 1, 'their turn goes to the agent');
  assert.equal(said()[0].text, 'what about the tests');
});

// ---------- a line in a voice of its own ----------
// A caller may give a line a voice (and a pace); the viewer never does. Such
// a line is a reply of its own, said in that voice, and where it must wait
// for the reply being heard its sound is made meanwhile.
const made = { prepared: [], taken: [], dropped: [] };
let madeGen = 1000;
Object.assign(voice, {
  prepare(text, opts, into) {
    const p = into || { gen: ++madeGen, segs: [], sources: [], playHead: 0, playing: false, synthDone: false, heard: '', voice: opts.voice, pace: opts.pace, words: '' };
    p.words += text;
    made.prepared.push({ p, text, joined: !!into });
    return p;
  },
  take(p) { this.current = p; made.taken.push(p); this.said.push(p.words); this.play(p, p.words); },
  drop(p) { p.dropped = true; made.dropped.push(p); },
  // Made ahead and neither taken nor let go: each of these holds the voice.
  waiting() { return [...new Set(made.prepared.map((x) => x.p))].filter((p) => !made.taken.includes(p) && !made.dropped.includes(p)); },
});
function fresh() {
  sent.length = 0; rows.length = 0; voice.said.length = 0; voice.cancelled.length = 0;
  made.prepared.length = 0; made.taken.length = 0; made.dropped.length = 0;
  talk.start({ stt, speaker: voice, turnModel: { slow: true, finished: async () => 0.9 }, nods: () => false });
}
const FIRST = 'The first speaker says a fairly long sentence so that it is still being heard.';
const SECOND = 'And the second one answers.';

test('a line with a voice of its own, nobody talking: said at once, in that voice', (t) => {
  t.after(() => { talk.stop(); timers = []; });
  fresh();
  talk.agentSaid(talk.line(FIRST, { voice: 'af_bella', pace: 1.2 }));
  assert.deepEqual(voice.said, [FIRST], 'said as any line is');
  assert.equal(typeof voice.said[0], 'string', 'the voice is handed plain text');
  assert.equal(voice.current.voice, 'af_bella');
  assert.equal(voice.current.pace, 1.2);
  assert.equal(made.prepared.length, 0, 'nothing to make ahead: nothing is being heard');
});

test('control: a plain line has no voice of its own, and nothing is made ahead for it', (t) => {
  t.after(() => { talk.stop(); timers = []; });
  fresh();
  talk.agentSaid(FIRST);
  mic(1600, false);                 // its turn ends (a reply of its own), still being heard
  talk.agentSaid(SECOND);           // waits for it, as before
  assert.equal(voice.current.voice, undefined);
  assert.equal(made.prepared.length, 0, 'a plain line is not made ahead');
  assert.ok(!voice.said.includes(SECOND), 'not said over the reply being heard');
  mic(9000, false);
  assert.deepEqual(voice.said, [FIRST, SECOND], 'said after it, as before');
});

test('a line in another voice while a reply is being heard: made meanwhile, said after it, once', (t) => {
  t.after(() => { talk.stop(); timers = []; });
  fresh();
  talk.agentSaid(talk.line(FIRST, { voice: 'af_bella' }));
  mic(1600, false);                 // made (1.2 s) and its turn over; it plays for some seconds yet
  const first = voice.current;
  talk.agentSaid(talk.line(SECOND, { voice: 'am_adam' }));
  assert.equal(made.prepared.length, 1, 'its sound is made while the first is heard');
  assert.equal(made.prepared[0].p.voice, 'am_adam');
  assert.ok(!voice.said.includes(SECOND), 'not said over the first');
  assert.equal(voice.current, first, 'the first is still the one being said');
  mic(9000, false);                 // the first has been heard
  assert.equal(made.taken.length, 1, 'the one made ahead is the one said next');
  assert.equal(made.taken[0], made.prepared[0].p);
  assert.equal(voice.said.filter((s) => s.includes(SECOND)).length, 1, 'and its words go to the voice once');
  assert.ok(rows.some((r) => r.who === 'agent' && !r.removed && String(r.text).includes(SECOND)), 'shown as its own reply');
});

test('a line in another voice while a reply of its own is still being said ends that one first', (t) => {
  t.after(() => { talk.stop(); timers = []; });
  fresh();
  talk.agentSaid(talk.line(FIRST, { voice: 'af_bella' }));
  talk.agentSaid(talk.line(SECOND, { voice: 'am_adam' })); // at once, before the first's turn is over
  assert.deepEqual(voice.said, [FIRST], 'not joined to the first speaker\'s reply');
  assert.equal(made.prepared.length, 1);
  mic(12000, false);
  assert.equal(made.taken.length, 1, 'said after the first, as a reply of its own');
});

test('two waiting lines in the same voice are one reply made ahead; another voice is another', (t) => {
  t.after(() => { talk.stop(); timers = []; });
  fresh();
  talk.agentSaid(talk.line(FIRST, { voice: 'af_bella' }));
  mic(1600, false);
  talk.agentSaid(talk.line('Second, part one.', { voice: 'am_adam' }));
  talk.agentSaid(talk.line('Second, part two.', { voice: 'am_adam' }));
  talk.agentSaid(talk.line('Third speaker.', { voice: 'bf_emma' }));
  assert.deepEqual(made.prepared.map((x) => [x.p.voice, x.joined]), [['am_adam', false], ['am_adam', true], ['bf_emma', false]]);
  assert.equal(made.prepared[0].p, made.prepared[1].p, 'the two lines are one reply');
  mic(30000, false);
  assert.deepEqual(made.taken.map((p) => p.voice), ['am_adam', 'bf_emma'], 'each said once, in order');
  assert.ok(!voice.said.includes('Second, part two.'), 'the joined line is not said a second time');
});

test('a line with a voice held while they made a sound keeps its voice when it is said', (t) => {
  t.after(() => { talk.stop(); timers = []; });
  fresh();
  sound(300);
  talk.agentSaid(talk.line(HELD, { voice: 'af_bella' }));
  assert.ok(!voice.said.includes(HELD), 'not said over them');
  sound(300);
  mic(3000, false);
  assert.ok(voice.said.includes(HELD), 'said once the sound ends');
  assert.equal(voice.current && voice.current.voice, 'af_bella', 'in its own voice');
});

test('lines waiting with sound made ahead are given up when they talk over the reply', (t) => {
  t.after(() => { talk.stop(); timers = []; });
  fresh();
  talk.agentSaid(talk.line(FIRST, { voice: 'af_bella' }));
  mic(1600, false);
  talk.agentSaid(talk.line(SECOND, { voice: 'am_adam' }));
  assert.equal(made.prepared.length, 1);
  mic(1500, true, 'no wait stop that is wrong');   // they talk over it
  mic(3000, false);
  assert.equal(made.taken.length, 0, 'the waiting line is not said after being talked over');
  assert.equal(made.dropped.length, 1, 'and what was made for it is let go');
});

test('a line with a voice but no words is the plain empty line it was', () => {
  assert.equal(talk.line('', { voice: 'af_bella' }), '');
  assert.equal(talk.line('   ', { voice: 'af_bella' }), '   ');
  assert.ok(!talk.line('', { voice: 'af_bella' }), 'false where an empty line is asked after');
  assert.equal(typeof talk.line('', { voice: 'af_bella' }), 'string');
  assert.equal(String(talk.line('Hi.', { voice: 'af_bella' })), 'Hi.');
});

test('two lines in two voices held while they made a sound come out as two replies, each in its own', (t) => {
  t.after(() => { talk.stop(); timers = []; });
  fresh();
  sound(300);
  talk.agentSaid(talk.line('First speaker, held.', { voice: 'af_bella' }));
  talk.agentSaid(talk.line('Second speaker, held.', { voice: 'am_adam' }));
  sound(300);
  mic(3000, false);                 // the sound ends: both are said
  assert.deepEqual(voice.said.filter((s) => /held/.test(s)).slice(0, 1), ['First speaker, held.'], 'the first is said alone, not joined with the second');
  assert.ok(!voice.said.some((s) => /First speaker, held\..*Second speaker, held\./.test(s)), 'never the two as one');
  assert.equal(made.prepared.length, 1, 'the second waits for the first, its sound made meanwhile');
  assert.equal(made.prepared[0].p.voice, 'am_adam');
  mic(12000, false);
  assert.equal(made.taken.length, 1, 'and is said after it, in its own voice');
});

test('three lines in three voices waiting: each made once, each said once, none left holding the voice', (t) => {
  t.after(() => { talk.stop(); timers = []; });
  fresh();
  talk.agentSaid(talk.line(FIRST, { voice: 'af_bella' }));
  mic(1600, false);
  talk.agentSaid(talk.line('Second speaker.', { voice: 'am_adam' }));
  talk.agentSaid(talk.line('Third speaker.', { voice: 'bf_emma' }));
  talk.agentSaid(talk.line('Fourth speaker.', { voice: 'bm_george' }));
  assert.equal(made.prepared.length, 3, 'each is made while the first is heard');
  mic(40000, false);                // all four heard, one after another
  assert.equal(made.prepared.length, 3, `none is made a second time as the one before it starts (made: ${made.prepared.map((x) => x.p.voice).join(', ')})`);
  assert.deepEqual(made.taken.map((p) => p.voice), ['am_adam', 'bf_emma', 'bm_george'], 'each said once, in order');
  assert.equal(made.dropped.length, 0, 'none had to be let go');
  assert.equal(voice.waiting().length, 0, 'and nothing made ahead is left: the voice is free');
});

test('a line made ahead and then held while they talk, and given up, lets its sound go', (t) => {
  t.after(() => { talk.stop(); timers = []; });
  fresh();
  talk.agentSaid(talk.line(FIRST, { voice: 'af_bella' }));
  mic(1600, false);
  talk.agentSaid(talk.line(SECOND, { voice: 'am_adam' }));   // made ahead, waiting
  assert.equal(voice.waiting().length, 1);
  mic(9000, true, 'no stop I want to ask something else entirely');   // they talk over it and on
  mic(3000, false);                                                   // their turn goes
  assert.equal(made.taken.length, 0, 'it is not said');
  assert.equal(voice.waiting().length, 0, 'and nothing is left holding the voice');
});
