// Whether a person has finished speaking, from the sound of the end of what
// they said (falling pitch, a finished phrase), not from a fixed silence:
// smart-turn v3.2 on the ONNX runtime the page loads (the global `ort`, see
// voice.js), its features made by transformers.js.
import { loadTransformers, SMART_TURN, pageRuntime } from './assets.js';

const SAMPLES = 128000; // 8 s at 16 kHz, the window it was trained on

export async function loadTurnModel() {
  const { WhisperFeatureExtractor } = await loadTransformers();
  ort.env.wasm.wasmPaths = await pageRuntime();
  const session = await ort.InferenceSession.create(SMART_TURN, { executionProviders: ['wasm'] });
  const features = new WhisperFeatureExtractor({ feature_size: 80, sampling_rate: 16000, hop_length: 160, n_fft: 400, chunk_length: 8, n_samples: SAMPLES, nb_max_frames: 800, padding_value: 0 });

  // The probability that the speech in audio (16 kHz) is a finished turn.
  async function finished(audio) {
    const x = new Float32Array(SAMPLES);
    const a = audio.length > SAMPLES ? audio.subarray(audio.length - SAMPLES) : audio;
    x.set(a, SAMPLES - a.length); // padded at the start, as it was trained
    let mean = 0; for (let i = 0; i < SAMPLES; i++) mean += x[i]; mean /= SAMPLES;
    let v = 0; for (let i = 0; i < SAMPLES; i++) v += (x[i] - mean) ** 2; v /= SAMPLES;
    const s = 1 / Math.sqrt(v + 1e-7); for (let i = 0; i < SAMPLES; i++) x[i] = (x[i] - mean) * s;
    const { input_features } = await features(x);
    const r = await session.run({ input_features: new ort.Tensor('float32', input_features.data, input_features.dims) });
    return r[Object.keys(r)[0]].data[0];
  }

  await finished(new Float32Array(16000)); // warm-up
  const t0 = performance.now();
  await finished(new Float32Array(32000));
  return { finished, ms: performance.now() - t0 };
}
