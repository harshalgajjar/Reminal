// Kokoro-82M (Apache-2.0) run straight through transformers.js: phonemes in,
// 24 kHz audio out. Turning text into phonemes is g2p.js's job.
//
// The voice loading and generate_from_ids below follow kokoro-js 1.2.1
// (dist/kokoro.js, Apache-2.0, https://github.com/hexgrad/kokoro), adapted.
import { guardFetch } from './integrity.js';
import { loadTransformers, HF, REVISION } from './assets.js';

guardFetch(); // every file it fetches from /voice-assets/ is checked first

const MODEL = 'onnx-community/Kokoro-82M-v1.0-ONNX';
const RATE = 24000;
// The model takes at most 512 tokens, two of them the start and end marks.
const MAX = 510;

// Loaded in load(), not on import: a module that waits at its top would hold
// up the worker importing it, and the first message sent to it would be lost.
let model = null, tokenizer = null, Tensor = null;

export async function load({ device = 'wasm', dtype = 'q8', progress_callback = null } = {}) {
  const lib = await loadTransformers(MODEL);
  Tensor = lib.Tensor;
  [model, tokenizer] = await Promise.all([
    lib.StyleTextToSpeech2Model.from_pretrained(MODEL, { dtype, device, progress_callback }),
    lib.AutoTokenizer.from_pretrained(MODEL, { progress_callback }),
  ]);
}

// A voice is a table of styles, one row of 256 floats per input length.
const voices = new Map();
async function voiceData(id) {
  if (!/^[a-z]{2}_[a-z]+$/.test(id)) throw new Error('no such voice: ' + id);
  if (voices.has(id)) return voices.get(id);
  const url = `${HF}${MODEL}/${REVISION[MODEL]}/voices/${id}.bin`;
  // Checked, and kept once downloaded, by the guarded fetch (integrity.js).
  const r = await fetch(url);
  if (!r.ok) throw new Error(`voice ${id}: HTTP ${r.status}`);
  const v = new Float32Array(await r.arrayBuffer());
  voices.set(id, v);
  return v;
}

async function fromIds(ids, voice, speed) {
  const n = ids.dims.at(-1);
  const off = 256 * Math.min(Math.max(n - 2, 0), 509);
  const style = (await voiceData(voice)).slice(off, off + 256);
  const { waveform } = await model({
    input_ids: ids,
    style: new Tensor('float32', style, [1, 256]),
    speed: new Tensor('float32', [speed], [1]),
  });
  return waveform.data;
}

// Phoneme strings longer than the model takes, split at the space nearest the
// middle until every piece fits (a piece with no space is cut short).
function pieces(ph) {
  const n = tokenizer(ph).input_ids.dims.at(-1) - 2;
  if (n <= MAX) return [ph];
  const mid = ph.length >> 1;
  let at = -1;
  for (let d = 0; d < mid; d++) {
    if (ph[mid - d] === ' ') { at = mid - d; break; }
    if (ph[mid + d] === ' ') { at = mid + d; break; }
  }
  if (at <= 0) return [ph];
  return [...pieces(ph.slice(0, at).trim()), ...pieces(ph.slice(at + 1).trim())];
}

export async function generate(phonemes, { voice = 'af_heart', speed = 1 } = {}) {
  if (!model) throw new Error('kokoro: load() first');
  const parts = pieces(String(phonemes || '').trim()).filter((p) => p);
  const outs = [];
  for (const p of parts) {
    const { input_ids } = tokenizer(p, { truncation: true });
    outs.push(await fromIds(input_ids, voice, speed));
  }
  if (outs.length === 1) return { audio: outs[0], sampling_rate: RATE };
  const audio = new Float32Array(outs.reduce((s, a) => s + a.length, 0));
  let o = 0; for (const a of outs) { audio.set(a, o); o += a.length; }
  return { audio, sampling_rate: RATE };
}
