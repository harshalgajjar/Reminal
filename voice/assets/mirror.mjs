// The voice panel's libraries and models, served by us: every file at a
// fixed version (an exact npm version, a Hugging Face commit), checked by its
// SHA-256 when it is mirrored, laid out under the paths /voice-assets/ serves.
//
//   node mirror.mjs pin            fetch each file once; write manifest.json
//                                  (path, source, size, sha256)
//   node mirror.mjs fetch OUT      fetch each file in manifest.json into
//                                  OUT/<path>, refusing any whose size or
//                                  hash differs (nothing of it is written)
//   node mirror.mjs check OUT      check every file in OUT against
//                                  manifest.json; run it before an upload,
//                                  which must not go ahead if it fails
//   node mirror.mjs serve OUT PORT serve OUT as /voice-assets/ (with Range),
//                                  for tests
//   node verify-bucket.mjs URL     check what URL (a /voice-assets/ base)
//                                  serves against manifest.json
//
// Run it in Docker (node:22). The upload (R2 bucket reminal-voice-assets) is
// a deploy, done by whoever deploys, from OUT, after `check OUT` passed.
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import http from 'node:http';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
export const MANIFEST = path.join(HERE, 'manifest.json');

export const readManifest = (file = MANIFEST) => JSON.parse(fs.readFileSync(file, 'utf8'));
export const totalBytes = (files) => files.reduce((a, f) => a + f.size, 0);
export const mb = (n) => (n / 1e6).toFixed(1) + ' MB';

// What differs between a manifest entry and what was found ({ size, sha256 }),
// or '' when nothing does.
export function mismatch(f, got) {
  const bad = [];
  if (got.size !== f.size) bad.push(`size ${got.size}, want ${f.size}`);
  if (got.sha256 !== f.sha256) bad.push(`sha256 ${got.sha256}, want ${f.sha256}`);
  return bad.join('; ');
}

// The size and SHA-256 of a stream of bytes, read as it comes (a model is
// hundreds of MB: never held whole).
export async function measure(chunks) {
  const h = crypto.createHash('sha256');
  let size = 0;
  for await (const c of chunks) { h.update(c); size += c.length; }
  return { size, sha256: h.digest('hex') };
}

const npm = (pkg, files) => files.map((f) => ({ path: `npm/${pkg}/${f}`, source: `https://cdn.jsdelivr.net/npm/${pkg}/${f}` }));
const hf = (repo, rev, files) => files.map((f) => ({ path: `hf/${repo}/${rev}/${f}`, source: `https://huggingface.co/${repo}/resolve/${rev}/${f}` }));

const ORT_DEV = 'onnxruntime-web@1.31.0-dev.20260914-8d85527a0';
const MOONSHINE = ['config.json', 'generation_config.json', 'preprocessor_config.json', 'tokenizer.json', 'tokenizer_config.json',
  'onnx/encoder_model_quantized.onnx', 'onnx/decoder_model_merged_quantized.onnx', // q8, the default
  'onnx/encoder_model.onnx', 'onnx/decoder_model_merged_q4.onnx']; // with the GPU
const VOICES = ['af_heart', 'af_bella', 'af_nicole', 'am_michael', 'am_fenrir', 'bf_emma', 'bm_george'];

export const FILES = [
  ...npm('@huggingface/transformers@4.3.2', ['dist/transformers.min.js', 'dist/ort-wasm-simd-threaded.jsep.mjs']),
  ...npm(ORT_DEV, ['dist/ort-wasm-simd-threaded.mjs', 'dist/ort-wasm-simd-threaded.wasm',
    'dist/ort-wasm-simd-threaded.asyncify.mjs', 'dist/ort-wasm-simd-threaded.asyncify.wasm',
    'dist/ort-wasm-simd-threaded.jsep.mjs', 'dist/ort-wasm-simd-threaded.jsep.wasm']),
  ...npm('onnxruntime-web@1.22.0', ['dist/ort.wasm.min.js', 'dist/ort-wasm-simd-threaded.mjs', 'dist/ort-wasm-simd-threaded.wasm']),
  ...npm('@ricky0123/vad-web@0.0.31', ['dist/bundle.min.js', 'dist/silero_vad_v5.onnx', 'dist/vad.worklet.bundle.min.js']),
  ...hf('onnx-community/Kokoro-82M-v1.0-ONNX', '1939ad2a8e416c0acfeecc08a694d14ef25f2231', [
    'config.json', 'tokenizer.json', 'tokenizer_config.json',
    'onnx/model_quantized.onnx', // q8, on every device (the fp32 one is over the asset store's 300 MiB limit)
    ...VOICES.map((v) => `voices/${v}.bin`)]),
  ...hf('onnx-community/moonshine-base-ONNX', 'b1e9b6aae3c3c7298f10c3798393fdf38e8fbbad', MOONSHINE),
  ...hf('onnx-community/moonshine-tiny-ONNX', 'a6da1241cd305dcd64eab1edbd615f2bb9aabb95', MOONSHINE),
  ...hf('pipecat-ai/smart-turn-v3', 'f766f81d3cfdf7737ac64aad813d91bbfd56bf93', ['smart-turn-v3.2-cpu.onnx']),
];

async function get(url) {
  const r = await fetch(url, { redirect: 'follow' });
  if (!r.ok) throw new Error(`${r.status} ${url}`);
  return Buffer.from(await r.arrayBuffer());
}
const sha256 = (b) => crypto.createHash('sha256').update(b).digest('hex');

async function pin() {
  const out = [];
  for (const f of FILES) {
    const b = await get(f.source);
    out.push({ ...f, size: b.length, sha256: sha256(b) });
    console.log(String(b.length).padStart(10), f.path);
  }
  fs.writeFileSync(MANIFEST, JSON.stringify(out, null, 1) + '\n');
  console.log(out.length, 'files,', (out.reduce((a, f) => a + f.size, 0) / 1e6).toFixed(0), 'MB');
}

// The size and SHA-256 of a file on disk, or null when there is none.
const measureFile = (file) => (fs.existsSync(file) && fs.statSync(file).isFile() ? measure(fs.createReadStream(file)) : null);

// fetch: a file whose size or hash is not the pinned one is refused, and
// nothing of it is written.
async function fetchAll(dir) {
  const files = readManifest();
  for (const f of files) {
    const dest = path.join(dir, f.path);
    const have = await measureFile(dest);
    if (have && !mismatch(f, have)) continue;
    const b = await get(f.source);
    const bad = mismatch(f, { size: b.length, sha256: sha256(b) });
    if (bad) throw new Error(`refused ${f.path}: ${bad}`);
    fs.mkdirSync(path.dirname(dest), { recursive: true });
    fs.writeFileSync(dest, b);
    console.log('ok', f.path);
  }
  console.log(files.length, 'objects,', totalBytes(files), 'bytes (' + mb(totalBytes(files)) + ') verified in', dir);
}

// check: every file in dir is the pinned one; the upload goes ahead only
// when this exits 0.
async function checkAll(dir) {
  const files = readManifest();
  let bad = 0;
  for (const f of files) {
    const have = await measureFile(path.join(dir, f.path));
    const why = have ? mismatch(f, have) : 'missing';
    if (why) { bad++; console.error('BAD', f.path + ':', why); }
  }
  console.log(files.length, 'objects,', totalBytes(files), 'bytes (' + mb(totalBytes(files)) + '),', bad, 'bad, in', dir);
  if (bad) process.exit(1);
}

const TYPES = { '.wasm': 'application/wasm', '.js': 'text/javascript', '.mjs': 'text/javascript', '.json': 'application/json' };
function serve(dir, port) {
  http.createServer((req, res) => {
    const u = decodeURIComponent(new URL(req.url, 'http://x').pathname);
    if (!u.startsWith('/voice-assets/') || u.includes('..')) { res.writeHead(404).end(); return; }
    const file = path.join(dir, u.slice('/voice-assets/'.length));
    if (!fs.existsSync(file) || !fs.statSync(file).isFile()) { res.writeHead(404).end(); return; }
    const size = fs.statSync(file).size, type = TYPES[path.extname(file)] || 'application/octet-stream';
    const head = { 'Content-Type': type, 'Accept-Ranges': 'bytes', 'Access-Control-Allow-Origin': '*', 'Cache-Control': 'public, max-age=31536000, immutable' };
    const m = /bytes=(\d*)-(\d*)/.exec(req.headers.range || '');
    if (m) {
      const start = m[1] ? +m[1] : size - +m[2], end = m[1] && m[2] ? +m[2] : size - 1;
      res.writeHead(206, { ...head, 'Content-Range': `bytes ${start}-${end}/${size}`, 'Content-Length': end - start + 1 });
      if (req.method === 'HEAD') return res.end();
      fs.createReadStream(file, { start, end }).pipe(res);
      return;
    }
    res.writeHead(200, { ...head, 'Content-Length': size });
    if (req.method === 'HEAD') return res.end();
    fs.createReadStream(file).pipe(res);
  }).listen(+port, () => console.log('serving', dir, 'on', port));
}

// Run as a command, not when imported (verify-bucket.mjs uses its helpers).
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const [cmd, a, b] = process.argv.slice(2);
  try {
    if (cmd === 'pin') await pin();
    else if (cmd === 'fetch') await fetchAll(a);
    else if (cmd === 'check') await checkAll(a);
    else if (cmd === 'serve') serve(a, b);
    else { console.error('usage: mirror.mjs pin | fetch OUT | check OUT | serve OUT PORT'); process.exit(2); }
  } catch (e) {
    console.error(String(e && e.message || e));
    process.exit(1);
  }
}
