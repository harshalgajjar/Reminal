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
// answer, and an "mm-hm" while you talk at length.
export const CLIPS = { ack: ['Okay.', 'Sure.', 'Got it.', 'Right.', 'Mm, okay.'], nod: ['Mm-hm.', 'Mhm.'] };

let nextGen = 0;
export function newReply(fields) {
  return { gen: ++nextGen, fmt: new Speakable(), segs: [], sources: [], playHead: 0, playing: false, synthDone: false, heard: '', ...fields };
}

export class Speaker {
  // on: { started(r, what), heard(r), finished(r) }
  constructor(on) { this.on = on; this.clips = { ack: [], nod: [] }; this.lastAck = ''; this.lastNodT = 0; this.nodUntil = 0; this.buf = ''; }

  // natural: Kokoro through `tts` (its worker), at `speed` x real time; else
  // the device's voice. guard: play through the echo canceller's loopback.
  async open({ ctx, natural, tts, speed, guard, pace }) {
    Object.assign(this, { ctx, natural, tts, speed, pace, current: null });
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

  close() { clearInterval(this.watch); this.current = null; this.clips = { ack: [], nod: [] }; }

  makeClips() { this.clips = { ack: [], nod: [] }; [...CLIPS.ack, ...CLIPS.nod].forEach((text) => this.tts.postMessage({ type: 'clip', text })); }
  setVoice(id) { if (this.tts) { this.tts.postMessage({ type: 'voice', v: id }); this.makeClips(); } }
  setPace(p) { this.pace = p; if (this.tts) this.tts.postMessage({ type: 'speed', v: p }); }

  // ---------- saying a reply ----------
  // A line of the agent's, said through its Speakable; the end of a line is
  // the end of a sentence, so it is sent on at once.
  say(r, text) {
    this.current = r;
    for (const w of text.match(/\s*\S+\s*/g) || []) this.words(r, r.fmt.push(w));
    this.words(r, r.fmt.end());
    r.fmt = new Speakable();
    if (this.natural) this.tts.postMessage({ type: 'flush', gen: r.gen });
    else if (this.buf.trim()) this.utter(r, this.flushBuf());
  }

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
      this.tts.postMessage({ type: 'push', gen: r.gen, text: w });
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
    u.onend = () => { if (this.current === r) { r.heard += s; this.on.heard(r); } };
    speechSynthesis.speak(u);
  }

  // ---------- Kokoro's audio ----------
  onWorker(m) {
    if (m.type === 'clip') { (CLIPS.nod.includes(m.text) ? this.clips.nod : this.clips.ack).push({ text: m.text, buf: this.buffer(m.pcm, m.rate) }); return; }
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
  track() {
    const r = this.current;
    if (!r || !this.natural) return;
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
