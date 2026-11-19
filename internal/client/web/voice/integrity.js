// Nothing from /voice-assets/ is used until its bytes are proven. The voice
// frame is on the viewer's own origin (so the models it downloads once stay
// in its storage), which means code running in it could reach the viewer and
// the session. So every library, runtime and model it loads is checked
// against the size and SHA-256 in hashes.js, which ships inside reminal with
// this file; whoever serves /voice-assets/ cannot change what runs.
//
//   guardFetch()        fetch, for /voice-assets/, hands on only checked bytes
//                       (passed on as they arrive, for download progress; the
//                       body ends in an error unless the whole file matches)
//   verifiedURL(url)    a blob: URL of the checked file, for what is loaded
//                       by URL rather than fetched (modules, wasm, a worklet)
//   importVerified(url) import() of a checked module
//   loadScript(url)     a checked classic script, run in the page
//
// A file not in hashes.js is answered as not found. Anything that does not
// match is refused with AssetRefused, which the panel shows in plain words.
import { PINNED } from './hashes.js';

const ASSETS = new URL('/voice-assets/', self.location.origin).href;

// The path under /voice-assets/ a URL names, or null for any other URL.
export function assetPath(url) {
  const u = new URL(String(url), self.location.href);
  u.hash = ''; u.search = '';
  return u.href.startsWith(ASSETS) ? u.href.slice(ASSETS.length) : null;
}

export class AssetRefused extends Error {
  constructor(path, why) { super(`${path}: ${why}`); this.name = 'AssetRefused'; this.path = path; }
}

const plainFetch = self.fetch.bind(self);

// The last file refused here. A refusal reaches the caller as whatever the
// library makes of a body that failed (often "Failed to fetch"), and from a
// worker only as text: describe() says what really happened.
let refused = null;
export const refusal = () => refused;
// Told of every refusal as it happens: a library may load a file late (the
// voice-activity model loads its worklet once listening starts) and carry on
// without it.
let onRefused = () => {};
export function whenRefused(f) { onRefused = f; }
export function describe(err) {
  return refused ? `refused ${refused}` : String((err && err.message) || err);
}

// The checked body of the response to one /voice-assets/ file, whose pinned
// size and hash are known. Bytes stream on for download progress, except the
// last one, held back by position (not by how the server chunks it) until
// the body has ended at exactly the pinned size and its hash matched. A body
// that runs past the size is refused at once. So a reader that wants `size`
// bytes, whatever headers or chunks the server sends, never holds the whole
// of a file that has not been proven.
function checkedBody(path, body) {
  const [want, size] = PINNED[path], hash = new Sha256(), reader = body.getReader();
  let seen = 0, last = null;
  const refuse = (out, why) => { refused = path; reader.cancel().catch(() => {}); out.error(new AssetRefused(path, why)); onRefused(path); };
  return new ReadableStream({
    async pull(out) {
      const { done, value } = await reader.read();
      if (!done) {
        seen += value.length;
        if (seen > size) return refuse(out, 'it is longer than the version reminal expects');
        hash.update(value);
        if (seen < size) { out.enqueue(value); return; }
        // This chunk ends at the pinned size: all of it but its last byte.
        if (value.length > 1) out.enqueue(value.subarray(0, value.length - 1));
        last = value.subarray(value.length - 1);
        return;
      }
      if (seen !== size || hash.hex() !== want) return refuse(out, 'its contents do not match the version reminal expects');
      out.enqueue(last);
      out.close();
    },
    cancel(why) { return reader.cancel(why); },
  });
}

// Checked files are kept, so each is downloaded once: in this Cache, shared
// by the page and its workers. A copy is stored from the checked body
// itself, so one that is refused errors the store and nothing is kept; a
// stored copy is trusted when read (it was proven against exactly its pinned
// hash, which is in its key, when stored, and only code
// already running on this origin could write it). transformers.js keeps
// its model files under the same name.
export const CHECKED_CACHE = 'reminal-voice-checked';
const openCache = () => (self.caches ? caches.open(CHECKED_CACHE).catch(() => null) : Promise.resolve(null));

// One download of a file at a time, across the page and its workers (the
// page and both workers want the same runtime at once): the first holds the
// file's lock until its copy is stored, the others then read the copy.
function oneAtATime(path, f) {
  if (!self.navigator || !navigator.locks) return f(() => {});
  return new Promise((resolve, reject) => {
    navigator.locks.request('reminal-voice:' + path, () => new Promise((release) => {
      f(release).then(resolve, (e) => { release(); reject(e); });
    })).catch(reject);
  });
}

async function fetchChecked(url, init = {}) {
  const path = assetPath(url);
  if (!PINNED[path]) return new Response('not a voice asset', { status: 404 });
  const href = new URL(String(url), self.location.href).href;
  // Kept under its pinned hash too: a copy proven against another pin (an
  // older build, a changed file) is never read as this one.
  const key = `${href}?sha256=${PINNED[path][0]}`;
  return oneAtATime(path, async (release) => {
    const cache = await openCache();
    const kept = cache && await cache.match(key).catch(() => null);
    if (kept) { release(); return kept; }
    // The whole file, always: a range could not be checked against its hash.
    // Not stopped by the caller's signal: the copy being kept still needs it.
    const res = await plainFetch(href, { method: 'GET', credentials: 'same-origin', cache: init.cache });
    if (!res.ok || !res.body) { release(); return res; }
    // Only headers that say what the checked body is, never the server's
    // length or encoding (the body here is already decoded).
    const headers = new Headers({ 'content-type': res.headers.get('content-type') || 'application/octet-stream', 'content-length': String(PINNED[path][1]) });
    const checked = new Response(checkedBody(path, res.body), { status: 200, statusText: 'OK', headers });
    if (!cache) { release(); return checked; }
    cache.put(key, checked.clone()).catch(() => {}).finally(release);
    return checked;
  });
}

export function guardFetch() {
  if (self.fetch.voiceGuarded) return;
  const guarded = (input, init = {}) => {
    const url = input instanceof Request ? input.url : String(input);
    const path = assetPath(url);
    if (path == null) return plainFetch(input, init);
    const method = (init.method || (input instanceof Request ? input.method : 'GET')).toUpperCase();
    const range = new Headers(init.headers || (input instanceof Request ? input.headers : undefined)).get('range');
    if (method === 'HEAD' || range) return Promise.resolve(probe(path, method, range));
    return fetchChecked(url, init);
  };
  guarded.voiceGuarded = true;
  self.fetch = guarded;
}

// What a library asks before it downloads a file (transformers.js sends
// "Range: bytes=0-0" to learn its size), answered from the pinned size with
// no request at all: passed on, it would start the whole file only to be
// cancelled. Any other range is refused: a part of a file cannot be checked.
function probe(path, method, range) {
  const pin = PINNED[path];
  if (!pin) return new Response(null, { status: 404 });
  if (method === 'HEAD') return new Response(null, { status: 200, headers: { 'content-length': String(pin[1]) } });
  if (range.replace(/\s/g, '') !== 'bytes=0-0') return new Response(null, { status: 416 });
  return new Response(null, { status: 206, headers: { 'content-range': `bytes 0-0/${pin[1]}` } });
}

const TYPES = { js: 'text/javascript', mjs: 'text/javascript', wasm: 'application/wasm' };
const urls = new Map();

export function verifiedURL(url) {
  const path = assetPath(url);
  if (path == null) return Promise.reject(new AssetRefused(String(url), 'not a voice asset'));
  if (!urls.has(path)) {
    urls.set(path, (async () => {
      const res = await fetchChecked(url);
      if (!res.ok) throw new AssetRefused(path, `could not be fetched (${res.status})`);
      const bytes = await res.arrayBuffer();
      return URL.createObjectURL(new Blob([bytes], { type: TYPES[path.split('.').pop()] || 'application/octet-stream' }));
    })());
    urls.get(path).catch(() => urls.delete(path));
  }
  return urls.get(path);
}

export async function importVerified(url) { return import(await verifiedURL(url)); }

export async function loadScript(url) {
  const src = await verifiedURL(url);
  await new Promise((resolve, reject) => {
    const s = document.createElement('script');
    s.src = src; s.onload = resolve; s.onerror = () => reject(new AssetRefused(assetPath(url), 'did not run'));
    document.head.appendChild(s);
  });
}

// The page's AudioWorklet modules too (the voice-activity model's worklet).
export function guardWorklets() {
  if (!self.AudioWorklet || AudioWorklet.prototype.addModule.voiceGuarded) return;
  const add = AudioWorklet.prototype.addModule;
  const guarded = async function (url, options) {
    return add.call(this, assetPath(url) == null ? url : await verifiedURL(url), options);
  };
  guarded.voiceGuarded = true;
  AudioWorklet.prototype.addModule = guarded;
}

// ---------- SHA-256 ----------
// Incremental, so a file is checked as it downloads with no second copy of
// it in memory (SubtleCrypto takes only a whole buffer, and the natural
// voice is 330 MB).
const K = new Uint32Array([
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
  0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
  0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
  0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
]);

export class Sha256 {
  constructor() {
    this.h = new Uint32Array([0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19]);
    this.w = new Uint32Array(64);
    this.tail = new Uint8Array(64);
    this.held = 0;   // bytes in tail
    this.length = 0; // bytes in all
  }

  update(data) {
    let i = 0;
    this.length += data.length;
    if (this.held) {
      i = Math.min(64 - this.held, data.length);
      this.tail.set(data.subarray(0, i), this.held);
      this.held += i;
      if (this.held < 64) return;
      this.block(this.tail, 0);
      this.held = 0;
    }
    for (; i + 64 <= data.length; i += 64) this.block(data, i);
    if (i < data.length) { this.tail.set(data.subarray(i)); this.held = data.length - i; }
  }

  hex() {
    const bits = this.length * 8, t = this.tail;
    t[this.held++] = 0x80;
    if (this.held > 56) { t.fill(0, this.held); this.block(t, 0); this.held = 0; }
    t.fill(0, this.held, 56);
    const hi = Math.floor(bits / 0x100000000), lo = bits >>> 0;
    t[56] = hi >>> 24; t[57] = hi >>> 16; t[58] = hi >>> 8; t[59] = hi;
    t[60] = lo >>> 24; t[61] = lo >>> 16; t[62] = lo >>> 8; t[63] = lo;
    this.block(t, 0);
    return Array.from(this.h, (x) => x.toString(16).padStart(8, '0')).join('');
  }

  block(b, o) {
    const w = this.w, h = this.h;
    for (let t = 0; t < 16; t++, o += 4) w[t] = (b[o] << 24) | (b[o + 1] << 16) | (b[o + 2] << 8) | b[o + 3];
    for (let t = 16; t < 64; t++) {
      const x = w[t - 15], y = w[t - 2];
      const s0 = ((x >>> 7) | (x << 25)) ^ ((x >>> 18) | (x << 14)) ^ (x >>> 3);
      const s1 = ((y >>> 17) | (y << 15)) ^ ((y >>> 19) | (y << 13)) ^ (y >>> 10);
      w[t] = (w[t - 16] + s0 + w[t - 7] + s1) | 0;
    }
    let a = h[0], c1 = h[1], c2 = h[2], d = h[3], e = h[4], f = h[5], g = h[6], k = h[7];
    for (let t = 0; t < 64; t++) {
      const S1 = ((e >>> 6) | (e << 26)) ^ ((e >>> 11) | (e << 21)) ^ ((e >>> 25) | (e << 7));
      const t1 = (k + S1 + ((e & f) ^ (~e & g)) + K[t] + w[t]) | 0;
      const S0 = ((a >>> 2) | (a << 30)) ^ ((a >>> 13) | (a << 19)) ^ ((a >>> 22) | (a << 10));
      const t2 = (S0 + ((a & c1) ^ (a & c2) ^ (c1 & c2))) | 0;
      k = g; g = f; f = e; e = (d + t1) | 0; d = c2; c2 = c1; c1 = a; a = (t1 + t2) | 0;
    }
    h[0] += a; h[1] += c1; h[2] += c2; h[3] += d; h[4] += e; h[5] += f; h[6] += g; h[7] += k;
  }
}
