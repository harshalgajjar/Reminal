// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package cloudflare

import (
	"encoding/json"
	"strings"
	"testing"
)

// The voice panel's files, served from R2 through the Worker's real fetch
// (src/index.ts routing to src/voiceassets.ts), with a stand-in bucket.
const voiceAssetsScript = `
import worker from "SRC/index.ts";
const data = new TextEncoder().encode("0123456789abcdefghij"); // 20 bytes
const K = {
  wasm: "npm/onnxruntime-web@1.22.0/dist/ort-wasm-simd-threaded.wasm",
  onnx: "hf/onnx-community/Kokoro-82M-v1.0-ONNX/1939ad2a8e416c0acfeecc08a694d14ef25f2231/onnx/model_quantized.onnx",
  mjs: "npm/@huggingface/transformers@4.3.2/dist/transformers.web.mjs",
  json: "hf/onnx-community/Kokoro-82M-v1.0-ONNX/1939ad2a8e416c0acfeecc08a694d14ef25f2231/config.json",
};
const objs = Object.fromEntries(Object.values(K).map((k) => [k, data]));
const calls = [];
const meta = (k) => ({ key: k, size: objs[k].length, etag: "abc", httpEtag: '"abc"' });
const VOICE_ASSETS = {
  async head(k) { calls.push("head " + k); return objs[k] ? meta(k) : null; },
  async get(k, o = {}) {
    calls.push("get " + k);
    if (!objs[k]) return null;
    const m = meta(k);
    const inm = o.onlyIf && o.onlyIf.get && o.onlyIf.get("if-none-match");
    if (inm && inm.includes(m.httpEtag)) return m;
    let b = objs[k];
    if (o.range) b = b.slice(o.range.offset, o.range.offset + o.range.length);
    return { ...m, body: new Response(b).body };
  },
  async put() { throw new Error("the Worker must never write"); },
  async delete() { throw new Error("the Worker must never delete"); },
};
const env = { VOICE_ASSETS, ASSETS: { fetch: () => new Response("viewer") } };
const H = ["content-type", "cache-control", "etag", "accept-ranges", "content-length", "content-range",
  "access-control-allow-origin", "access-control-expose-headers", "cross-origin-resource-policy", "allow"];
async function ask(name, path, init = {}, e = env) {
  calls.length = 0;
  const r = await worker.fetch(new Request("https://live.reminal.app" + path, init), e);
  const body = init.method === "HEAD" ? "" : await r.text();
  const headers = {};
  for (const h of H) if (r.headers.get(h) !== null) headers[h] = r.headers.get(h);
  console.log(JSON.stringify({ name, status: r.status, body: r.status < 300 ? body : "", headers, calls: [...calls] }));
}
const P = "/voice-assets/";
await ask("wasm", P + K.wasm);
await ask("onnx", P + K.onnx);
await ask("mjs", P + K.mjs);
await ask("json", P + K.json);
await ask("range", P + K.onnx, { headers: { Range: "bytes=2-5" } });
await ask("range-open", P + K.onnx, { headers: { Range: "bytes=15-" } });
await ask("range-suffix", P + K.onnx, { headers: { Range: "bytes=-3" } });
await ask("range-past-end", P + K.onnx, { headers: { Range: "bytes=10-99" } });
await ask("range-unsatisfiable", P + K.onnx, { headers: { Range: "bytes=20-" } });
await ask("range-multi", P + K.onnx, { headers: { Range: "bytes=0-1,4-5" } });
await ask("head", P + K.wasm, { method: "HEAD" });
await ask("etag-match", P + K.wasm, { headers: { "If-None-Match": '"abc"' } });
await ask("missing", P + "npm/onnxruntime-web@1.22.0/dist/nope.wasm");
await ask("missing-range", P + "npm/onnxruntime-web@1.22.0/dist/nope.wasm", { headers: { Range: "bytes=0-1" } });
await ask("post", P + K.wasm, { method: "POST", body: "x" });
await ask("put", P + K.wasm, { method: "PUT", body: "x" });
await ask("delete", P + K.wasm, { method: "DELETE" });
await ask("preflight", P + K.wasm, { method: "OPTIONS" });
for (const [n, p] of [["dotdot", "npm/a@1/../../secret"], ["dot", "npm/a@1/./x.js"], ["empty-seg", "npm/a@1//x.js"],
  ["encoded-slash", "npm/a@1%2F..%2Fx.js"], ["encoded-dots", "npm/a@1/%2e%2e/x.js"], ["backslash", "npm/a@1/..\\x.js"],
  ["other-tree", "gh/a/b/c.js"], ["bare", ""], ["short", "npm/x.js"]]) {
  await ask("bad-" + n, P + p);
}
await ask("no-bucket", P + K.wasm, {}, { ASSETS: env.ASSETS });
// Paths as they could reach the function unnormalised.
import("SRC/voiceassets.ts").then(({ voiceAssetKey }) => console.log(JSON.stringify({ name: "keys", body: JSON.stringify(
  ["/voice-assets/npm/a@1/../x.js", "/voice-assets/npm/a@1/./x.js", "/voice-assets/npm/a@1//x.js", "/voice-assets/npm/a@1%2F..%2Fx.js",
   "/voice-assets/npm/a@1/..\\x.js", "/voice-assets/gh/a/b.js", "/voice-assets/", "/voice-assets/npm/x.js", "/voice-assets/hf/r/c/f.onnx"].map(voiceAssetKey)) })));
`

type voiceAnswer struct {
	Name    string            `json:"name"`
	Status  int               `json:"status"`
	Body    string            `json:"body"`
	Headers map[string]string `json:"headers"`
	Calls   []string          `json:"calls"`
}

func TestVoiceAssets(t *testing.T) {
	out := runWorkerTS(t, voiceAssetsScript)
	got := map[string]voiceAnswer{}
	for _, line := range jsonLines(out) {
		var a voiceAnswer
		if err := json.Unmarshal([]byte(line), &a); err != nil {
			t.Fatalf("%v: %q", err, line)
		}
		got[a.Name] = a
	}
	answer := func(name string) voiceAnswer {
		a, ok := got[name]
		if !ok {
			t.Fatalf("no answer for %s in:\n%s", name, out)
		}
		return a
	}
	cors := func(a voiceAnswer) {
		t.Helper()
		if a.Headers["access-control-allow-origin"] != "*" {
			t.Errorf("%s: allow-origin %q", a.Name, a.Headers["access-control-allow-origin"])
		}
		for _, h := range []string{"Content-Length", "Content-Range", "ETag", "Accept-Ranges"} {
			if !strings.Contains(a.Headers["access-control-expose-headers"], h) {
				t.Errorf("%s: %s not exposed (%q)", a.Name, h, a.Headers["access-control-expose-headers"])
			}
		}
	}
	whole := func(name, ctype string) {
		t.Helper()
		a := answer(name)
		if a.Status != 200 || a.Body != "0123456789abcdefghij" {
			t.Errorf("%s: %d %q", name, a.Status, a.Body)
		}
		want := map[string]string{"content-type": ctype, "cache-control": "public, max-age=31536000, immutable",
			"etag": `"abc"`, "accept-ranges": "bytes", "content-length": "20", "cross-origin-resource-policy": "cross-origin"}
		for k, v := range want {
			if a.Headers[k] != v {
				t.Errorf("%s: %s %q, want %q", name, k, a.Headers[k], v)
			}
		}
		cors(a)
	}
	whole("wasm", "application/wasm")
	whole("onnx", "application/octet-stream")
	whole("mjs", "text/javascript; charset=utf-8")
	whole("json", "application/json")

	for _, c := range []struct{ name, body, rng, length string }{
		{"range", "2345", "bytes 2-5/20", "4"},
		{"range-open", "fghij", "bytes 15-19/20", "5"},
		{"range-suffix", "hij", "bytes 17-19/20", "3"},
		{"range-past-end", "abcdefghij", "bytes 10-19/20", "10"},
	} {
		a := answer(c.name)
		if a.Status != 206 || a.Body != c.body || a.Headers["content-range"] != c.rng || a.Headers["content-length"] != c.length {
			t.Errorf("%s: %d %q range %q length %q; want 206 %q %q %q", c.name, a.Status, a.Body, a.Headers["content-range"], a.Headers["content-length"], c.body, c.rng, c.length)
		}
		if a.Headers["etag"] != `"abc"` || a.Headers["cache-control"] != "public, max-age=31536000, immutable" {
			t.Errorf("%s: headers %v", c.name, a.Headers)
		}
		cors(a)
	}
	if a := answer("range-unsatisfiable"); a.Status != 416 || a.Headers["content-range"] != "bytes */20" {
		t.Errorf("range past the end: %d %v", a.Status, a.Headers)
	}
	// More than one range is answered with the whole file, as RFC 9110 allows.
	if a := answer("range-multi"); a.Status != 200 || a.Body != "0123456789abcdefghij" {
		t.Errorf("multi-range: %d %q", a.Status, a.Body)
	}
	if a := answer("head"); a.Status != 200 || a.Body != "" || a.Headers["content-length"] != "20" || a.Headers["content-type"] != "application/wasm" {
		t.Errorf("HEAD: %d %q %v", a.Status, a.Body, a.Headers)
	}
	if a := answer("etag-match"); a.Status != 304 {
		t.Errorf("If-None-Match with the ETag: %d", a.Status)
	}
	for _, n := range []string{"missing", "missing-range"} {
		if a := answer(n); a.Status != 404 {
			t.Errorf("%s: %d", n, a.Status)
		}
	}
	for _, n := range []string{"post", "put", "delete"} {
		a := answer(n)
		if a.Status != 405 || a.Headers["allow"] != "GET, HEAD" {
			t.Errorf("%s: %d allow %q", n, a.Status, a.Headers["allow"])
		}
		if len(a.Calls) != 0 {
			t.Errorf("%s reached the bucket: %v", n, a.Calls)
		}
	}
	if a := answer("preflight"); a.Status != 204 || a.Headers["access-control-allow-origin"] != "*" {
		t.Errorf("preflight: %d %v", a.Status, a.Headers)
	}
	for name, a := range got {
		if !strings.HasPrefix(name, "bad-") {
			continue
		}
		// A request URL normalises "." and ".." away before the Worker sees
		// it; either way no such path may reach the bucket outside its trees.
		if a.Status == 200 || a.Status == 206 {
			t.Errorf("%s served: %d", name, a.Status)
		}
		for _, c := range a.Calls {
			if !strings.HasPrefix(c, "head npm/") && !strings.HasPrefix(c, "get npm/") && !strings.HasPrefix(c, "head hf/") && !strings.HasPrefix(c, "get hf/") || strings.Contains(c, "..") {
				t.Errorf("%s asked the bucket for %q", name, c)
			}
		}
	}
	if a := answer("no-bucket"); a.Status != 503 {
		t.Errorf("no bucket bound: %d", a.Status)
	}
}

func TestVoiceAssetKeyRefusesRawBadPaths(t *testing.T) {
	out := runWorkerTS(t, voiceAssetsScript)
	for _, line := range jsonLines(out) {
		var a voiceAnswer
		if json.Unmarshal([]byte(line), &a) == nil && a.Name == "keys" {
			if want := `[null,null,null,null,null,null,null,null,"hf/r/c/f.onnx"]`; a.Body != want {
				t.Errorf("voiceAssetKey: %s, want %s", a.Body, want)
			}
			return
		}
	}
	t.Fatalf("no keys line in:\n%s", out)
}

// The edge cache and the caller limit, through the Worker's real fetch: a
// whole file is read from R2 once and then from the cache (GET, HEAD and a
// 304 alike); a range always comes from R2; a cache that cannot keep a file
// still serves it; and a caller past the limit gets a 429 the page can read.
const voiceAssetsCacheScript = `
import worker from "SRC/index.ts";
const data = new TextEncoder().encode("0123456789abcdefghij");
const K = "npm/onnxruntime-web@1.22.0/dist/ort-wasm-simd-threaded.wasm";
const calls = [];
const meta = { key: K, size: data.length, etag: "abc", httpEtag: '"abc"' };
const VOICE_ASSETS = {
  async head(k) { calls.push("r2-head"); return k === K ? meta : null; },
  async get(k, o = {}) {
    calls.push(o.range ? "r2-range" : "r2-get");
    if (k !== K) return null;
    const inm = o.onlyIf && o.onlyIf.get && o.onlyIf.get("if-none-match");
    if (inm && inm.includes(meta.httpEtag)) return meta;
    const b = o.range ? data.slice(o.range.offset, o.range.offset + o.range.length) : data;
    return { ...meta, body: new Response(b).body };
  },
};
const store = new Map();
let refuse = false;
globalThis.caches = { default: {
  async match(req) { calls.push("cache-match"); const e = store.get(req.url); return e ? new Response(e.body, { headers: e.headers }) : undefined; },
  async put(req, res) {
    calls.push("cache-put");
    if (refuse) { await res.body?.cancel(); throw new Error("too large"); }
    if (req.headers.get("range") || req.method !== "GET") throw new Error("bad key");
    store.set(req.url, { body: new Uint8Array(await res.arrayBuffer()), headers: [...res.headers] });
  },
} };
const env = { VOICE_ASSETS, ASSETS: { fetch: () => new Response("viewer") } };
async function ask(name, init = {}, ip = "198.51.100.7", path = "/voice-assets/" + K) {
  calls.length = 0;
  const h = new Headers(init.headers || {}); h.set("cf-connecting-ip", ip);
  const r = await worker.fetch(new Request("https://live.reminal.app" + path, { ...init, headers: h }), env);
  const body = init.method === "HEAD" ? "" : await r.text();
  console.log(JSON.stringify({ name, status: r.status, body, etag: r.headers.get("etag"), cc: r.headers.get("cache-control"), acao: r.headers.get("access-control-allow-origin"), calls: [...calls] }));
}
await ask("first");
await ask("second");
await ask("head", { method: "HEAD" });
await ask("etag", { headers: { "If-None-Match": '"abc"' } });
await ask("range", { headers: { Range: "bytes=2-5" } });
refuse = true; store.clear();
await ask("refused");
for (let i = 0; i < 600; i++) { const h = new Headers({ "cf-connecting-ip": "203.0.113.9" }); await (await worker.fetch(new Request("https://live.reminal.app/voice-assets/" + K, { headers: h }), env)).arrayBuffer(); }
await ask("over", {}, "203.0.113.9");
await ask("other-caller", {}, "203.0.113.10");
`

func TestVoiceAssetsEdgeCacheAndLimit(t *testing.T) {
	out := runWorkerTS(t, voiceAssetsCacheScript)
	type answer struct {
		Name   string   `json:"name"`
		Status int      `json:"status"`
		Body   string   `json:"body"`
		ETag   string   `json:"etag"`
		CC     string   `json:"cc"`
		ACAO   string   `json:"acao"`
		Calls  []string `json:"calls"`
	}
	got := map[string]answer{}
	for _, line := range jsonLines(out) {
		var a answer
		if err := json.Unmarshal([]byte(line), &a); err != nil {
			t.Fatalf("%v: %q", err, line)
		}
		got[a.Name] = a
	}
	calls := func(name string) string { return strings.Join(got[name].Calls, " ") }
	const whole = "0123456789abcdefghij"
	if a := got["first"]; a.Status != 200 || a.Body != whole || calls("first") != "cache-match r2-get cache-put cache-match" {
		t.Errorf("first GET: %d %q %s", a.Status, a.Body, calls("first"))
	}
	if a := got["second"]; a.Status != 200 || a.Body != whole || a.ETag != `"abc"` || a.CC != "public, max-age=31536000, immutable" || calls("second") != "cache-match" {
		t.Errorf("second GET (cached): %d %q %q %q %s", a.Status, a.Body, a.ETag, a.CC, calls("second"))
	}
	if a := got["head"]; a.Status != 200 || calls("head") != "cache-match" {
		t.Errorf("HEAD (cached): %d %s", a.Status, calls("head"))
	}
	if a := got["etag"]; a.Status != 304 || calls("etag") != "cache-match" {
		t.Errorf("If-None-Match (cached): %d %s", a.Status, calls("etag"))
	}
	if a := got["range"]; a.Status != 206 || a.Body != "2345" || strings.Contains(calls("range"), "cache") {
		t.Errorf("range: %d %q %s (a range never goes through the cache)", a.Status, a.Body, calls("range"))
	}
	if a := got["refused"]; a.Status != 200 || a.Body != whole {
		t.Errorf("a cache that cannot keep it: %d %q %s", a.Status, a.Body, calls("refused"))
	}
	if a := got["over"]; a.Status != 429 || a.ACAO != "*" || len(a.Calls) != 0 {
		t.Errorf("past the limit: %d acao %q, asked %s", a.Status, a.ACAO, calls("over"))
	}
	if a := got["other-caller"]; a.Status != 200 {
		t.Errorf("another caller: %d", a.Status)
	}
}

// jsonLines are the lines of out that are JSON objects: the Worker's own
// warnings (no LIMITER binding here) are printed among them.
func jsonLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.HasPrefix(l, "{") {
			lines = append(lines, l)
		}
	}
	return lines
}
