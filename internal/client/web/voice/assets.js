// Where the voice panel's libraries and models come from: this origin's
// /voice-assets/, at the fixed versions mirrored there (an exact npm version,
// a Hugging Face commit). Each file is checked against its SHA-256 before it
// is used (integrity.js, hashes.js). Change a version here, in the mirror's
// list and in hashes.js together.
import { verifiedURL, importVerified, CHECKED_CACHE } from './integrity.js';

const BASE = new URL('/voice-assets/', self.location.origin).href;

const TRANSFORMERS = BASE + 'npm/@huggingface/transformers@4.3.2/dist/transformers.min.js';
// The ONNX runtime transformers.js 4.3.2 runs on, and the one the
// voice-activity model and the turn model use in the page.
const ORT_TRANSFORMERS = BASE + 'npm/onnxruntime-web@1.31.0-dev.20260914-8d85527a0/dist/';
const ORT_PAGE = BASE + 'npm/onnxruntime-web@1.22.0/dist/';
export const ORT_PAGE_SCRIPT = ORT_PAGE + 'ort.wasm.min.js';
export const VAD = BASE + 'npm/@ricky0123/vad-web@0.0.31/dist/';
export const VAD_SCRIPT = VAD + 'bundle.min.js';
export const SMART_TURN = BASE + 'hf/pipecat-ai/smart-turn-v3/f766f81d3cfdf7737ac64aad813d91bbfd56bf93/smart-turn-v3.2-cpu.onnx';

// Models transformers.js loads, each at its commit.
export const HF = BASE + 'hf/';
export const REVISION = {
  'onnx-community/Kokoro-82M-v1.0-ONNX': '1939ad2a8e416c0acfeecc08a694d14ef25f2231',
  'onnx-community/moonshine-base-ONNX': 'b1e9b6aae3c3c7298f10c3798393fdf38e8fbbad',
  'onnx-community/moonshine-tiny-ONNX': 'a6da1241cd305dcd64eab1edbd615f2bb9aabb95',
};

// The runtime's own module and WebAssembly, as checked blob: URLs: it loads
// them by URL, which the checked fetch alone would not cover.
const runtime = (dir) => async () => ({
  mjs: await verifiedURL(dir + 'ort-wasm-simd-threaded' + (dir === ORT_TRANSFORMERS ? '.asyncify' : '') + '.mjs'),
  wasm: await verifiedURL(dir + 'ort-wasm-simd-threaded' + (dir === ORT_TRANSFORMERS ? '.asyncify' : '') + '.wasm'),
});
export const pageRuntime = runtime(ORT_PAGE);

// transformers.js, checked, set to fetch models and its runtime from here
// only. Importing it points its runtime at jsDelivr and may start loading it
// at once, so the checked runtime is made ready first and set in the same
// moment the import returns. A worker that loads a model names it: its
// commit goes into the path itself, as transformers.js does not pass a
// revision on for every file it fetches (it asks for "main", which the
// mirror does not have).
export async function loadTransformers(model) {
  const wasmPaths = await runtime(ORT_TRANSFORMERS)();
  const lib = await importVerified(TRANSFORMERS);
  const env = lib.env;
  env.backends.onnx.wasm.wasmPaths = wasmPaths;
  env.allowLocalModels = false;
  env.cacheKey = CHECKED_CACHE; // only copies checked as they downloaded
  env.remoteHost = HF;
  env.remotePathTemplate = model ? `{model}/${REVISION[model]}/` : '{model}/{revision}/';
  return lib;
}
