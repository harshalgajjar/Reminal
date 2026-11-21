// The reply's voice in its own thread: Kokoro (82M, Apache-2.0) via kokoro.js,
// with g2p.js turning words into the phonemes Kokoro reads.
// Text arrives a few words at a time as the reply is written; the splitter
// hands Kokoro one sentence at a time, so the first sentence is heard while
// the rest is still being written.
import { load as loadKokoro, generate } from './kokoro.js';
import { loadG2P, phonemize } from './g2p.js';
import { describe } from './integrity.js';

let voice = 'af_heart', speed = 1;
const gens = new Map(); // gen -> { splitter, cancelled }

const say = (text, opts) => generate(phonemize(text), opts);

// Sentences out of text that arrives in pieces. push() adds text and hands on
// every sentence that is surely finished (its end is followed by more text);
// flush() hands on what is left as it is; close() flushes and ends the stream.
const ABBREV = new Set(['mr', 'mrs', 'ms', 'dr', 'prof', 'sr', 'jr', 'st', 'mt', 'vs', 'etc', 'eg', 'ie', 'e.g', 'i.e', 'inc', 'ltd', 'co', 'dept', 'no', 'fig', 'approx', 'jan', 'feb', 'mar', 'apr', 'jun', 'jul', 'aug', 'sep', 'sept', 'oct', 'nov', 'dec']);
const ENDS = '.!?…';
const CLOSERS = '"\')]}”’';
class Splitter {
  constructor() { this.buf = ''; this.out = []; this.closed = false; this.wake = null; }
  push(text) { this.buf += text; this.cut(); }
  flush() { const t = this.buf.trim(); this.buf = ''; if (t) this.out.push(t); this.poke(); }
  close() { if (this.closed) return; this.closed = true; this.flush(); }
  poke() { if (this.wake) { const w = this.wake; this.wake = null; w(); } }
  cut() {
    const s = this.buf;
    let from = 0;
    for (let i = 0; i < s.length; i++) {
      const c = s[i];
      if (c !== '\n' && !ENDS.includes(c)) continue;
      let end = i;
      while (end + 1 < s.length && (ENDS.includes(s[end + 1]) || CLOSERS.includes(s[end + 1]))) end++;
      let next = end + 1;
      if (next < s.length && !/\s/.test(s[next])) { i = end; continue; } // 3.5, e.g.x, a.b
      while (next < s.length && /\s/.test(s[next])) next++;
      if (next >= s.length) break; // not sure it ended until more text comes
      if (c === '.') {
        const word = s.slice(from, i).split(/\s/).pop().toLowerCase();
        if (ABBREV.has(word.replace(/\.$/, ''))) { i = end; continue; }
        if (/^([a-z]\.)*[a-z]$/.test(word) && /[A-Z]/.test(s[next])) { i = end; continue; } // U.S. Army
        if (/[a-z0-9]/.test(s[next])) { i = end; continue; } // lower case after: not a new sentence
        if (/(^|\n)\s*\d+$/.test(s.slice(from, i))) { i = end; continue; } // "1." in a list
      }
      const t = s.slice(from, end + 1).trim();
      if (t && !/^[.…]+$/.test(t)) this.out.push(t);
      from = next; i = next - 1;
    }
    this.buf = s.slice(from);
    if (this.out.length) this.poke();
  }
  async *[Symbol.asyncIterator]() {
    for (;;) {
      if (this.out.length) yield this.out.shift();
      else if (this.closed) return;
      else await new Promise((r) => { this.wake = r; });
    }
  }
}

// The 8-bit model on the CPU, on every device: the full-size one the GPU
// would run is too large for the asset store it is served from.
async function load({ v }) {
  voice = v || voice;
  const device = 'wasm';
  const t0 = performance.now();
  await Promise.all([
    loadG2P(new URL('./', import.meta.url).href),
    loadKokoro({
      dtype: 'q8',
      device,
      progress_callback: (p) => {
        if (p.status === 'progress' && p.total) postMessage({ type: 'progress', file: p.file, loaded: p.loaded, total: p.total });
      },
    }),
  ]);
  await say('Hi.', { voice }); // warm-up, and fetches the voice
  // How fast it makes speech here: seconds of audio per second of work.
  const s0 = performance.now();
  const a = await say('This is a short test of how quickly the voice can talk.', { voice });
  const speed = (a.audio.length / a.sampling_rate) / ((performance.now() - s0) / 1000);
  postMessage({ type: 'ready', device, ms: Math.round(performance.now() - t0), isolated: self.crossOriginIsolated, speed });
}

// One job on the model at a time: it cannot run two at once, and a reply cut
// off mid-sentence would otherwise still be running when the next one starts.
// Replies go first; the short clips ("Okay.") are made only while no reply is
// waiting, or a clip being made could hold up the answer it is meant to cover.
let busy = false;
const waitReply = [], waitClip = [];
const acquire = (reply) => new Promise((r) => { if (!busy) { busy = true; r(); } else (reply ? waitReply : waitClip).push(r); });
// The next job is chosen a turn of the event loop later, not at once: the
// model keeps this thread busy, and handing over straight away started the
// next clip before the messages waiting meanwhile (a reply's words) were
// read, so the first reply waited for every clip to be made.
function release() {
  setTimeout(() => { const next = waitReply.shift() || waitClip.shift(); if (next) next(); else busy = false; }, 0);
}

// own: the voice and the pace of this reply alone, when its first words named
// them (else the ones in use).
function start(gen, own = {}) {
  const splitter = new Splitter();
  const g = { splitter, cancelled: false, closed: false, voice: own.voice, speed: own.speed };
  gens.set(gen, g);
  (async () => {
    await acquire(true);
    try {
      if (!g.cancelled) {
        for await (const text of splitter) {
          if (g.cancelled) break;
          const audio = await say(text, { voice: g.voice || voice, speed: g.speed || speed });
          if (g.cancelled) break;
          const pcm = audio.audio;
          postMessage({ type: 'audio', gen, text, pcm, rate: audio.sampling_rate, ms: Math.round(performance.now() - (g.lastT || performance.now())) }, [pcm.buffer]);
          g.lastT = performance.now();
          // Without a real turn of the event loop here a cancel never gets
          // read: a reply that was talked over went on being made, and held
          // up the next.
          await new Promise((r) => setTimeout(r, 0));
          if (g.cancelled) break;
        }
      }
    } catch (err) {
      postMessage({ type: 'error', gen, message: describe(err) });
    } finally { release(); }
    gens.delete(gen);
    postMessage({ type: 'done', gen, cancelled: g.cancelled });
  })();
  return g;
}
// v: the voice it is for (the one in use when it was asked for, which may
// have changed by the time it is made). It goes back as it was asked: the
// asker keeps its clips by that name, and one asked for with no voice named
// is made in the voice in use here and kept under no name.
async function clip(text, v) {
  await acquire(false);
  try {
    const a = await say(text, { voice: v || voice, speed });
    postMessage({ type: 'clip', text, v, pcm: a.audio, rate: a.sampling_rate }, [a.audio.buffer]);
  } catch (_) {} finally { release(); }
}
function close(g) { if (!g.closed) { g.closed = true; g.splitter.close(); } }

onmessage = async (e) => {
  const m = e.data;
  try {
    if (m.type === 'load') return await load(m);
    if (m.type === 'speed') { speed = +m.v || 1; return; }
    if (m.type === 'voice') { voice = m.v || voice; return; }
    if (m.type === 'push') { const g = gens.get(m.gen) || start(m.gen, { voice: typeof m.voice === 'string' && m.voice ? m.voice : undefined, speed: +m.speed > 0 ? +m.speed : undefined }); if (!g.closed) { if (!g.firstT) g.firstT = g.lastT = performance.now(); g.splitter.push(m.text); } return; }
    // A short clip made once and kept ("Okay."), after any waiting reply.
    if (m.type === 'clip') { clip(m.text, m.v); return; }
    // The first words of a reply go to the voice as soon as there is a phrase,
    // not a whole sentence, so it starts talking sooner.
    if (m.type === 'flush') { const g = gens.get(m.gen); if (g && !g.closed) g.splitter.flush(); return; }
    if (m.type === 'end') { const g = gens.get(m.gen); if (g) close(g); return; }
    if (m.type === 'cancel') { const g = gens.get(m.gen); if (g) { g.cancelled = true; close(g); } return; }
  } catch (err) {
    postMessage({ type: 'error', gen: m.gen, message: describe(err) });
  }
};
