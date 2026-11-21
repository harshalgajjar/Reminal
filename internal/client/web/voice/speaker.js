// The agent's voice: Kokoro (made in tts-worker.js, played through Web Audio)
// or the device's own speech synthesis, behind one interface. A reply is a
// plain object the conversation owns; the speaker keeps its audio state on it:
//   r.gen      which reply the worker's audio belongs to
//   r.fmt      the Speakable its text goes through
//   r.segs     what has been scheduled: {text, start, end, ack}
//   r.playing  it has made a sound
//   r.heard    the text heard so far
import { T } from './tuning.js';
import { Speakable } from './speakable.js';
import { levelOf, rmsOf } from './level.js';

// Short clips made once in the natural voice: a quick "Okay." before a slow
// answer, an "mm-hm" while you talk at length, and the "Sorry," a cut-in
// starts with (it must be heard at once, while you are still talking).
export const CLIPS = { ack: ['Okay.', 'Sure.', 'Got it.', 'Right.', 'Mm, okay.'], nod: ['Mm-hm.', 'Mhm.'], cue: [T.cutInCue] };

// After a change of voice its clips are asked for this much later (a voice
// changed for one line and changed back never has them made), and that much
// later again while a reply is still being made.
const CLIPS_AFTER_VOICE_MS = 1500, CLIPS_RETRY_MS = 1000;
// How much is made ahead at most: seconds of sound waiting, and replies.
const AHEAD_MAX_S = 30, AHEAD_MAX_REPLIES = 4;
// A waiting reply's first words are made early only when the reply being
// said has this much sound made and not yet heard: the first words' own
// length (about two seconds of speech) over how fast the voice makes speech
// here, half as much again, and never under two seconds.
const FIRST_WORDS_S = 2, FIRST_MARGIN = 1.5, FIRST_MIN_S = 2;

let nextGen = 0;
export function newReply(fields) {
  return { gen: ++nextGen, fmt: new Speakable(), segs: [], sources: [], playHead: 0, playing: false, synthDone: false, heard: '', ...fields };
}

export class Speaker {
  // on: { started(r, what), heard(r), finished(r), failed(r, text, why) }
  // failed: the device's voice could not say text (a browser with no speech
  // engine says "synthesis-failed" and makes no sound).
  constructor(on) { this.on = on; this.clipsBy = new Map(); this.ahead = new Map(); this.voice = ''; this.lastAck = ''; this.lastNodT = 0; this.nodUntil = 0; this.buf = ''; }

  // The clips of the voice in use. They are kept by voice: made once for
  // each, and there again when a voice comes back.
  get clips() { return this.clipsOf(this.voice); }
  clipsOf(v) {
    let c = this.clipsBy.get(v);
    if (!c) { c = { ack: [], nod: [], cue: [], asked: false }; this.clipsBy.set(v, c); }
    return c;
  }

  // natural: Kokoro through `tts` (its worker), at `speed` x real time; else
  // the device's voice. guard: play through the echo canceller's loopback.
  async open({ ctx, natural, tts, speed, guard, pace, voice }) {
    Object.assign(this, { ctx, natural, tts, speed, pace, current: null, voice: voice || '' });
    this.out = ctx.createGain();
    this.meter = ctx.createAnalyser(); this.meter.fftSize = 1024; this.meterBuf = new Float32Array(1024);
    this.out.connect(this.meter);
    if (guard && natural) {
      const dest = ctx.createMediaStreamDestination();
      this.out.connect(dest);
      this.guardEl = new Audio(); this.guardEl.autoplay = true; this.guardEl.srcObject = await loopback(dest.stream);
      await this.guardEl.play().catch(() => {});
    } else this.out.connect(ctx.destination);
    this.watch = setInterval(() => this.track(), 100);
    if (natural) this.makeClips();
  }

  close() { clearInterval(this.watch); clearTimeout(this.clipTimer); this.current = null; this.clipsBy = new Map(); this.ahead = new Map(); }

  // The clips of the voice in use, asked of the voice once.
  makeClips() {
    const c = this.clips;
    if (c.asked || !this.tts) return;
    c.asked = true;
    [...CLIPS.cue, ...CLIPS.ack, ...CLIPS.nod].forEach((text) => this.tts.postMessage({ type: 'clip', text, v: this.voice }));
  }
  // A new voice: its clips are made a moment later and not while a reply is
  // being made (each clip made is time the reply's next words wait), and not
  // at all if this voice had them made before.
  setVoice(id) {
    if (!this.tts) return;
    this.tts.postMessage({ type: 'voice', v: id });
    this.voice = id;
    clearTimeout(this.clipTimer);
    const later = () => {
      if (this.current && !this.current.synthDone) { this.clipTimer = setTimeout(later, CLIPS_RETRY_MS); return; }
      this.makeClips();
    };
    if (!this.clips.asked) this.clipTimer = setTimeout(later, CLIPS_AFTER_VOICE_MS);
  }
  setPace(p) { this.pace = p; if (this.tts) this.tts.postMessage({ type: 'speed', v: p }); }

  // ---------- saying a reply ----------
  // A line of the agent's, said through its Speakable; the end of a line is
  // the end of a sentence, so it is sent on at once.
  say(r, text) {
    this.current = r;
    for (const w of text.match(/\s*\S+\s*/g) || []) this.words(r, r.fmt.push(w));
    this.words(r, r.fmt.end());
    r.fmt = new Speakable();
    if (this.natural) { r.synthDone = false; this.tts.postMessage({ type: 'flush', gen: r.gen }); }
    else if (this.buf.trim()) this.utter(r, this.flushBuf());
  }

  // ---------- made ahead ----------
  // A line in a voice of its own that must wait for the reply being heard:
  // its sound is made meanwhile and kept, so it starts the moment it may
  // (made only once it could be heard, there is a silence between the two
  // while its first words are made). Returns the reply it will be (take()
  // plays it), or null where nothing is made ahead: the device's voice, or
  // too much waiting already (AHEAD_MAX_S of sound).
  // into: a reply being made ahead that this line joins.
  prepare(text, { voice, pace } = {}, into) {
    if (!this.natural) return null;
    let p = into;
    if (!p) {
      let waiting = 0;
      for (const q of this.ahead.values()) for (const a of q.made) waiting += a.buf.duration;
      if (waiting > AHEAD_MAX_S || this.ahead.size >= AHEAD_MAX_REPLIES) return null;
      p = newReply({ voice: voice || '', pace: pace || 0, made: [], madeAll: false });
      this.ahead.set(p.gen, p);
    }
    for (const w of text.match(/\s*\S+\s*/g) || []) this.words(p, p.fmt.push(w));
    this.words(p, p.fmt.end());
    p.fmt = new Speakable();
    this.tts.postMessage({ type: 'flush', gen: p.gen });
    return p;
  }
  // The reply made ahead is the one being said now: what is made of it plays
  // at once, the rest as it comes.
  // (False for one let go meanwhile: there is nothing of it to play.)
  take(p) {
    if (!this.ahead.delete(p.gen)) return false;
    this.current = p;
    p.synthDone = !!p.madeAll;
    for (const a of p.made.splice(0)) this.play(p, a.buf, a.text, false);
    return true;
  }
  // The replies made ahead and not yet taken.
  waiting() { return [...this.ahead.values()]; }
  // Made ahead and not wanted after all.
  drop(p) { p.dropped = true; if (this.ahead.delete(p.gen) && this.tts) this.tts.postMessage({ type: 'cancel', gen: p.gen }); }

  // No more for this reply: it finishes once all of it has been heard.
  end(r) {
    if (this.natural) { this.tts.postMessage({ type: 'end', gen: r.gen }); return; }
    if (this.buf.trim()) this.utter(r, this.flushBuf());
    const poll = setInterval(() => {
      if (this.current !== r || !speechSynthesis.speaking) { clearInterval(poll); if (this.current === r) this.on.finished(r); }
    }, 100);
  }

  // Spoken text as it goes to the voice (r.spoken: where "go on" picks up).
  words(r, s) { r.spoken = (r.spoken || '') + s; for (const w of s.match(/\s*\S+/g) || []) this.word(r, w); }

  // Kokoro gets a phrase as soon as there is one (the voice starts sooner);
  // after the first, a fast voice gets whole sentences (better intonation),
  // a slow one keeps to phrases. The device's voice gets the first phrase at
  // once, then whole sentences, which it phrases better.
  word(r, w) {
    if (this.natural) {
      // (A reply with a voice or a pace of its own is made in them.)
      this.tts.postMessage(r.voice || r.pace ? { type: 'push', gen: r.gen, text: w, voice: r.voice || undefined, speed: r.pace || undefined } : { type: 'push', gen: r.gen, text: w });
      r.words = (r.words || 0) + 1;
      const max = r.firstOut ? (this.speed >= 3 ? 28 : 12) : 6;
      if (/[.!?]["')]*\s*$/.test(w)) { r.words = 0; r.firstOut = true; }
      else if ((/[,;:]\s*$/.test(w) && r.words >= 3) || r.words >= max) { r.words = 0; r.firstOut = true; this.tts.postMessage({ type: 'flush', gen: r.gen }); }
      return;
    }
    this.buf += w;
    const n = this.buf.trim().split(/\s+/).length;
    if (/[.!?]\s*$/.test(this.buf) || (!r.firstOut && ((/[,;:]\s*$/.test(this.buf) && n >= 3) || n >= 6))) { r.firstOut = true; this.utter(r, this.flushBuf()); }
  }
  flushBuf() { const s = this.buf; this.buf = ''; return s; }

  utter(r, s) {
    const u = new SpeechSynthesisUtterance(s);
    u.rate = this.pace;
    u.onstart = () => { if (this.current !== r) return; r.playing = true; this.on.started(r, 'its answer'); };
    let failed = false;
    u.onend = () => { if (!failed && this.current === r) { r.heard += s; this.on.heard(r); } };
    // Stopped on purpose (talked over, cancelled) is not a failure.
    u.onerror = (e) => {
      if (/^(interrupted|canceled)$/.test(e.error)) return;
      failed = true;
      if (this.on.failed) this.on.failed(r, s, e.error || 'error');
    };
    speechSynthesis.speak(u);
  }

  // ---------- Kokoro's audio ----------
  onWorker(m) {
    // A clip belongs to the voice it was made in, which may no longer be the one in use.
    if (m.type === 'clip') { const c = this.clipsOf(m.v ?? this.voice); (CLIPS.cue.includes(m.text) ? c.cue : CLIPS.nod.includes(m.text) ? c.nod : c.ack).push({ text: m.text, buf: this.buffer(m.pcm, m.rate) }); return; }
    // The sound of a reply made ahead is kept until it is taken.
    const p = this.ahead.get(m.gen);
    if (p) {
      if (m.type === 'audio') p.made.push({ buf: this.buffer(m.pcm, m.rate), text: m.text });
      else if (m.type === 'done') p.madeAll = true;
      return;
    }
    const r = this.current;
    if (!r || m.gen !== r.gen) return;
    if (m.type === 'audio') { clearTimeout(r.ackTimer); this.play(r, this.buffer(m.pcm, m.rate), m.text, false); }
    else if (m.type === 'done') r.synthDone = true;
    else if (m.type === 'error') console.warn('tts', m.message);
  }

  // Kokoro pads each piece with silence: trimmed, the voice starts sooner and
  // pieces join as speech does. Every piece at the same loudness, and a few
  // ms of fade at each end so the joins are silent.
  buffer(pcm, rate) {
    const thr = 0.01; let a = 0, b = pcm.length - 1;
    while (a < b && Math.abs(pcm[a]) < thr) a++;
    while (b > a && Math.abs(pcm[b]) < thr) b--;
    a = Math.max(0, a - Math.round(0.01 * rate)); b = Math.min(pcm.length, b + Math.round(0.05 * rate));
    const x = pcm.slice(a, b), fade = Math.min(Math.round(0.004 * rate), x.length >> 1);
    let peak = 0; for (let i = 0; i < x.length; i++) peak = Math.max(peak, Math.abs(x[i]));
    const rms = rmsOf(x), k = rms > 1e-4 ? Math.min(T.targetRms / rms, 0.95 / Math.max(peak, 1e-4)) : 1;
    for (let i = 0; i < x.length; i++) x[i] *= k;
    for (let i = 0; i < fade; i++) { const g = i / fade; x[i] *= g; x[x.length - 1 - i] *= g; }
    const buf = this.ctx.createBuffer(1, Math.max(1, x.length), rate);
    buf.copyToChannel(x, 0);
    return buf;
  }

  // Back to back, with the breath speech has: longer at a full stop.
  play(r, buf, text, ack) {
    const prev = r.segs[r.segs.length - 1], pt = prev ? prev.text.trim() : '';
    const gap = !prev ? 0 : prev.ack ? 0.1 : /[.!?]["')]*$/.test(pt) ? 0.16 : /[,;:]$/.test(pt) ? 0.06 : 0.015;
    const at = Math.max(this.ctx.currentTime + 0.02, r.playHead + gap);
    const src = this.ctx.createBufferSource(); src.buffer = buf; src.connect(this.out); src.start(at);
    r.playHead = at + buf.duration;
    r.sources.push(src); r.segs.push({ text, start: at, end: at + buf.duration, ack });
    r.playing = true;
    setTimeout(() => { if (this.current === r) this.on.started(r, ack ? `"${text}" first` : 'its answer'); }, Math.max(0, (at - this.ctx.currentTime) * 1000));
  }

  // What has been heard of the current reply, and whether all of it has.
  // A reply made ahead may have its first words made before the rest of the
  // reply being said, once there is sound enough of that one already made
  // to cover the time they take (else the one being said would run dry
  // mid-sentence). Asked of the voice once a reply.
  hurry(p) {
    const r = this.current;
    if (p.hurried || !r || r === p || r.synthDone) return; // (all of it made: the voice is free for p as it is)
    const unplayed = r.playHead - this.ctx.currentTime;
    const need = Math.max(FIRST_MIN_S, FIRST_MARGIN * (FIRST_WORDS_S / Math.max(this.speed || 1, 0.1)));
    if (unplayed < need) return;
    p.hurried = true;
    this.tts.postMessage({ type: 'first', gen: p.gen });
  }
  track() {
    const r = this.current;
    if (!r || !this.natural) return;
    for (const p of this.ahead.values()) this.hurry(p);
    const t = this.ctx.currentTime;
    const heard = r.segs.filter((s) => !s.ack && s.start <= t).map((s) => s.text + ' ').join('');
    if (heard.trim() !== r.heard.trim()) { r.heard = heard; this.on.heard(r); }
    if (r.playing && r.synthDone && !r.writing && t >= r.playHead) this.on.finished(r);
  }

  // ---------- clips and sounds ----------
  ack(r) {
    if (!this.natural || !this.clips.ack.length) return false;
    const pick = this.clips.ack.filter((a) => a.text !== this.lastAck);
    const a = pick[Math.floor(Math.random() * pick.length)] || this.clips.ack[0];
    this.lastAck = a.text;
    this.play(r, a.buf, a.text, true);
    return true;
  }
  // A made clip said as a reply's line (a cut-in's "Sorry,"), at once; false
  // if there is none (the device's voice, or not made yet): say it instead.
  cue(r, text) {
    const a = this.natural && this.clips.cue.find((c) => c.text === text);
    if (!a) return false;
    this.current = r;
    r.spoken = (r.spoken || '') + text + ' ';
    this.play(r, a.buf, a.text, false);
    r.synthDone = true; // nothing for the voice to make: it is all here
    return true;
  }
  // A quiet "mm-hm" at a pause in long talk; true if one played. Its own sound
  // coming back through the mic is not you talking (see nodding()).
  nod() {
    if (!this.natural || !this.clips.nod.length) return false;
    const a = this.clips.nod[Math.floor(Math.random() * this.clips.nod.length)];
    const src = this.ctx.createBufferSource(), g = this.ctx.createGain();
    g.gain.value = 0.55; src.buffer = a.buf; src.connect(g); g.connect(this.out); src.start();
    this.nodUntil = performance.now() + a.buf.duration * 1000 + 200;
    return true;
  }
  nodding() { return performance.now() < this.nodUntil; }
  // A soft two-note pulse: it heard you and is working on it.
  pulse() {
    const t = this.ctx.currentTime;
    [[523, 0], [659, 0.11]].forEach(([f, d]) => {
      const o = this.ctx.createOscillator(), g = this.ctx.createGain();
      o.type = 'sine'; o.frequency.value = f;
      g.gain.setValueAtTime(0, t + d); g.gain.linearRampToValueAtTime(0.035, t + d + 0.02); g.gain.exponentialRampToValueAtTime(0.0005, t + d + 0.16);
      o.connect(g); g.connect(this.out); o.start(t + d); o.stop(t + d + 0.2);
    });
  }

  // ---------- state ----------
  // Its voice can be heard now. The natural voice: with a moment's grace after
  // each piece, while the next is made.
  audible(r) {
    if (!r) return false;
    return this.natural ? this.ctx.currentTime < r.playHead + 0.8 : speechSynthesis.speaking;
  }
  // Whether it has started saying something other than its "Okay.".
  answering(r) { return r.playing && (!this.natural || r.segs.some((s) => !s.ack)); }
  // What of r has been heard to its end (where "go on" picks up).
  heardWhole(r) { return this.natural ? r.segs.filter((s) => !s.ack && s.end <= this.ctx.currentTime).map((s) => s.text).join(' ') : r.heard; }
  // How loud its voice is now, 0 to 1. The device's voice cannot be read from
  // the page: while it is speaking, a stand-in that rises and falls.
  level(r) {
    if (!this.natural) {
      if (!r || !speechSynthesis.speaking) return 0;
      const s = performance.now() / 1000;
      return 0.2 + 0.5 * Math.abs(Math.sin(s * 8.5 + 2 * Math.sin(s * 1.7))) * (0.65 + 0.35 * Math.random());
    }
    this.meter.getFloatTimeDomainData(this.meterBuf);
    return levelOf(rmsOf(this.meterBuf));
  }

  duck(on) { if (this.natural) this.out.gain.setTargetAtTime(on ? T.duckGain : 1, this.ctx.currentTime, 0.02); }

  // Stopped at once, without a trace (a reply left behind).
  cancel(r) {
    this.buf = '';
    if (this.current === r) this.current = null;
    if (this.natural) { this.tts.postMessage({ type: 'cancel', gen: r.gen }); r.sources.forEach((s) => { try { s.stop(); } catch (_) {} }); }
    else stopSynthesis();
  }

  // Talked over: a quick fade, not a click, the way a person trails off.
  fadeOut(r) {
    if (this.current === r) this.current = null;
    if (!this.natural) { this.buf = ''; stopSynthesis(); return; }
    this.tts.postMessage({ type: 'cancel', gen: r.gen });
    const g = this.out.gain, t = this.ctx.currentTime;
    g.cancelScheduledValues(t); g.setTargetAtTime(0, t, 0.012);
    setTimeout(() => {
      r.sources.forEach((s) => { try { s.stop(); } catch (_) {} });
      g.cancelScheduledValues(this.ctx.currentTime); g.setValueAtTime(1, this.ctx.currentTime);
    }, 70);
  }
}

// Chrome stays paused after pause() then cancel(): resume, or new speech
// would queue silently.
function stopSynthesis() { speechSynthesis.cancel(); speechSynthesis.resume(); }

// The echo guard: the reply played through a local WebRTC loopback, which is
// the audio the browser's echo canceller listens for (plain Web Audio output
// is not cancelled on every browser, and the agent would hear itself).
async function loopback(stream) {
  const a = new RTCPeerConnection(), b = new RTCPeerConnection();
  a.onicecandidate = (e) => e.candidate && b.addIceCandidate(e.candidate);
  b.onicecandidate = (e) => e.candidate && a.addIceCandidate(e.candidate);
  // No jitter buffer: this "network" is in the same page.
  const got = new Promise((res) => { b.ontrack = (e) => { try { e.receiver.playoutDelayHint = 0; e.receiver.jitterBufferTarget = 0; } catch (_) {} res(e.streams[0]); }; });
  stream.getTracks().forEach((t) => a.addTrack(t, stream));
  const offer = await a.createOffer(); await a.setLocalDescription(offer); await b.setRemoteDescription(offer);
  const answer = await b.createAnswer(); await b.setLocalDescription(answer); await a.setRemoteDescription(answer);
  return got;
}
