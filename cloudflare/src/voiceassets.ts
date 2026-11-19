// /voice-assets/<path>: the voice panel's libraries and models, served from
// our own R2 bucket at fixed versions instead of from jsDelivr and Hugging
// Face. Paths name exact versions (npm/<pkg>@<version>/..., hf/<repo>/<commit
// id>/...), so a file never changes under its path: cached for a year,
// immutable. They are public model and library files, so any origin may load
// them (the local relay's viewer and exposed port-*.reminal.app pages do).
// transformers.js reads models with Range requests and expects 206.

export interface VoiceAssetsEnv {
  VOICE_ASSETS?: R2Bucket;
}

export const VOICE_ASSETS_PREFIX = "/voice-assets/";

const TYPES: Record<string, string> = {
  wasm: "application/wasm",
  js: "text/javascript; charset=utf-8",
  mjs: "text/javascript; charset=utf-8",
  json: "application/json",
};

// The key a request path names, or null for a path that is not one: only
// the two trees there are, no empty, "." or ".." segment, nothing
// percent-encoded (an encoded "/" or ".." would name another key than it
// looks like) and no backslash.
export function voiceAssetKey(pathname: string): string | null {
  if (!pathname.startsWith(VOICE_ASSETS_PREFIX)) return null;
  const key = pathname.slice(VOICE_ASSETS_PREFIX.length);
  if (!key || key.length > 1024 || /[%\\\0]/.test(key)) return null;
  const segs = key.split("/");
  if (segs.some((s) => s === "" || s === "." || s === "..")) return null;
  if (segs[0] !== "npm" && segs[0] !== "hf") return null;
  if (segs.length < 3) return null;
  return key;
}

function contentType(key: string): string {
  const name = key.slice(key.lastIndexOf("/") + 1);
  const dot = name.lastIndexOf(".");
  return (dot > 0 && TYPES[name.slice(dot + 1).toLowerCase()]) || "application/octet-stream";
}

function baseHeaders(): Headers {
  return new Headers({
    "access-control-allow-origin": "*",
    "access-control-expose-headers": "Content-Length, Content-Range, ETag, Accept-Ranges",
    // The voice frame may run cross-origin isolated (threads need it): its
    // no-cors loads need this too.
    "cross-origin-resource-policy": "cross-origin",
    "x-content-type-options": "nosniff",
  });
}

function plain(status: number, text: string, extra?: Record<string, string>): Response {
  const h = baseHeaders();
  h.set("content-type", "text/plain; charset=utf-8");
  h.set("cache-control", "no-store");
  for (const [k, v] of Object.entries(extra || {})) h.set(k, v);
  return new Response(text, { status, headers: h });
}

// One range of "bytes=a-b", "bytes=a-" or "bytes=-n" against size; "none"
// for a header that is not a single byte range (served whole, as RFC 9110
// allows), "bad" for one that cannot be met (416).
type Range = { offset: number; length: number } | "none" | "bad";
export function parseRange(header: string | null, size: number): Range {
  if (!header) return "none";
  const m = header.trim().match(/^bytes=(\d*)-(\d*)$/i);
  if (!m || (m[1] === "" && m[2] === "")) return "none";
  if (m[1] === "") {
    const n = Number(m[2]);
    if (n === 0) return "bad";
    const length = Math.min(n, size);
    return size === 0 ? "bad" : { offset: size - length, length };
  }
  const start = Number(m[1]);
  if (start >= size) return "bad";
  let end = m[2] === "" ? size - 1 : Number(m[2]);
  if (end < start) return "none";
  end = Math.min(end, size - 1);
  return { offset: start, length: end - start + 1 };
}

function objectHeaders(key: string, obj: R2Object): Headers {
  const h = baseHeaders();
  h.set("content-type", contentType(key));
  h.set("cache-control", "public, max-age=31536000, immutable");
  h.set("etag", obj.httpEtag);
  h.set("accept-ranges", "bytes");
  return h;
}

// Whether the request's If-None-Match names this ETag (weak or strong).
function notModified(request: Request, httpEtag: string | null): boolean {
  const inm = request.headers.get("if-none-match");
  if (!inm || !httpEtag) return false;
  const tags = inm.split(",").map((s) => s.trim().replace(/^W\//, ""));
  return tags.includes("*") || tags.includes(httpEtag);
}

// The edge cache (caches.default), where there is one: workers.dev and the
// tests' node have none, and a Worker then reads R2 every time.
function edgeCache(): Cache | null {
  try { return (globalThis as { caches?: { default?: Cache } }).caches?.default ?? null; } catch { return null; }
}

// A whole file from the edge cache, as a 200 or a 304. The cache key is the
// URL alone (a GET with no headers): one entry per file, whatever was asked.
async function fromCache(cache: Cache, key: Request, request: Request): Promise<Response | null> {
  const hit = await cache.match(key);
  if (!hit) return null;
  const h = new Headers(hit.headers);
  if (notModified(request, h.get("etag"))) {
    await hit.body?.cancel();
    return new Response(null, { status: 304, headers: h });
  }
  return new Response(request.method === "HEAD" ? null : hit.body, { status: 200, headers: h });
}

export async function handleVoiceAsset(request: Request, env: VoiceAssetsEnv): Promise<Response> {
  const url = new URL(request.url);
  const method = request.method.toUpperCase();
  if (method === "OPTIONS") {
    // A preflight, for a Range header that is not a simple one ("bytes=-n").
    const h = baseHeaders();
    h.set("access-control-allow-methods", "GET, HEAD");
    h.set("access-control-allow-headers", "Range, If-None-Match");
    h.set("access-control-max-age", "86400");
    return new Response(null, { status: 204, headers: h });
  }
  if (method !== "GET" && method !== "HEAD") return plain(405, "method not allowed", { allow: "GET, HEAD" });
  const key = voiceAssetKey(url.pathname);
  if (!key) return plain(400, "bad path");
  const bucket = env.VOICE_ASSETS;
  if (!bucket) return plain(503, "voice assets are not set up here");

  const rangeHeader = method === "GET" ? request.headers.get("range") : null;
  // Whole files (GET and HEAD with no Range) come from the edge cache when
  // it has them. A range is read from R2 (cheap, and slicing a cached model
  // of hundreds of MB here would not fit in a Worker's memory).
  const cache = edgeCache();
  const cacheKey = new Request(url.origin + url.pathname);
  if (cache && !rangeHeader) {
    const hit = await fromCache(cache, cacheKey, request);
    if (hit) return hit;
  }
  // Whole-file GETs go straight to get(); a range needs the size first.
  if (method === "HEAD" || rangeHeader) {
    const head = await bucket.head(key);
    if (!head) return plain(404, "not found");
    const h = objectHeaders(key, head);
    if (notModified(request, head.httpEtag)) return new Response(null, { status: 304, headers: h });
    if (method === "HEAD") {
      h.set("content-length", String(head.size));
      return new Response(null, { status: 200, headers: h });
    }
    const r = parseRange(rangeHeader, head.size);
    if (r === "bad") return plain(416, "range not satisfiable", { "content-range": `bytes */${head.size}` });
    if (r !== "none") {
      const obj = await bucket.get(key, { range: { offset: r.offset, length: r.length } });
      if (!obj || !("body" in obj)) return plain(404, "not found");
      // The object changed between the two reads: never for these paths, but
      // a range of another version than the ETag says must not be sent.
      if (obj.httpEtag !== head.httpEtag) return plain(503, "changed, try again", { "retry-after": "1" });
      h.set("content-range", `bytes ${r.offset}-${r.offset + r.length - 1}/${head.size}`);
      h.set("content-length", String(r.length));
      return new Response(obj.body, { status: 206, headers: h });
    }
  }
  const whole = await wholeFromR2(bucket, key, request);
  if (whole.status !== 200 || !cache || rangeHeader) return whole;
  // Into the edge cache, then served from it: stored first and read back,
  // rather than one body sent two ways at once, which would hold all of a
  // large model in memory when the person reads it slower than the cache
  // takes it. Where the cache keeps nothing, R2 is read again.
  try {
    await cache.put(cacheKey, whole);
  } catch {
    return wholeFromR2(bucket, key, request); // too large to keep, say: R2 serves it
  }
  return (await fromCache(cache, cacheKey, request)) ?? wholeFromR2(bucket, key, request);
}

// A whole file from R2, as a 200 (or 304, or 404). Only If-None-Match is
// honoured; other preconditions are not asked.
async function wholeFromR2(bucket: R2Bucket, key: string, request: Request): Promise<Response> {
  const inm = request.headers.get("if-none-match");
  const obj = await bucket.get(key, inm ? { onlyIf: new Headers({ "if-none-match": inm }) } : {});
  if (!obj) return plain(404, "not found");
  const h = objectHeaders(key, obj);
  if (!("body" in obj)) return new Response(null, { status: 304, headers: h }); // a precondition failed
  h.set("content-length", String(obj.size));
  return new Response(obj.body, { status: 200, headers: h });
}

// Past the caller's limit (index.ts counts it): a 429 the page can read
// (with the CORS headers), to try again in a minute.
export function voiceAssetsTooMany(): Response {
  return plain(429, "too many requests, try again in a minute", { "retry-after": "60" });
}
