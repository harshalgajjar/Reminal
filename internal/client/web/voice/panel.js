// The panel: everything shown in voice.html, and nothing else. The
// conversation calls these; none of them decides anything.

export const $ = (id) => document.getElementById(id);

const MOBILE = (navigator.userAgentData && navigator.userAgentData.mobile) || /Android|iPhone|iPad|Mobile/i.test(navigator.userAgent);
export const isPhone = MOBILE;

// ---------- status ----------
// cls is one of '', listen, hear, think, speak, duck; it colours the status
// and the viewer's mic button.
let onStatus = () => {};
export function onStatusChange(f) { onStatus = f; }
export function status(text, cls = '') {
  $('state').textContent = text;
  $('hero').dataset.s = cls;
  onStatus(text, cls);
}
export const statusClass = () => $('hero').dataset.s || '';

export function loadingNote(text) { $('load').textContent = text; }

// ---------- controls ----------
const RUN_LOCKED = ['stt', 'voice', 'gpu', 'guard']; // settings that apply from the next Start

export function startButton({ label, stop = false, disabled = false }) {
  $('go').textContent = label;
  $('go').className = stop ? 'stop' : '';
  $('go').disabled = disabled;
}

export function showRunning(on) {
  $('mute').hidden = $('hold').hidden = !on;
  $('sethint').hidden = !on;
  $('hint').textContent = on
    ? 'Talk over it to stop it. Hold (Space) while you think; Mute (M) to talk to someone else.'
    : 'Hearing and the voice run on this device; only text is sent.';
  RUN_LOCKED.forEach((id) => { $(id).disabled = on; });
  if (!on) { showMuted(false); showHolding(false); }
}
export function lockSettings(on) { RUN_LOCKED.forEach((id) => { $(id).disabled = on; }); }

export function showMuted(on) {
  $('mute').querySelector('span').textContent = on ? 'Unmute' : 'Mute';
  $('mute').classList.toggle('on', on);
}
export function showHolding(on) {
  $('hold').querySelector('span').textContent = on ? 'Holding' : 'Hold';
  $('hold').classList.toggle('stop', on);
}

// ---------- settings ----------
export function settings() {
  return {
    stt: $('stt').value, voice: $('voice').value, who: $('who').value, pace: +$('pace').value,
    gpu: $('gpu').checked, guard: $('guard').checked, nods: $('nod').checked,
  };
}

// Whether Auto picks the natural voice here: a phone makes it at a fraction
// of real time, so Auto uses the device's own voice there.
export function naturalVoiceChosen() {
  const s = settings();
  return s.voice === 'kokoro' || (s.voice === 'auto' && !MOBILE);
}

// What a first start downloads, in MB, from the mirror's manifest: the
// runtimes, voice activity and turn models (50), the hearing model's files
// (4, plus its weights: 8-bit on the CPU, larger with the GPU), and the
// natural voice (93) when it is used. Rounded to the nearest 10.
const MB = { shared: 54, hearing: { base: [63, 154], tiny: [28, 76] }, natural: 93 };
function sizeNote() {
  const s = settings(), w = MB.hearing[s.stt.includes('base') ? 'base' : 'tiny'][s.gpu ? 1 : 0];
  const mb = Math.round((MB.shared + w + (naturalVoiceChosen() ? MB.natural : 0)) / 10) * 10;
  loadingNote(`First start downloads about ${mb} MB (once; after that it starts in seconds).`);
}

// The GPU by default on a computer that has one (hearing is several times
// faster); not on a phone, where it is a far larger first download.
export async function initSettings(isRunning) {
  try { if (!MOBILE && 'gpu' in navigator && await navigator.gpu.requestAdapter()) $('gpu').checked = true; } catch (_) {}
  ['gpu', 'voice', 'stt'].forEach((id) => $(id).addEventListener('change', () => { if (!isRunning()) sizeNote(); }));
  sizeNote();
  const KEY = 'reminal.voice.showTimings';
  const showTimings = (on) => { $('timings').hidden = !on; try { localStorage.setItem(KEY, on ? '1' : ''); } catch (_) {} };
  try { $('showtimes').checked = localStorage.getItem(KEY) === '1'; } catch (_) {}
  showTimings($('showtimes').checked);
  $('showtimes').addEventListener('change', () => showTimings($('showtimes').checked));
}

// ---------- the conversation ----------
let agentName = 'Agent';
export function setAgentName(name) { if (name && name.trim()) agentName = name.trim().slice(0, 60); }

function log() {
  const l = $('log');
  const empty = l.querySelector(':scope > .note');
  if (empty) empty.remove();
  return l;
}

// A turn's row: who, what was said, and a line about it.
export function addRow(kind) {
  const d = document.createElement('div');
  d.className = 'turn ' + kind;
  d.innerHTML = '<div class="who"></div><div class="txt"></div><div class="t"></div>';
  d.querySelector('.who').textContent = kind === 'you' ? 'You' : agentName; // a session's name is text, never markup
  log().appendChild(d);
  return d;
}
// A row for your words before they are in, so rows keep the order things
// were said even when a later turn's words come back first.
export function pendingRow() { const r = addRow('you'); setRowText(r, '(hearing)', 'partial'); return r; }
export function setRowText(row, text, style = '') { const x = row.querySelector('.txt'); x.textContent = text; x.className = 'txt' + (style ? ' ' + style : ''); }
export function setRowNote(row, text) { row.querySelector('.t').textContent = text; }
// The agent's words, with what has been heard so far set apart.
export function setRowHeard(row, heard, rest) {
  const x = row.querySelector('.txt');
  x.className = 'txt';
  x.innerHTML = '<span class="said"></span><span class="unsaid"></span>';
  x.querySelector('.said').textContent = heard;
  x.querySelector('.unsaid').textContent = rest;
}
export function setRowCut(row, heard) {
  const x = row.querySelector('.txt');
  x.className = 'txt';
  x.innerHTML = '<span class="said"></span> <span class="cut">(you cut it off here)</span>';
  x.querySelector('.said').textContent = heard || '(nothing yet)';
}
export function addDivider(text) {
  const d = document.createElement('div');
  d.className = 'turn switch';
  d.textContent = text;
  log().appendChild(d);
}

// The conversation follows its newest words (a reply fills in after its row
// is added), unless you have scrolled up to read.
{
  const l = $('log');
  let follow = true;
  l.addEventListener('scroll', () => { follow = l.scrollHeight - l.scrollTop - l.clientHeight < 40; });
  new MutationObserver(() => { if (follow) l.scrollTop = l.scrollHeight; }).observe(l, { childList: true, subtree: true, characterData: true });
}

// ---------- timings (Settings > Show timings) ----------
export const stats = { reply: [], decide: [], firstWords: [], live: [], duck: [], stop: [] };
const ms = (v) => (v == null ? '-' : Math.round(v) + ' ms');
const avg = (a) => (a.length ? Math.round(a.reduce((x, y) => x + y, 0) / a.length) : null);

function averages() {
  const parts = [];
  if (stats.reply.length) parts.push(`reply ${avg(stats.reply)} ms over ${stats.reply.length} ${stats.reply.length === 1 ? 'turn' : 'turns'}`);
  if (stats.duck.length) parts.push(`goes quiet ${avg(stats.duck)} ms`);
  if (stats.stop.length) parts.push(`stops ${avg(stats.stop)} ms`);
  if (stats.live.length) parts.push(`live text ${avg(stats.live)} ms`);
  $('avg').textContent = parts.length ? 'Averages: ' + parts.join(', ') : '';
}
export function timeReply({ reply, decide, firstWords, why, counted }) {
  if (counted) { stats.reply.push(reply); stats.decide.push(decide); stats.firstWords.push(firstWords); }
  $('m-reply').textContent = ms(reply);
  $('m-turn').textContent = `${ms(decide)} (${why})`;
  $('m-tts').textContent = ms(firstWords);
  averages();
}
export function timeLiveText(took, secs) { stats.live.push(took); $('m-live').textContent = `${ms(took)} for ${secs.toFixed(1)} s of speech`; }
export function timeDuck(took) { stats.duck.push(took); $('m-barge').textContent = `${ms(took)} / -`; }
export function timeStop(took) {
  stats.stop.push(took);
  $('m-barge').textContent = `${ms(stats.duck[stats.duck.length - 1])} / ${ms(took)}`;
  averages();
}
export function timeModels(text) { $('m-dev').textContent = text; }
export const msText = ms;
