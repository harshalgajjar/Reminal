// The conversation: who is talking, when your turn ends, what the agent's
// replies do, and what happens when you talk over each other. One state
// machine, in sections:
//
//   hearing       mic frames (from the voice-activity model) into utterances
//   words         live and final transcription (stt-worker.js)
//   your turn     deciding it is over, and sending it; long talk in parts
//   replies       the agent's lines, said as they come (speaker.js)
//   talking over  ducking, stopping it, "go on"
//   while you talk lines held back, and the agent cutting in on long talk
//   the agent     its state from the viewer, switching sessions
//   controls      hold, mute, stop
//
// What reaches the agent goes through bridge.js as text; what is shown goes
// through panel.js.
import { T, FRAME_MS } from './tuning.js';
import { Speakable } from './speakable.js';
import { newReply } from './speaker.js';
import { micFrame } from './level.js';
import * as panel from './panel.js';
import { send } from './bridge.js';

const now = () => performance.now();
const log = (...a) => console.log('[voice]', ...a);

// A thought that stops on one of these is not finished, however it sounded.
const HANGING = /(\b(and|but|because|or|um+|uh+|er+|hmm+|the|a|an|to|of|with|if|which|my|your|in|for|at)\W*|\.\.\.|…)\s*$/i;
// Talking over it: a backchannel only dips its voice; a stop word stops it.
const BACKCHANNEL = new Set(['yeah', 'yes', 'yep', 'yup', 'mm', 'mhm', 'mmhmm', 'mmhm', 'hmm', 'uhhuh', 'uh', 'huh', 'okay', 'ok', 'right', 'sure', 'cool', 'nice', 'alright', 'true', 'exactly', 'got', 'it', 'i', 'see', 'go', 'on', 'oh', 'ah', 'great', 'wow']);
const STOP_WORDS = new Set(['stop', 'wait', 'no', 'hold', 'pause', 'hang', 'quiet', 'shush', 'enough', 'cancel', 'actually', 'hey', 'sorry', 'but']);
const COMMON = new Set(['the', 'a', 'an', 'of', 'to', 'in', 'on', 'and', 'or', 'is', 'it', 'its', 'i', 'you', 'this', 'that', 'for', 'with', 'be', 'are', 'was', 'so', 'but', 'my', 'your', 'me', 'we', 'at', 'as', 'all']);
const GO_ON = /^(ok(ay)?,? )?(go on|go ahead|continue|carry on|keep going|sorry,? go on|you were saying|what were you saying|never ?mind,? go on)[.!?]*$/i;
const wordsOf = (s) => s.toLowerCase().replace(/[^a-z' ]/g, ' ').replace(/'/g, '').split(/\s+/).filter(Boolean);
const joinFrames = (frames) => { let n = 0; for (const f of frames) n += f.length; const o = new Float32Array(n); let i = 0; for (const f of frames) { o.set(f, i); i += f.length; } return o; };
const hasWords = (s) => /[a-z0-9]/i.test(s || '');

const STATUS = {
  listening: ['Listening', 'listen'], hearing: ['Hearing you', 'hear'], ducked: ['Hearing you (lowered its voice)', 'duck'],
  thinking: ['Thinking', 'think'], speaking: ['Speaking (talk over it to stop)', 'speak'], working: ['Working', 'think'],
  muted: ['Muted: it cannot hear you', ''],
};
const show = (k) => panel.status(...STATUS[k]);

// ---------- state ----------
let stt, speaker, turnModel, nods;      // set by start()
export let running = false;
export let muted = false;
let holding = false, ducked = false;

// You: 'quiet', 'talking', or 'pausing' (quiet, but the turn not decided).
let you = 'quiet';
let hot = 0, preRoll = [], frames = [], uttId = 0, voicedMs = 0, silenceMs = 0;
let lastVoiceT = 0, uttStartT = 0, lastPartialT = 0, partialBusy = false, liveRow = null, liveBefore = '';
let finalStarted = false, checked = false, rechecked = false, checkVoiced = -1, thinking = false;
let levels = [], yourLevel = 0;           // how loud you are at the mic, learnt over turns
export let partsSent = 0;                 // parts of the current long talk already sent

// The agent's current reply, and what came of earlier ones.
let reply = null;
let lastHadAck = false, cutReply = null, bargeT0 = 0, bargeKind = '';
let agentState = '';
const afterReply = [];                    // lines it said while a finished reply was still being heard
let held = [], heldRow = null;            // lines it said while you were talking
let cutIn = null, cutNote = '';           // a cut-in waiting for you to stop, or one you talked over
let cutLate = null;                       // one judged talked over, still taken if you stop soon after
let beforeCut = [];                       // what you said up to stopping for a cut-in: sent with your next turn

export function start(deps) {
  ({ stt, speaker, turnModel, nods } = deps);
  running = true;
  you = 'quiet'; hot = 0; preRoll = []; frames = []; partsSent = 0;
  show('listening');
}

// Another voice from here on (the device's own could not speak): a reply
// under way carries on in it.
export function useSpeaker(s) {
  speaker = s;
  speaker.current = reply;
}

// A line of the agent's its voice could not say: it stays on screen, marked
// not heard, and the session is told, so speak says so to the agent.
export function lineUnheard(r, text) {
  if (r && r.row) panel.setRowNote(r.row, 'not heard: this device could not say it');
  send('unheard', { text });
}

export function stop() {
  held = []; heldRow = null; cutIn = null; cutNote = ''; cutLate = null; beforeCut = [];
  running = false;
  if (reply) cutOff(now(), { quietly: true });
  muted = holding = false;
}

// The agent's voice can be heard now (not merely a reply under way: it works
// between lines in silence, and talking then is not talking over it).
export function agentSpeaking() { return !!reply && reply.playing && speaker.audible(reply); }
export function agentLevel() { return speaker && running ? speaker.level(reply) : 0; }

// ---------- hearing ----------
export function onFrame(probs, frame) {
  if (!running) return;
  micFrame(frame);
  const t = now(), p = probs.isSpeech;
  if (muted) { hot = 0; return; }
  if (speaker.nodding()) { if (you === 'quiet') hot = 0; else { frames.push(frame); silenceMs += FRAME_MS; } return; }
  let e = 0; for (let i = 0; i < frame.length; i++) e += frame[i] * frame[i];
  const rms = Math.sqrt(e / frame.length);
  if (you !== 'quiet' && p >= T.keepP) levels.push(rms);

  if (you === 'quiet') { maybeStart(t, p, rms, frame); return; }

  frames.push(frame);
  if (p >= T.keepP) voiced(t); else quiet(t);
  if (you === 'quiet') return;
  if (!ducked) maybeSendPart(t);
  maybeTranscribeLive(t);
}

function maybeStart(t, p, rms, frame) {
  preRoll.push(frame); if (preRoll.length > T.preRollFrames) preRoll.shift();
  const over = agentSpeaking();
  const need = over ? T.startPWhileSpeaking : T.startP, n = over ? T.startFramesWhileSpeaking : T.startFrames;
  // Speech much quieter than you (a TV, people across the room) does not start a turn.
  const near = !yourLevel || rms >= yourLevel * T.nearFactor;
  hot = p > need && near ? hot + 1 : 0;
  if (hot < n) return;
  you = 'talking'; uttId++; frames = preRoll.slice(); preRoll = [];
  voicedMs = hot * FRAME_MS; silenceMs = 0; checked = thinking = false;
  uttStartT = t - hot * FRAME_MS; lastVoiceT = t; lastPartialT = 0; hot = 0; liveRow = null;
  if (over) { bargeT0 = uttStartT; bargeKind = ''; duck(true); panel.timeDuck(t - bargeT0); return; }
  // A reply not yet heard waits for your whole thought; one that has spoken
  // and is working in silence ends here, what it said kept.
  if (reply) { if (reply.playing) finishReply(); else dropReply(reply); }
  show('hearing');
}

function voiced(t) {
  voicedMs += FRAME_MS; silenceMs = 0; lastVoiceT = t;
  checked = rechecked = finalStarted = thinking = false;
  if (cutIn && cueHeardFor(t) > T.cutInWaitMs) cutInDecided(false);
  if (cutLate && t - cutLate.at > T.cutInLateMs) cutLate = null; // talked on past it: no taking it late
  if (you !== 'talking') { you = 'talking'; show(ducked ? 'ducked' : 'hearing'); }
  // Words decide first (overheard); this is for speech the text model cannot
  // make out. A backchannel it did make out may run longer.
  if (ducked && voicedMs >= (bargeKind === 'back' ? T.backchannelMaxMs : T.bargeStopMs)) cutOff(t);
}

function quiet(t) {
  silenceMs += FRAME_MS;
  if (cutIn && cueStarted(t) && silenceMs >= T.cutInStopMs) cutInDecided(true);
  else if (cutLate && silenceMs >= T.cutInStopMs) { if (t - cutLate.at <= T.cutInLateMs) cutInTakenLate(); else cutLate = null; }
  if (you === 'quiet') return;
  if (ducked) { if (silenceMs >= T.backchannelQuietMs) liftDuck(); return; }
  // The final words, started a moment before the turn model is asked, so they
  // are ready when it says the turn is over.
  if (!checked && !finalStarted && silenceMs >= T.finalSilenceMs) { finalStarted = true; requestFinal(); }
  if ((!checked && silenceMs >= T.checkSilenceMs) || (checked && !rechecked && silenceMs >= T.recheckSilenceMs)) askTurnModel(t);
  // Where the turn model is too slow to wait for (phones), a short pause ends
  // the turn once the words are in, unless they leave the thought hanging.
  const words = latestFinal();
  if (turnModel.slow && you === 'pausing' && !holding && !thinking && silenceMs >= T.fastCommitMs && words && words.text != null) {
    if (HANGING.test(words.text)) thinking = true; else { endTurn(t, 'a pause (the turn model is slow on this device)'); return; }
  }
  if (you !== 'quiet' && !holding && silenceMs >= (thinking ? T.maxSilenceThinkingMs : T.maxSilenceMs)) endTurn(t, thinking ? 'a long pause' : 'silence');
}

// The turn model at the first short pause; if it says "not finished", once
// more a little later, as a longer pause can change its mind.
function askTurnModel(t) {
  if (checked) rechecked = true; else checked = true;
  checkVoiced = voicedMs; you = 'pausing';
  const id = uttId, audio = joinFrames(frames);
  if (!latestFinal()) requestFinal();
  if (turnModel.slow) return; // 1.5 s of it would block this page
  turnModel.finished(audio).then((prob) => {
    const stale = () => id !== uttId || you !== 'pausing' || voicedMs !== checkVoiced || holding;
    if (stale()) return;
    // The words, once in, get a say too: a thought left hanging on "and" or
    // "um" is not finished, whatever it sounded like.
    const decide = () => {
      if (stale()) return;
      const words = latestFinal(), hanging = words && words.text != null && HANGING.test(words.text);
      if (prob > 0.5 && !hanging) { endTurn(t, `the turn model (${prob.toFixed(2)})`); return; }
      thinking = true;
      nod();
      if (frames.length * FRAME_MS >= T.partEverySec * 1000) sendPart('a pause in long talk');
    };
    const words = latestFinal();
    if (words && words.text == null) words.waiting.push(decide); else decide();
  });
}

function learnYourLevel() {
  // The middle of the loud half of your speech, averaged over turns.
  if (levels.length < 10) { levels = []; return; }
  const s = levels.sort((a, b) => a - b), mid = s[Math.floor(s.length * 0.75)];
  yourLevel = yourLevel ? yourLevel * 0.7 + mid * 0.3 : mid;
  levels = [];
}

// ---------- words ----------
// Finals asked for and not back yet, by utterance and how much speech they
// cover: each turn gets its own words, however late they come.
const finals = new Map();
let latest = null;      // the newest final of the current utterance
let settled = null;     // {id, len, text}: the start of the utterance already transcribed

// Only the audio after what is settled is transcribed again, so a long
// thought with pauses costs no more at its end than its last part.
function unsettled(audio, id) {
  return settled && settled.id === id && audio.length > settled.len ? { audio: audio.subarray(settled.len), before: settled.text } : { audio, before: '' };
}
function latestFinal() { return latest && latest.id === uttId && latest.voiced === voicedMs ? latest : null; }

function requestFinal() {
  const audio = joinFrames(frames), id = uttId, rest = unsettled(audio, id);
  const f = { id, voiced: voicedMs, text: null, ms: 0, waiting: [], before: rest.before, len: audio.length };
  latest = f;
  finals.set(id + ':' + voicedMs, f);
  stt.postMessage({ type: 'transcribe', id: 'f' + id + ':' + voicedMs, final: true, audio: rest.audio });
  return f;
}

function maybeTranscribeLive(t) {
  const every = ducked ? T.partialEveryMsOverlap : T.partialEveryMs;
  if (you !== 'talking' || silenceMs !== 0 || partialBusy || t - lastPartialT < every || frames.length <= 8) return;
  partialBusy = true; lastPartialT = t;
  const rest = unsettled(joinFrames(frames), uttId);
  liveBefore = rest.before;
  const a = rest.audio.length > T.partialMaxSamples ? rest.audio.subarray(rest.audio.length - T.partialMaxSamples) : rest.audio;
  stt.postMessage({ type: 'transcribe', id: 'p' + uttId, voiced: voicedMs, final: false, audio: a });
}

export function onWords(m) {
  if (m.type === 'error') {
    console.warn('stt', m.message);
    partialBusy = false;
    // A final that failed is a stretch with no words: the turn it ends still ends.
    if (typeof m.id === 'string' && m.id[0] === 'f') onWords({ type: 'text', id: m.id, final: true, text: '', ms: 0 });
    return;
  }
  if (!m.final) { live(m); return; }
  const [id, voiced] = m.id.slice(1).split(':').map(Number);
  const f = finals.get(id + ':' + voiced);
  if (!f) return;
  finals.delete(id + ':' + voiced);
  f.text = (f.before ? f.before + ' ' : '') + m.text;
  f.ms = m.ms;
  if (m.text && id === uttId) settled = { id, len: f.len, text: f.text };
  f.waiting.splice(0).forEach((go) => go());
}

function live(m) {
  partialBusy = false;
  panel.timeLiveText(m.ms, m.secs);
  if (m.id !== 'p' + uttId || you === 'quiet') return;
  if (ducked && overheard(m.text || '', m.voiced)) return;
  if (!m.text) return;
  if (!liveRow) liveRow = panel.addRow('you');
  panel.setRowText(liveRow, (liveBefore ? liveBefore + ' ' : '') + m.text, 'partial');
}

// ---------- your turn ----------
function endTurn(t, why) {
  learnYourLevel();
  if (you === 'quiet') return;
  // You stopped: the cut-in is said (also one judged talked over a moment ago).
  if (cutIn) { cutInDecided(true); return; }
  if (cutLate && now() - cutLate.at <= T.cutInLateMs) { cutInTakenLate(); return; }
  const endT = lastVoiceT, decidedT = now(), parts = partsSent;
  you = 'quiet'; preRoll = []; partsSent = 0;
  show('thinking');
  // This turn's own row and words: talk that starts before they are in gets
  // its own, and cannot take these.
  const row = liveRow || panel.pendingRow();
  liveRow = null;
  const f = latestFinal() || requestFinal();
  const take = () => turnTaken({ text: (f.text || '').trim(), voiced: f.voiced, ms: f.ms, why, parts, endT, decidedT, row });
  if (f.text != null) take(); else f.waiting.push(take);
}

function turnTaken({ text, voiced, ms, why, parts, endT, decidedT, row }) {
  // After parts of long talk, a tail with hardly any speech in it is a breath
  // the text model made a word of.
  if (parts && voiced < T.tailMinVoicedMs) text = '';
  const recentCut = cutReply && now() - cutReply.at < T.resumeWithinMs;
  if (!hasWords(text)) {
    row.remove();
    // Long talk sent in parts still needs its end, or the agent waits for more.
    if (parts) toAgent('said', '(that is all)');
    // Nothing new from you: what it said meanwhile is said now.
    releaseHeld(!parts);
    if (recentCut && now() - cutReply.at < T.resumeNothingMs) { goOn('it was nothing'); return; }
    show('listening');
    return;
  }
  if (recentCut && (GO_ON.test(text) || (wordsOf(text).length <= 2 && wordsOf(text).every((w) => BACKCHANNEL.has(w))))) {
    panel.setRowText(row, text);
    releaseHeld(false);
    goOn('you said go on');
    return;
  }
  panel.setRowText(row, text);
  panel.setRowNote(row, `turn ended by ${why}${parts ? `, after ${parts} ${parts === 1 ? 'part' : 'parts'} sent while you talked` : ''}; final text took ${ms} ms`);
  releaseHeld(false); // it answers what you said; what it said meanwhile stays on screen
  replyTo({ endT, decidedT, textT: now(), why, text, row });
}

// Long talk: at a pause between sentences once there is enough of it, or
// when it runs on too long without one, what has been said so far goes to
// the agent as a part ("[voice, continues]"), so it can follow along and cut
// in if asked to. The talk goes on as a new utterance of the same turn.
function maybeSendPart(t) {
  const talked = frames.length * FRAME_MS;
  if (talked > T.maxUttSec * 1000) sendPart('long without a pause');
  else if (turnModel.slow && talked >= T.partEverySec * 1000 && silenceMs >= T.partPauseMs && silenceMs < T.fastCommitMs) sendPart('a pause in long talk');
}

// No part while a cut-in waits for you to stop (or was judged talked over a
// moment ago): the pause may be you stopping for it, and if you do, what you
// said after it came in is dropped, not sent. A part held now goes once the
// cut-in is decided either way.
function cutInWaiting() { return !!cutIn || (!!cutLate && now() - cutLate.at <= T.cutInLateMs); }
function sendPart(why) {
  if (cutInWaiting()) return;
  const row = liveRow || panel.pendingRow();
  liveRow = null;
  const f = requestFinal();
  log('part', why, (f.len / 16000).toFixed(1) + ' s');
  f.waiting.push(() => {
    const text = (f.text || '').trim();
    if (!hasWords(text)) { row.remove(); return; }
    panel.setRowText(row, text);
    panel.setRowNote(row, 'sent while you were still talking');
    toAgent('part', text);
  });
  partsSent++;
  // The rest of the talk: a new utterance. A "not finished" from the turn
  // model still stands: the pause that sent this part may be the thought's end.
  uttId++; frames = []; preRoll = []; voicedMs = 0; settled = null; latest = null; liveBefore = '';
  finalStarted = false; lastPartialT = 0;
  if (you === 'pausing') you = 'talking';
}

// A quiet "mm-hm" at a thinking pause in long talk, as a listener nods.
let lastNodT = 0;
function nod() {
  if (!nods()) return;
  const t = now();
  if (t - lastNodT < T.backEveryMs || t - uttStartT < T.backAfterTalkMs) return;
  if (speaker.nod()) lastNodT = t;
}

// Your words to the agent; after a cut-in you talked over, with a note first.
function toAgent(t, text) {
  if (cutNote) { text = `(they talked on over your cut-in: "${cutNote}"; they did not hear it) ` + text; cutNote = ''; cutLate = null; }
  if (beforeCut.length) { text = `(before they stopped for your cut-in they had also said: "${beforeCut.join(' ')}") ` + text; beforeCut = []; }
  send(t, { text });
}

// ---------- replies ----------
// Your turn goes to the agent; its reply is what it says with speak, a line
// at a time, until its turn is over (agentStateIs).
function replyTo(turn) {
  leaveReply();
  reply = newReply({ turn, row: panel.addRow('agent'), text: '', writing: true, saidAt: now() });
  const r = reply;
  speaker.current = r;
  // A slow start gets a quick "Okay." first, as a person would; not on every
  // turn, though, as an "Okay." each time is filler people tire of.
  r.ackTimer = setTimeout(() => { if (reply === r && !r.playing && speaker.ack(r)) r.acked = true; }, lastHadAck ? T.ackAfterMsAgain : T.ackAfterMs);
  pulseWhileStarting(r);
  log('said', r.gen, turn.text.slice(0, 40));
  toAgent('said', turn.text);
}

// It spoke with no turn of yours to answer (a long task finishing, say).
function ownReply() {
  leaveReply();
  const at = now();
  reply = newReply({ turn: { endT: at, decidedT: at, textT: at, why: 'it spoke up', text: '' }, row: panel.addRow('agent'), text: '', writing: true, own: true });
  speaker.current = reply;
  return reply;
}

// A line the agent said with speak.
export function agentSaid(text) {
  if (!running) return;
  let r = reply;
  log('speak', r ? r.gen : '-', text.slice(0, 40));
  // You are talking and it is not mid-reply: never over you. A late line of
  // an answer you have moved on from is held back.
  if (you !== 'quiet' && !(r && r.playing) && !partsSent) { holdBack(text); return; }
  // Cutting in on long talk sent in parts: a cue first.
  if (you !== 'quiet' && partsSent) { offerCutIn(text); return; }
  // Its turn is over but that reply is still being heard: after it, not over it.
  if (r && r.agentDone) { afterReply.push(text); return; }
  if (!r) r = ownReply();
  r.text += (r.text ? ' ' : '') + text;
  r.spoke = true; r.lastLineT = now();
  speaker.say(r, text);
  paint(r);
  if (r.own) { clearTimeout(r.endTimer); r.endTimer = setTimeout(() => endReply(r), T.ownReplyEndMs); }
}

// The agent's turn is over: the reply ends once it has all been heard.
function endReply(r) {
  clearTimeout(r.endTimer);
  if (reply !== r || r.agentDone) return;
  r.agentDone = true; r.writing = false;
  if (!r.spoke && !r.own) { panel.setRowText(r.row, '(it answered on screen)', 'note'); }
  if (r.spoke) speaker.end(r);
  else setTimeout(() => { if (reply === r) finishReply(); }, Math.max(0, (r.playHead - speaker.ctx.currentTime) * 1000));
}

// All of it heard (or you started a new turn after it had spoken).
function finishReply() {
  const r = reply;
  if (!r) return;
  r.heard = r.text; paint(r);
  lastHadAck = !!r.acked;
  reply = null; speaker.current = null;
  if (ducked) duck(false);
  if (you === 'quiet') show('listening');
  // Lines it said while this one was still being heard: a reply of their own.
  afterReply.splice(0).forEach((t) => agentSaid(t));
}

// The current reply, left for a new one: one whose words have been heard
// stays on screen as far as it was heard (its sound stops); one not heard
// yet goes.
function leaveReply() {
  const r = reply;
  if (!r) return;
  if (!speaker.answering(r)) { dropReply(r); return; } // nothing but its "Okay." yet
  [r.ackTimer, r.endTimer, r.pulseTimer].forEach(clearTimeout);
  speaker.cancel(r);
  paint(r);
  afterReply.length = 0;
  reply = null;
}

// A reply not heard yet, dropped without a trace: you went on talking.
function dropReply(r) {
  [r.ackTimer, r.endTimer, r.pulseTimer].forEach(clearTimeout);
  speaker.cancel(r);
  r.row.remove();
  if (reply === r) reply = null;
}

function paint(r) {
  const heard = r.heard.length && r.text.startsWith(r.heard) ? r.heard : '';
  panel.setRowHeard(r.row, heard, r.text.slice(heard.length));
}

// Its first sound: when it started, for the row and the timings.
export function replyStarted(r, what) {
  cueHeard(r);
  if (r.startedAt) return;
  r.startedAt = now();
  const turn = r.turn;
  const took = r.startedAt - turn.endT, decide = turn.textT - turn.endT, firstWords = r.startedAt - turn.textT;
  panel.timeReply({ reply: took, decide, firstWords, why: `${turn.why}; heard ${what}`, counted: !r.own && !r.resumed });
  panel.setRowNote(r.row, r.fromReminal ? 'from reminal, not the agent' : r.own ? 'it spoke up' : r.resumed ? 'picking up where it stopped' : `started speaking ${panel.msText(took)} after you stopped`);
}
export function replyHeard(r) { paint(r); }
export function replyFinished(r) { if (reply === r) finishReply(); }

// Between its lines the agent may be working: the status says so, never
// "Speaking" over silence. A reply whose end never comes (it answered so
// fast that the viewer never saw it working) ends after a quiet spell.
setInterval(() => {
  const r = reply;
  if (!running || !r || r.agentDone || !r.playing) return;
  const calm = you === 'quiet' && !muted && !ducked;
  if (speaker.audible(r)) { r.quietSince = 0; if (calm && panel.statusClass() !== 'speak') show('speaking'); return; }
  if (!r.quietSince) r.quietSince = now();
  const q = now() - r.quietSince;
  if (calm && q > T.quietShowMs && panel.statusClass() === 'speak') show('working');
  if (agentState !== 'working' && q > T.replyQuietEndMs && now() - (r.lastLineT || 0) > T.replyQuietEndMs) { log('reply ended by quiet', r.gen); endReply(r); }
}, 200);

// A soft "heard you" while it starts on your turn: a couple at most, only
// until it says something, never while you talk or are muted.
function pulseWhileStarting(r) {
  let n = 0;
  const tick = () => {
    if (reply !== r || r.agentDone || n >= T.pulseMax || speaker.answering(r)) return;
    if (!speaker.audible(r) && you === 'quiet' && !muted) { speaker.pulse(); n++; }
    r.pulseTimer = setTimeout(tick, T.pulseEveryMs);
  };
  r.pulseTimer = setTimeout(tick, T.pulseAfterMs);
}

// ---------- talking over ----------
function duck(on) {
  ducked = on;
  speaker.duck(on);
  if (on) show('ducked');
}

// Its voice comes back up: what you said over it was a backchannel or its own echo.
function liftDuck() {
  duck(false); you = 'quiet'; frames = []; bargeKind = '';
  if (liveRow) { liveRow.remove(); liveRow = null; }
  show('speaking');
}

// What you said over it decides: its own words back through the mic are an
// echo, a "yeah" a backchannel, anything else stops it. True if handled.
function overheard(text, voiced) {
  let w = wordsOf(text.replace(/-/g, ''));
  w = w.filter((x, i) => x !== w[i - 1]); // the text model loops on a fraction of a second ("list list list")
  if (!w.length) return false;
  if (reply && speaker.natural) {
    const t = speaker.ctx.currentTime;
    const recent = new Set(wordsOf(reply.segs.filter((s) => !s.ack && s.start <= t && s.end >= t - 4).map((s) => s.text).join(' ')));
    const meaningful = [...new Set(w.filter((x) => !COMMON.has(x)))];
    if (meaningful.length >= 2 && meaningful.filter((x) => recent.has(x)).length / meaningful.length >= 0.6) { liftDuck(); return true; }
  }
  if (w.length <= 3 && w.every((x) => BACKCHANNEL.has(x))) { bargeKind = 'back'; return true; }
  // The first instant of a word reads as almost anything: a sentence counts
  // once there is enough speech in it.
  if (STOP_WORDS.has(w[0]) || (w.length >= 2 && voiced >= T.overlapWordsMs)) cutOff(now());
  else bargeKind = 'talk';
  return false;
}

// You talked over it: it stops, the agent is told what you heard, and where
// it stopped is kept in case it was nothing or you say "go on". quietly: no
// report and no timing (Stop, or a hold).
function cutOff(t, { quietly = false } = {}) {
  const r = reply;
  ducked = false;
  if (!r) return;
  reply = null;
  afterReply.length = 0;
  clearTimeout(r.ackTimer); lastHadAck = !!r.acked;
  const heard = speaker.natural ? r.segs.filter((s) => !s.ack && s.start <= speaker.ctx.currentTime).map((s) => s.text).join(' ') : r.heard;
  if (!quietly && !r.resumed) { log('cut', r.gen, heard.slice(-40)); send('cut', { text: heard }); }
  // Pick up at the start of the phrase it was in, as a person repeats the phrase they lost.
  const done = speaker.heardWhole(r).split(/\s+/).filter(Boolean).length;
  const remaining = ((r.spoken || '').match(/\S+\s*/g) || []).slice(done).join('');
  cutReply = remaining.trim() ? { remaining, at: now() } : null;
  speaker.fadeOut(r);
  panel.setRowCut(r.row, heard);
  if (!quietly) panel.timeStop(t - bargeT0);
  show('hearing');
}

// After a talk-over that was nothing, or "go on": the rest of what it was
// saying, from where it stopped.
function goOn(why) {
  const c = cutReply;
  cutReply = null;
  leaveReply();
  const at = now();
  reply = newReply({ turn: { endT: lastVoiceT, decidedT: at, textT: at, why: 'resumed: ' + why, text: '' }, row: panel.addRow('agent'), text: c.remaining, writing: false, agentDone: true, resumed: true });
  speaker.say(reply, c.remaining);
  speaker.end(reply);
  paint(reply);
}

// ---------- while you talk ----------
// A line it said while you were talking, not read out over you: on screen as
// it came; said when you stop if what you said was nothing; left on screen if
// you said something new, which it answers anew.
function holdBack(text) {
  log('held', text.slice(0, 40));
  if (reply && !reply.playing) dropReply(reply); // an answer not yet heard goes with it
  if (!heldRow) { heldRow = panel.addRow('agent'); panel.setRowNote(heldRow, 'not read out: you were talking'); }
  held.push(text);
  panel.setRowText(heldRow, held.join(' '), 'unsaid');
}
function releaseHeld(sayIt) {
  const lines = held.splice(0), row = heldRow;
  heldRow = null;
  if (!lines.length || !sayIt) return;
  row.remove();
  lines.forEach((t) => agentSaid(t));
}

// A cut-in, as people make one: a short "Sorry," first, and the rest only if
// you stop for it (mostly you are mid-word, and a whole sentence would be lost
// under yours). Talked over, it is dropped and its next message says so.
//
// Whether you stopped for it is timed from when you heard "Sorry," (a made
// clip, so at once), not from when the agent's line arrived: a person needs a
// moment to hear it and finish the word they are on. Judged talked over and
// then you stop soon after all, it is still taken.
//
// Stopping for it ends your turn there, and nothing of it is sent as a turn
// of its own: the agent would answer that, and its answer would cut off the
// cut-in you stopped to hear. What you said after it came in is dropped (you
// were finishing a word, or reacting to it, and stopped to listen); what you
// said before it came in, which the agent has not had, goes with your next
// turn. Not taken, everything you said is sent, as ever.
function offerCutIn(text) {
  if (cutIn) { cutIn.lines.push(text); return; }
  // One judged talked over a moment ago: a line now joins it (and what the
  // agent is told it said); after that window, a line is a new cut-in.
  if (cutLate && now() - cutLate.at > T.cutInLateMs) cutLate = null;
  if (cutLate) {
    cutLate.lines.push(text);
    cutNote = cutLate.lines.join(' ').slice(0, 300);
    panel.setRowText(cutLate.row, cutNote, 'unsaid');
    return;
  }
  // Where your talk was when it came in: what you say after it is dropped if
  // you stop for it.
  cutIn = { lines: [text], at: now(), heardAt: 0, endAt: 0, cue: null, utt: uttId, upTo: frames.length };
  log('cut-in offered', text.slice(0, 40));
  const r = ownReply();
  cutIn.cue = r;
  r.text = T.cutInCue; r.spoke = true; r.lastLineT = now();
  if (!speaker.cue(r, T.cutInCue)) speaker.say(r, T.cutInCue);
  paint(r);
  r.endTimer = setTimeout(() => endReply(r), T.ownReplyEndMs);
}
// The cue is heard from its first sound (replyStarted); one that never makes
// a sound counts as heard a while after it was offered.
function cueHeard(r) {
  const c = cutIn;
  if (!c || c.cue !== r || c.heardAt) return;
  c.heardAt = now();
  const left = speaker.natural ? Math.max(0, (r.playHead - speaker.ctx.currentTime) * 1000) : 0;
  c.endAt = c.heardAt + (left || T.cutInCueMs);
}
function cueTimes(t) {
  const c = cutIn;
  if (c && !c.heardAt && t - c.at > T.cutInNoSoundMs) { c.heardAt = c.at + T.cutInNoSoundMs; c.endAt = c.heardAt + T.cutInCueMs; }
  return c && c.heardAt ? c : null;
}
const cueStarted = (t) => { const c = cueTimes(t); return !!c && t - c.heardAt >= T.cutInMinHeardMs; };
const cueHeardFor = (t) => { const c = cueTimes(t); return c ? t - c.endAt : -Infinity; };

function cutInDecided(stopped) {
  const c = cutIn;
  cutIn = null;
  if (stopped) { log('cut-in taken'); takeCutIn(c); return; }
  log('cut-in not taken');
  cutNote = c.lines.join(' ').slice(0, 300);
  const row = panel.addRow('agent');
  panel.setRowText(row, cutNote, 'unsaid');
  panel.setRowNote(row, 'cut-in not taken: you kept talking');
  cutLate = { ...c, at: now(), row };
}
function cutInTakenLate() {
  const c = cutLate;
  cutLate = null; cutNote = '';
  log('cut-in taken late');
  c.row.remove();
  takeCutIn(c);
}
function takeCutIn(c) {
  stopForCutIn(c);
  c.lines.forEach((t) => agentSaid(t));
}
// The talk up to where the cut-in came in, ending at a gap between words: the
// last quiet frame in the moment before it (the text model, given a word cut
// in half, can loop on the words before it: "the tenth thing is the round
// desk in Chicago and the tenth thing is...").
function cleanCut(frs) {
  const back = Math.min(frs.length, Math.round(T.cutBackMs / FRAME_MS));
  const e = [];
  for (let i = frs.length - back; i < frs.length; i++) { let s = 0; const f = frs[i]; for (let j = 0; j < f.length; j++) s += f[j] * f[j]; e.push(s); }
  if (!e.length) return frs;
  const quiet = Math.max(Math.min(...e) * 2, Math.max(...e) * 0.1);
  let i = e.length - 1;
  while (i > 0 && e[i] > quiet) i--;
  return frs.slice(0, frs.length - back + i);
}
// Faded out and followed by a little silence, as speech that ended.
function endInSilence(a) {
  const pad = Math.round(16000 * T.cutPadMs / 1000), fade = Math.min(480, a.length);
  const o = new Float32Array(a.length + pad);
  o.set(a);
  for (let i = 0; i < fade; i++) o[a.length - 1 - i] *= i / fade;
  return o;
}
// Your turn ends where you stopped for it, without being sent: of its talk,
// only what came before the cut-in is kept, for your next turn.
function stopForCutIn(c) {
  partsSent = 0;
  if (you === 'quiet') return;
  learnYourLevel();
  const row = liveRow || panel.pendingRow();
  liveRow = null;
  // A part sent since it came in left nothing of this utterance before it.
  const upTo = c.utt === uttId ? Math.min(c.upTo, frames.length) : 0;
  const before = cleanCut(frames.slice(0, upTo));
  const voicedBefore = Math.round(before.length * FRAME_MS);
  const keep = (text) => {
    text = (text || '').trim();
    if (!hasWords(text) || voicedBefore < T.tailMinVoicedMs) { panel.setRowText(row, '(not sent: you stopped for its cut-in)', 'note'); return; }
    panel.setRowText(row, text);
    panel.setRowNote(row, 'said before it cut in; sent with your next turn. What you said after it was dropped.');
    beforeCut.push(text);
  };
  if (!before.length) keep('');
  else {
    // A final of its own, by the length of audio it covers.
    const id = uttId, key = id + ':' + -upTo;
    const f = { id, voiced: -upTo, text: null, ms: 0, waiting: [], before: '', len: 0 };
    finals.set(key, f);
    f.waiting.push(() => keep(f.text));
    stt.postMessage({ type: 'transcribe', id: 'f' + key, final: true, audio: endInSilence(joinFrames(before)) });
  }
  you = 'quiet'; frames = []; preRoll = []; uttId++; voicedMs = 0; silenceMs = 0;
  settled = null; latest = null; liveBefore = ''; checked = rechecked = finalStarted = thinking = false;
}

// ---------- the agent ----------
// Its state as the viewer reports it: working, done, needs you. A turn is
// over once it has worked and stopped (or, if working was never seen, a
// while after your words).
export function agentStateIs(state) {
  agentState = state || '';
  const r = reply;
  if (!r || r.own || r.agentDone) return;
  if (agentState === 'working') { r.sawWorking = true; clearTimeout(r.endTimer); return; }
  if (r.sawWorking || now() - r.saidAt > T.noWorkSeenMs) {
    clearTimeout(r.endTimer);
    r.endTimer = setTimeout(() => endReply(r), T.doneGraceMs);
  }
}

// The viewer went to another session: what the one left was saying stops
// (not a talk-over: nothing reported, nothing to pick up), and the
// conversation shows where the new one begins.
export function sessionSwitched(id, name) {
  held = []; heldRow = null; cutIn = null; cutNote = ''; cutLate = null; beforeCut = [];
  afterReply.length = 0; agentState = '';
  panel.setAgentName(name);
  if (reply) {
    const r = reply;
    if (ducked) duck(false);
    reply = null;
    [r.ackTimer, r.endTimer, r.pulseTimer].forEach(clearTimeout);
    speaker.cancel(r);
  }
  panel.addDivider(`Now talking to ${(name || '').trim() || id || 'another session'}`);
  if (running && you === 'quiet' && !muted) show('listening');
}

// What you said was not typed in: the session only types where words can do
// nothing but reach an agent as a message (never into a prompt, a shell, or
// a password). Said out loud, as the answer you were waiting for.
const NOT_TYPED = {
  asking: 'It is asking on screen. Answer there; I did not type that in.',
  'no-agent': 'No agent is running there, so I did not type that in.',
  hidden: 'The terminal is asking for a password, so I did not type that in.',
  unsent: 'It started asking on screen before I could send that. Your words are in its box, not sent.',
  draft: 'There is something typed in its box, so I did not add to it. Send or clear it, then speak.',
};
export function notTyped(why) {
  if (!running) return;
  const line = NOT_TYPED[why] || 'That was not typed in.';
  if (reply && reply.turn.row) panel.setRowNote(reply.turn.row, 'not typed in');
  if (reply && !reply.spoke) dropReply(reply);
  if (you !== 'quiet') { holdBack(line); return; }
  const r = ownReply();
  r.text = line; r.spoke = true; r.fromReminal = true;
  speaker.say(r, line);
  paint(r);
  endReply(r);
}

// ---------- controls ----------
// Hold: "I am not finished". Held, nothing ends your turn; let go, and it
// answers at once. Held while it talks, it stops.
export function holdDown() {
  if (!running || holding) return;
  holding = true; panel.showHolding(true);
  if (reply && agentSpeaking()) { bargeT0 = now(); panel.timeDuck(0); cutOff(now()); }
}
export function holdUp() {
  if (!holding) return;
  holding = false; panel.showHolding(false);
  if (you !== 'quiet') endTurn(now(), 'you let go');
}

// Mute: for talking to someone else in the room; it hears nothing until unmuted.
export function toggleMute() {
  if (!running) return;
  muted = !muted; hot = 0;
  panel.showMuted(muted);
  if (muted) {
    if (ducked) liftDuck();
    if (you !== 'quiet') { you = 'quiet'; frames = []; if (liveRow) { liveRow.remove(); liveRow = null; } }
    show('muted');
  } else show(agentSpeaking() ? 'speaking' : 'listening');
}
