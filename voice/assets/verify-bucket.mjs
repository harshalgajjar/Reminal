// Check what a /voice-assets/ base serves against manifest.json: every file
// fetched whole, its size and SHA-256 compared with the pinned ones.
//
//   node verify-bucket.mjs URL [--manifest FILE]
//     URL: https://live.reminal.app/voice-assets/, the bucket's own public
//     endpoint, or a local `mirror.mjs serve` (http://127.0.0.1:8099/voice-assets/)
//
// One line for each file that is wrong or could not be fetched, then a count
// of what was checked; exit 1 when anything is wrong. A redirect counts as
// wrong: a file is served where its path says. Run it in Docker (node:22),
// after an upload and before telling anyone the voice can load from there.
import { readManifest, mismatch, measure, totalBytes, mb } from './mirror.mjs';

const PARALLEL = 4;

const args = process.argv.slice(2);
const mi = args.indexOf('--manifest');
const manifestFile = mi >= 0 ? args.splice(mi, 2)[1] : undefined;
let base = args[0];
if (!base || !/^https?:\/\//.test(base)) {
  console.error('usage: verify-bucket.mjs URL [--manifest FILE]');
  process.exit(2);
}
if (!base.endsWith('/')) base += '/';

// What base serves at f.path: '' when it is the pinned file, else what is wrong.
async function check(f) {
  let r;
  try {
    r = await fetch(base + f.path, { redirect: 'manual' });
  } catch (e) {
    return { fetched: 0, why: 'fetch failed: ' + (e.cause && e.cause.message || e.message) };
  }
  if (r.status !== 200) {
    if (r.body) await r.body.cancel();
    return { fetched: 0, why: `HTTP ${r.status}` + (r.headers.get('location') ? ' to ' + r.headers.get('location') : '') };
  }
  const got = await measure(r.body);
  return { fetched: got.size, why: mismatch(f, got) };
}

const files = readManifest(manifestFile);
let next = 0, ok = 0, fetched = 0;
await Promise.all(Array.from({ length: PARALLEL }, async () => {
  while (next < files.length) {
    const f = files[next++];
    const r = await check(f);
    fetched += r.fetched;
    if (r.why) console.log('BAD', f.path + ':', r.why);
    else ok++;
  }
}));
const want = totalBytes(files);
console.log(`${files.length} objects checked, ${ok} ok, ${files.length - ok} bad; ${want} bytes expected (${mb(want)}), ${fetched} fetched (${mb(fetched)}), from ${base}`);
process.exit(ok === files.length ? 0 : 1);
