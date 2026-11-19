// Voice for a reminal session: talk to the agent, and hear it answer.
// Everything runs in this page, on the person's device; only text leaves it,
// to the viewer that framed it (bridge.js) and on to the session.
//
//   voice.js         this file: loads the models, starts and stops, wires the
//                    panel and the viewer's messages to the conversation
//   conversation.js  turn-taking: when you have finished, replies, talking over
//   speaker.js       the agent's voice (Kokoro or the device's own)
//   stt-worker.js    your words (Moonshine), in a worker
//   tts-worker.js    Kokoro's audio, in a worker (g2p.js: its phonemes)
//   turn-model.js    whether you have finished, from how you sounded
//   level.js         loudness, for the viewer's mic button
//   panel.js         what is shown; tuning.js: every timing and threshold
//
// The voice-activity model (Silero, through vad-web) and the ONNX runtime it
// uses run in the page; every file comes from /voice-assets/, checked first.
import { ORT_PAGE_SCRIPT, VAD, VAD_SCRIPT, pageRuntime } from './assets.js';
import { guardFetch, guardWorklets, loadScript, describe, refusal, whenRefused } from './integrity.js';
import { T } from './tuning.js';
import { Speakable } from './speakable.js';
import { Speaker } from './speaker.js';
import { loadTurnModel } from './turn-model.js';
import { startLevels, stopLevels, recentLevels } from './level.js';
import { send, onMessage } from './bridge.js';
import * as panel from './panel.js';
import * as talk from './conversation.js';

const { $ } = panel;
const log = (...a) => console.log('[voice]', ...a);

let ctx, mic, stt, tts, speaker, wakeLock, starting = false;

// Before anything is loaded: every file from /voice-assets/ is checked
// against reminal's own list of hashes (integrity.js).
guardFetch();
guardWorklets();
whenRefused(() => { if (talk.running) refusedWhileOn(); });

// What voice needs from the browser. Without it, it says so and Start stays
// off, rather than half-working.
function missing() {
  if (typeof WebAssembly !== 'object') return 'WebAssembly';
  if (!window.AudioWorklet || !window.AudioContext) return 'AudioWorklet';
  if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) return 'microphone access';
  let module = false;
  try { new Worker(URL.createObjectURL(new Blob([''], { type: 'text/javascript' })), { get type() { module = true; return 'module'; } }).terminate(); } catch (_) {}
  return module ? '' : 'module workers';
}

// The page's own scripts from /voice-assets/: the ONNX runtime the
// voice-activity model and the turn model run on, then the voice-activity
// model's library (which expects the runtime as the global `ort`).
async function loadPageScripts() {
  if (!window.ort) await loadScript(ORT_PAGE_SCRIPT);
  if (!window.vad) await loadScript(VAD_SCRIPT);
}

// What went wrong, in words for the person.
function plainError(e) {
  const why = describe(e);
  const bad = /^refused (.+)$/.exec(why);
  if (bad) return `A voice file did not match the version this reminal expects, so it was not used (${bad[1]}).`;
  if (/Failed to fetch|NetworkError|Load failed|404|502/.test(why)) return 'Voice could not download its models. Its relay gets them from live.reminal.app (or the address in REMINAL_VOICE_ASSETS) and could not reach it.';
  if (/Permission|NotAllowed/i.test(why)) return 'The microphone was not allowed. Allow it for this site, then Start again.';
  return why;
}

// The viewer's mic button shows the same state as the panel.
panel.onStatusChange((text, cls) => send('state', { text, cls, running: talk.running, muted: talk.muted }));

// ---------- loading ----------
const downloads = {};
function progress(who, p) {
  if (!starting) return; // late news from a start that has failed
  downloads[who + p.file] = [p.loaded, p.total];
  let got = 0, all = 0;
  for (const k in downloads) { got += downloads[k][0]; all += downloads[k][1]; }
  panel.loadingNote(got >= all ? 'Downloaded. Getting hearing and voice ready (a few seconds).' : `Downloading models, once: ${(got / 1e6).toFixed(0)} of ${(all / 1e6).toFixed(0)} MB`);
}

// A worker that answers 'ready' once loaded; its other messages go to onMsg.
function loadWorker(file, load, onMsg) {
  return new Promise((resolve, reject) => {
    const w = new Worker(file, { type: 'module' });
    let ready = false;
    w.onerror = (e) => reject(new Error(file + ' failed: ' + (e.message || 'error')));
    w.onmessage = ({ data: m }) => {
      if (m.type === 'progress') progress(file, m);
      else if (m.type === 'ready') { ready = true; resolve({ w, m }); }
      else if (m.type === 'error' && !ready) reject(new Error(m.message));
      else onMsg(m);
    };
    w.postMessage({ type: 'load', ...load });
  });
}

// Your words, the natural voice if it is used, and the turn model, at once.
// The natural voice slower than 1.5x real time cannot keep ahead of itself
// (every answer starts late): Auto uses the device's voice then.
async function loadModels(s) {
  let natural = panel.naturalVoiceChosen();
  const notes = [];
  const hearing = loadWorker('stt-worker.js', { model: s.stt, gpu: s.gpu }, (m) => talk.onWords(m)).then(({ w, m }) => {
    stt = w; notes.push(`hearing ${m.device} (${(m.ms / 1000).toFixed(1)} s)`);
  });
  const voice = !natural ? Promise.resolve(notes.push('voice built-in')) : loadWorker('tts-worker.js', { v: s.who }, (m) => speaker.onWorker(m)).then(({ w, m }) => {
    let note = `Kokoro ${m.device}${m.device === 'wasm' ? (m.isolated ? ' multi-thread' : ' one thread') : ''}, ${m.speed.toFixed(1)}x real time (${(m.ms / 1000).toFixed(1)} s)`;
    if (m.speed < 1.5 && s.voice === 'auto') { note += ': too slow here, so the built-in voice is used'; natural = false; w.terminate(); }
    else { tts = w; if (m.speed < 1.5) note += ' (slow here: the built-in voice will feel quicker)'; }
    notes.push(note);
    return m.speed;
  });
  const [, speed, turnModel] = await Promise.all([hearing, voice, loadTurnModel()]);
  turnModel.slow = turnModel.ms > T.slowTurnModelMs;
  notes.push(`turn model ${Math.round(turnModel.ms)} ms${turnModel.slow ? ' (slow here: pauses end turns)' : ''}`);
  panel.timeModels(notes.join(', '));
  panel.loadingNote('');
  return { natural, speed: natural ? speed : 0, turnModel };
}

// ---------- start / stop ----------
async function start() {
  if (talk.running || starting || lacking) return;
  starting = true;
  const s = panel.settings();
  panel.startButton({ label: 'Start', disabled: true });
  panel.lockSettings(true);
  try {
    ctx = new AudioContext();
    // Unlocks the device's voice while this is still a tap (Auto may fall back to it).
    if (s.voice !== 'kokoro') speechSynthesis.speak(new SpeechSynthesisUtterance(' '));
    panel.status('Loading');
    const t0 = performance.now();
    await loadPageScripts();
    const { natural, speed, turnModel } = await loadModels(s);
    speaker = new Speaker({ started: talk.replyStarted, heard: talk.replyHeard, finished: talk.replyFinished });
    await speaker.open({ ctx, natural, tts, speed, guard: s.guard, pace: s.pace });
    const runtime = await pageRuntime();
    mic = await vad.MicVAD.new({
      model: 'v5', baseAssetPath: VAD, startOnLoad: false,
      ortConfig: (o) => { o.env.wasm.wasmPaths = runtime; }, // its runtime, checked (it sets its own first)
      onFrameProcessed: talk.onFrame,
      getStream: async () => {
        const stream = await navigator.mediaDevices.getUserMedia({ audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: true } });
        // A phone call or another app can take the mic away: say so, never
        // sit there looking like it is listening.
        stream.getAudioTracks().forEach((t) => { t.onended = () => { if (talk.running) panel.status('The microphone was taken away (a call?). Tap Stop, then Start.'); }; });
        return stream;
      },
    });
    // A library may carry on without a file it was refused: any refusal, now or
    // later (whenRefused), stops voice, plainly.
    if (refusal()) throw new Error('refused ' + refusal());
    mic.start();
    try { wakeLock = await navigator.wakeLock.request('screen'); } catch (_) {}
    talk.start({ stt, speaker, turnModel, nods: () => panel.settings().nods });
    panel.startButton({ label: 'Stop', stop: true });
    panel.showRunning(true);
    startLevels({ agentLevel: talk.agentLevel, muted: () => talk.muted });
    if (refusal()) refusedWhileOn(); // refused while it was starting
    log('started', Math.round(performance.now() - t0) + ' ms', natural ? 'kokoro' : 'built-in');
  } catch (e) {
    starting = false;
    log('could not start', String((e && e.message) || e));
    shutDown();
    panel.status('Could not start');
    panel.loadingNote(plainError(e));
  } finally {
    starting = false;
  }
}

// A file refused once voice was on: it stops, and says which.
function refusedWhileOn() {
  stop();
  panel.status('Could not start');
  panel.loadingNote(plainError(new Error('refused ' + refusal())));
}

function stop() {
  talk.stop();
  shutDown();
  panel.status('Stopped');
}

function shutDown() {
  // Its destroy may be async and refuse when stopped as it was starting.
  try { const r = mic && mic.destroy(); if (r && r.catch) r.catch(() => {}); } catch (_) {}
  try { wakeLock && wakeLock.release(); } catch (_) {}
  if (speaker) speaker.close();
  [stt, tts].forEach((w) => w && w.terminate());
  try { ctx && ctx.close(); } catch (_) {}
  mic = stt = tts = speaker = wakeLock = null;
  stopLevels();
  panel.startButton({ label: 'Start' });
  panel.showRunning(false);
  send('listening', { on: false });
}

// Back from another app or a locked screen: the screen-awake lock was let go
// and the audio may have been suspended.
document.addEventListener('visibilitychange', async () => {
  if (document.visibilityState !== 'visible' || !talk.running) return;
  try { if (ctx.state !== 'running') await ctx.resume(); } catch (_) {}
  try { wakeLock = await navigator.wakeLock.request('screen'); } catch (_) {}
});

// Voice is on while this runs: the session is told every few seconds, so the
// agent's speak works only while someone listens.
setInterval(() => { if (talk.running) send('listening', { on: true }); }, 3000);

// ---------- the panel's controls ----------
const typing = (e) => e.target.tagName === 'SELECT' || e.target.tagName === 'INPUT';
$('go').addEventListener('click', () => (talk.running ? stop() : start()));
$('close').addEventListener('click', () => send('close')); // voice stays on until Stop
$('hold').addEventListener('pointerdown', (e) => { e.preventDefault(); talk.holdDown(); });
['pointerup', 'pointercancel', 'pointerleave'].forEach((ev) => $('hold').addEventListener(ev, talk.holdUp));
$('mute').addEventListener('click', talk.toggleMute);
document.addEventListener('keydown', (e) => {
  if (e.repeat || !talk.running || typing(e)) return;
  if (e.code === 'Space') { e.preventDefault(); talk.holdDown(); }
  else if (e.code === 'KeyM') talk.toggleMute();
});
document.addEventListener('keyup', (e) => { if (e.code === 'Space') talk.holdUp(); });
$('pace').addEventListener('change', () => speaker && speaker.setPace(+$('pace').value));
$('who').addEventListener('change', () => speaker && speaker.setVoice($('who').value));
const lacking = missing();
panel.initSettings(() => talk.running || starting).then(() => {
  if (!lacking) return;
  panel.startButton({ label: 'Start', disabled: true });
  panel.status('Voice does not work in this browser yet');
  panel.loadingNote(`It needs ${lacking}, which this browser does not have.`);
});

// ---------- the viewer ----------
onMessage((m) => {
  switch (m.t) {
    case 'speak': if (typeof m.text === 'string') talk.agentSaid(m.text); break;
    case 'notice': talk.notTyped(String(m.why || '')); break;
    case 'agent': talk.agentStateIs(String(m.state || '')); break;
    case 'session': talk.sessionSwitched(String(m.id || ''), String(m.name || '')); break;
    case 'name': if (typeof m.name === 'string') panel.setAgentName(m.name); break;
    case 'stop': if (talk.running) stop(); break;
    // From the mic button's swipe-up dock: as the panel's own buttons.
    case 'cmd':
      if (m.c === 'start') start();
      else if (m.c === 'stop' && talk.running) stop();
      else if (m.c === 'mute') talk.toggleMute();
      else if (m.c === 'hold') (m.on ? talk.holdDown : talk.holdUp)();
      break;
  }
});
send('ready');

// For the end-to-end tests.
window.__voice = { stats: panel.stats, T, Speakable, recentLevels, get parts() { return talk.partsSent; } };
