// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// The Worker's viewer, built: ./public made from internal/client/web by
// internal/client/web/build.json — the rule the binary renders the same page
// by (internal/viewerbuild), held byte for byte the same by
// cloudflare/viewer_build_test.go. Every wrangler run runs it first
// (wrangler.toml [build]), so a deploy can never carry a page older than its
// sources, nor one naming a script it does not have. Nothing it writes is
// committed, and each run empties ./public (all but _headers) first, so a
// file no longer built is no longer deployed.
//
//   node scripts/build-viewer.mjs --previous https://live.reminal.app && npx wrangler deploy
//   npx wrangler dev                         ([build] as dev: this page alone)
//
// A deploy runs --previous immediately before it, in the same script, every
// time: what is kept is what the live Worker served at that moment.
//
// --previous: before a deploy, keep the builds the live Worker serves, for
// pages already open when this one replaces it (build.json's keep, this one
// included): at most that many of the newest its builds.txt names, each
// line exactly build.json's scripts in order, each file taken only if its
// content is the one its name says, every read capped. They are kept beside
// --out (public.kept/), with a note of the build they were kept for, and
// only from https://live.reminal.app unless --other-origin <origin> names
// another. A deploy that cannot read them fails, unless --first-deploy says
// there are none to keep.
//
// Without --previous, a run is a deploy's [build]: it serves the builds a
// --previous run kept for this same build, from https://live.reminal.app or
// from the origin this run itself names with --other-origin (never one
// carried over from the --previous run), and fails if there is no such run
// — so nothing a build of another tree, or of another Worker, left behind is
// ever deployed — or --first-deploy, which keeps nothing. Only --dev, or
// wrangler's own dev (it runs [build] with WRANGLER_COMMAND=dev, over any
// value in the environment), builds this page alone.
import { createHash } from "node:crypto";
import { cpSync, existsSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const KNOWN_VERSION = 1;
const KNOWN_NAME = "assets/{script}.{sha256:16}.js";
const KNOWN_SRI = "sha384";
const KNOWN_KEYS = new Set(["_", "version", "scripts", "name", "sri", "template", "tag", "keep", "copy"]);
// The live Worker, the only origin --previous reads unless --other-origin
// names another.
export const LIVE = "https://live.reminal.app";
// What a live builds.txt, a script, and all of a run's scripts may be.
export const MAX_LIST = 4 << 10;
export const MAX_SCRIPT = 8 << 20;
export const MAX_TOTAL = 24 << 20;
// What only this builder writes in --out and beside it.
const OWN = "_headers";
const NOTE = "kept.json";

export function readSpec(text) {
  const s = JSON.parse(text);
  if (!s || typeof s !== "object" || Array.isArray(s)) throw new Error("build.json is not an object");
  for (const k of Object.keys(s)) if (!KNOWN_KEYS.has(k)) throw new Error(`build.json has ${JSON.stringify(k)}, which this build does not know`);
  if (s.version !== KNOWN_VERSION) throw new Error(`build.json is version ${JSON.stringify(s.version)}; this build knows only ${KNOWN_VERSION}`);
  if (!Array.isArray(s.scripts) || !s.scripts.length) throw new Error("build.json names no scripts");
  if (s.name !== KNOWN_NAME) throw new Error(`build.json names its assets ${JSON.stringify(s.name)}; this build knows only ${JSON.stringify(KNOWN_NAME)}`);
  if (s.sri !== KNOWN_SRI) throw new Error(`build.json's SRI is ${JSON.stringify(s.sri)}; this build knows only ${JSON.stringify(KNOWN_SRI)}`);
  if (typeof s.template !== "string" || !s.template.includes("{script}")) throw new Error("build.json's template does not name {script}");
  if (typeof s.tag !== "string" || !s.tag.includes("{name}") || !s.tag.includes("{sri}")) throw new Error("build.json's tag does not carry {name} and {sri}");
  if (!Number.isInteger(s.keep) || s.keep < 1) throw new Error("build.json keeps no builds");
  for (const sc of s.scripts) if (typeof sc !== "string" || !/^[a-z]+$/.test(sc)) throw new Error(`build.json's script ${JSON.stringify(sc)} is not a plain name`);
  for (const c of s.copy || []) {
    const parts = String(c).split("/");
    if (typeof c !== "string" || parts.some((p) => !p || p === "." || p === "..") || c === "index.html" || parts[0] === "assets") throw new Error(`build.json copies ${JSON.stringify(c)}, not a path of its own`);
  }
  return s;
}

const hex16 = (b) => createHash("sha256").update(b).digest("hex").slice(0, 16);
export const name = (script, js) => script + "." + hex16(js) + ".js";
export const sri = (js) => "sha384-" + createHash("sha384").update(js).digest("base64");

// count is how many times marker is in b.
function count(b, marker) {
  let n = 0;
  for (let i = b.indexOf(marker); i >= 0; i = b.indexOf(marker, i + marker.length)) n++;
  return n;
}

// render stamps index with each script's tag: exactly one marker each. It
// works on the page's bytes as they are, whatever their encoding.
export function render(spec, index, src) {
  let html = Buffer.from(index);
  const assets = {}, names = [];
  for (const sc of spec.scripts) {
    const js = src[sc];
    if (!js) throw new Error(`no ${sc}.js`);
    const marker = Buffer.from(spec.template.replaceAll("{script}", sc));
    const n = count(html, marker);
    if (n !== 1) throw new Error(`index.html has ${n} of ${marker}, want exactly 1`);
    const nm = name(sc, js);
    const at = html.indexOf(marker);
    const tag = Buffer.from(spec.tag.replaceAll("{name}", nm).replaceAll("{sri}", sri(js)));
    html = Buffer.concat([html.subarray(0, at), tag, html.subarray(at + marker.length)]);
    assets["assets/" + nm] = js;
    names.push(nm);
  }
  return { html, assets, build: names.join(" ") };
}

// lineNames is a builds.txt line's script names, if it is the first N of
// build.json's scripts in order (N at least 1), one build of each: all of
// them, or fewer for a build made before the later ones were added (a page
// already open on it loads only those). A script is therefore only ever
// added at the end of build.json's list. Allowing only one fewer failed the
// next deploy after a build that added two scripts at once.
function lineNames(spec, line) {
  const names = line.split(" ");
  if (names.length < 1 || names.length > spec.scripts.length) return null;
  for (let i = 0; i < names.length; i++) {
    const m = /^([a-z]+)\.([0-9a-f]{16})\.js$/.exec(names[i]);
    if (!m || m[1] !== spec.scripts[i]) return null;
  }
  return names;
}

// A response's body, refused past max bytes.
async function capped(resp, max, what) {
  if (!resp.ok) throw new Error(`${what}: the live Worker answered ${resp.status}`);
  const len = Number(resp.headers.get("content-length") || 0);
  if (len > max) throw new Error(`${what}: ${len} bytes, more than ${max}`);
  const reader = resp.body.getReader();
  const parts = [];
  let n = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    n += value.length;
    if (n > max) { await reader.cancel(); throw new Error(`${what}: more than ${max} bytes`); }
    parts.push(value);
  }
  return Buffer.concat(parts);
}

// liveBuilds is what the live Worker at origin serves for pages already open:
// at most keep of the newest builds its builds.txt names, other than this
// one, each script's content checked against the name it is served by.
// originOf is a URL's origin ("" for none).
const originOf = (u) => (u ? new URL(u).origin : "");

export async function liveBuilds(spec, origin, current, { otherOrigin = "", fetchFn = fetch } = {}) {
  const u = new URL(origin);
  if (u.origin !== LIVE && u.origin !== originOf(otherOrigin)) throw new Error(`--previous reads only ${LIVE}; say --other-origin ${u.origin} to read ${origin}`);
  if (u.protocol !== "https:" && !(u.protocol === "http:" && ["localhost", "127.0.0.1"].includes(u.hostname))) throw new Error(`--previous must be https: ${origin}`);
  const get = (path, max, what) => fetchFn(new URL(path, u.origin), { signal: AbortSignal.timeout(30_000), redirect: "error" }).then((r) => capped(r, max, what));
  const list = (await get("/assets/builds.txt", MAX_LIST, "its builds.txt")).toString("utf8");
  const lines = list.split("\n").filter((l) => l !== "").slice(0, spec.keep);
  const builds = [], files = {};
  let total = 0;
  for (const line of lines) {
    const names = lineNames(spec, line);
    if (!names) throw new Error(`its builds.txt has ${JSON.stringify(line)}, not one build of ${spec.scripts.join(" ")}`);
    if (line === current || builds.length >= spec.keep - 1) continue;
    for (const nm of names) {
      if (files["assets/" + nm]) continue;
      const js = await get("/assets/" + nm, MAX_SCRIPT, nm);
      total += js.length;
      if (total > MAX_TOTAL) throw new Error(`its builds are more than ${MAX_TOTAL} bytes`);
      if (hex16(js) !== nm.split(".")[1]) throw new Error(`${nm}: its content is not the one its name says`);
      files["assets/" + nm] = js;
    }
    builds.push(line);
  }
  return { builds, files };
}

// empty makes dir empty but for keep: only a directory this builder made,
// one with no files, or one that does not exist yet.
function empty(dir, mine, keep = []) {
  if (!existsSync(dir)) return mkdirSync(dir, { recursive: true });
  const there = readdirSync(dir);
  if (there.length && !there.some((f) => mine.includes(f))) throw new Error(`${dir} is not a directory this build made (no ${mine.join(" nor ")}); not emptying it`);
  for (const f of there) if (!keep.includes(f)) rmSync(join(dir, f), { recursive: true, force: true });
}

// keptFor reads what a --previous run kept for this build, each file checked
// against its name.
function keptFor(spec, store, current, otherOrigin) {
  let note;
  try { note = JSON.parse(readFileSync(join(store, NOTE), "utf8")); } catch {
    throw new Error(`no builds were kept for this deploy: run node scripts/build-viewer.mjs --previous ${LIVE} first (--first-deploy if there is no live Worker), or --dev`);
  }
  if (!Array.isArray(note.builds)) throw new Error(`${join(store, NOTE)} names no builds: run --previous again`);
  const want = originOf(otherOrigin) || LIVE;
  if (note.origin !== null && note.origin !== want) throw new Error(`the builds in ${store} were kept from ${note.origin}, and this deploy is of ${want}: ${note.origin === LIVE ? "" : `a deploy of them names it itself (--other-origin ${note.origin}), or `}run --previous again`);
  if (note.for !== current) throw new Error(`the builds in ${store} were kept for ${note.for}, and this is ${current}: run --previous again`);
  const files = {};
  for (const line of note.builds) {
    const names = lineNames(spec, line);
    if (!names) throw new Error(`${store} keeps ${JSON.stringify(line)}, not one build`);
    for (const nm of names) {
      let js = null;
      try { js = readFileSync(join(store, "assets", nm)); } catch {}
      if (!js || hex16(js) !== nm.split(".")[1]) throw new Error(`${store}: assets/${nm} is not the one its name says: run --previous again`);
      files["assets/" + nm] = js;
    }
  }
  return { builds: note.builds, files, origin: note.origin };
}

// build writes the page to out, with the builds kept for it.
export async function build({ web, out, store = out.replace(/\/+$/, "") + ".kept", previous = "", otherOrigin = "", firstDeploy = false, dev = false, fetchFn = fetch, log = () => {} }) {
  const spec = readSpec(readFileSync(join(web, "build.json"), "utf8"));
  const src = {};
  for (const sc of spec.scripts) src[sc] = readFileSync(join(web, sc + ".js"));
  const page = render(spec, readFileSync(join(web, "index.html")), src);
  let kept = { builds: [], files: {} };
  // --previous and --first-deploy keep (or keep nothing) whatever the mode.
  if (previous || firstDeploy) {
    dev = false;
    if (previous) {
      try {
        kept = await liveBuilds(spec, previous, page.build, { otherOrigin, fetchFn });
      } catch (e) {
        if (!firstDeploy) throw new Error(`the live builds at ${previous} could not be kept (${e.message}); say --first-deploy if there are none`);
        log(`no live builds kept (${e.message}): --first-deploy`);
      }
    }
    empty(store, [NOTE]);
    mkdirSync(join(store, "assets"));
    for (const [rel, b] of Object.entries(kept.files)) writeFileSync(join(store, rel), b);
    writeFileSync(join(store, NOTE), JSON.stringify({ for: page.build, origin: previous ? originOf(previous) : null, builds: kept.builds }, null, 2) + "\n");
  } else if (!dev) {
    kept = keptFor(spec, store, page.build, otherOrigin);
  }
  empty(out, [OWN, "index.html", "assets"], [OWN]);
  for (const c of spec.copy) cpSync(join(web, c), join(out, c), { recursive: true });
  const files = { ...kept.files, ...page.assets, "index.html": page.html, "assets/builds.txt": [page.build, ...kept.builds].join("\n") + "\n" };
  mkdirSync(join(out, "assets"), { recursive: true });
  for (const [rel, b] of Object.entries(files)) writeFileSync(join(out, rel), b);
  log(`built ${page.build}; kept ${kept.builds.length ? kept.builds.join(" | ") : "none before it"}${dev ? " (dev)" : ""}`);
  return { build: page.build, kept: kept.builds };
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const here = dirname(fileURLToPath(import.meta.url));
  const args = process.argv.slice(2);
  const opt = (k) => { const i = args.indexOf(k); return i >= 0 ? args[i + 1] : ""; };
  const out = resolve(opt("--out") || join(here, "..", "public"));
  const web = resolve(opt("--web") || join(here, "..", "..", "internal", "client", "web"));
  if (!existsSync(join(web, "build.json"))) { console.error(`build-viewer: no ${join(web, "build.json")}`); process.exit(1); }
  build({
    web, out, previous: opt("--previous"),
    otherOrigin: opt("--other-origin"),
    firstDeploy: args.includes("--first-deploy"),
    dev: args.includes("--dev") || process.env.WRANGLER_COMMAND === "dev",
    log: (m) => console.log("build-viewer: " + m),
  }).catch((e) => { console.error("build-viewer: " + e.message); process.exit(1); });
}
