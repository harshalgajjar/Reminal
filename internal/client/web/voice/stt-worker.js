// Speech to text in its own thread: Moonshine through transformers.js.
// Moonshine's encoder costs grow with the audio's length (no 30 s window to
// fill, unlike Whisper), so the utterance so far can be transcribed again
// every few hundred ms while the person is still talking: that is the live text.
import { guardFetch, describe } from './integrity.js';
import { loadTransformers } from './assets.js';

guardFetch(); // every file it fetches from /voice-assets/ is checked first
let pipeline = null;

let asr = null;

async function load({ model, gpu }) {
  const progress = (p) => {
    if (p.status === 'progress' && p.total) postMessage({ type: 'progress', file: p.file, loaded: p.loaded, total: p.total });
  };
  let device = 'wasm';
  if (gpu && 'gpu' in navigator) {
    try { if (await navigator.gpu.requestAdapter()) device = 'webgpu'; } catch (_) {}
  }
  ({ pipeline } = await loadTransformers(model)); // this model, at its commit, from our mirror
  const t0 = performance.now();
  try {
    asr = await pipeline('automatic-speech-recognition', model, {
      device,
      dtype: device === 'webgpu' ? { encoder_model: 'fp32', decoder_model_merged: 'q4' } : 'q8',
      progress_callback: progress,
    });
  } catch (e) {
    if (device !== 'webgpu') throw e;
    device = 'wasm';
    asr = await pipeline('automatic-speech-recognition', model, { device, dtype: 'q8', progress_callback: progress });
  }
  // One short run so the first real one is not also the warm-up.
  await asr(new Float32Array(16000));
  postMessage({ type: 'ready', device, ms: Math.round(performance.now() - t0) });
}

onmessage = async (e) => {
  const m = e.data;
  try {
    if (m.type === 'load') return await load(m);
    if (m.type === 'transcribe') {
      const t0 = performance.now();
      // About 6 tokens a second of speech is plenty; a cap keeps a stuck
      // decoder from repeating itself for long.
      const secs = m.audio.length / 16000;
      // The model fails on a sliver of sound (a few frames: the start of the
      // talk that follows a part sent while someone talks on), and stays in a
      // bad way after: anything under half a second is padded with silence.
      let audio = m.audio;
      if (audio.length < 8000) { const a = new Float32Array(8000); a.set(audio); audio = a; }
      const out = await asr(audio, { max_new_tokens: Math.max(8, Math.ceil(secs * 7)) });
      postMessage({ type: 'text', id: m.id, voiced: m.voiced, final: m.final, text: (out.text || '').trim(), ms: Math.round(performance.now() - t0), secs });
    }
  } catch (err) {
    postMessage({ type: 'error', id: m.id, message: describe(err) });
  }
};
