// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// The Worker's viewer, built: ./public made from internal/client/web by
// internal/client/web/build.json — the rule the binary renders the same page
// by (internal/viewerbuild), held byte for byte the same by
// scripts/viewer-parity.sh. Every wrangler run runs it first (wrangler.toml
// [build]), so a deploy can never carry a page older than its sources, nor
// one naming a script it does not have. Nothing it writes is committed.
//
//   node scripts/build-viewer.mjs [--out public] [--previous https://<live>] [--first-deploy]
//
// --previous: the builds the live Worker serves, kept for pages already
// open when this one replaces it (build.json's keep, this one included):
// at most that many of the newest its builds.txt names, each file taken
// only if its content is the one its name says. Without --previous, the
// builds a run before this one kept in --out are kept as they are. A deploy
// that cannot read the live builds fails, unless --first-deploy says there
// are none to keep.
import { createHash } from "node:crypto";
import { cpSync, existsSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const KNOWN_NAME = "assets/{script}.{sha256:16}.js";
const KNOWN_SRI = "sha384";
// What a live builds.txt and a script may be, read from a live Worker.
export const MAX_LIST = 4 << 10;
export const MAX_SCRIPT = 8 << 20;
const NAME = /^[a-z]+\.([0-9a-f]{16})\.js$/;

export function readSpec(text) {
  const s = JSON.parse(text);
  if (!Array.isArray(s.scripts) || !s.scripts.length) throw new Error("build.json names no scripts");
  if (s.name !== KNOWN_NAME) throw new Error(`build.json names its assets ${JSON.stringify(s.name)}; this build knows only ${JSON.stringify(KNOWN_NAME)}`);
  if (s.sri !== KNOWN_SRI) throw new Error(`build.json's SRI is ${JSON.stringify(s.sri)}; this build knows only ${JSON.stringify(KNOWN_SRI)}`);
  if (!String(s.template).includes("{script}")) throw new Error("build.json's template does not name {script}");
  if (!String(s.tag).includes("{name}") || !String(s.tag).includes("{sri}")) throw new Error("build.json's tag does not carry {name} and {sri}");
  if (!Number.isInteger(s.keep) || s.keep < 1) throw new Error("build.json keeps no builds");
  for (const sc of s.scripts) if (!/^[^/\\.]+$/.test(String(sc))) throw new Error(`build.json's script ${JSON.stringify(sc)} is not a plain name`);
  return s;
}

const hex16 = (b) => createHash("sha256").update(b).digest("hex").slice(0, 16);
export const name = (script, js) => script + "." + hex16(js) + ".js";
export const sri = (js) => "sha384-" + createHash("sha384").update(js).digest("base64");

// render stamps index with each script's tag: exactly one marker each.
export function render(spec, index, src) {
  let html = String(index);
  const assets = {}, names = [];
  for (const sc of spec.scripts) {
    const js = src[sc];
    if (!js) throw new Error(`no ${sc}.js`);
    const marker = spec.template.replaceAll("{script}", sc);
    const n = html.split(marker).length - 1;
    if (n !== 1) throw new Error(`index.html has ${n} of ${marker}, want exactly 1`);
    const nm = name(sc, js);
    html = html.replace(marker, () => spec.tag.replaceAll("{name}", nm).replaceAll("{sri}", sri(js)));
    assets["assets/" + nm] = js;
    names.push(nm);
  }
  return { html, assets, build: names.join(" ") };
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
export async function liveBuilds(origin, current, keep, fetchFn = fetch) {
  const u = new URL(origin);
  if (u.protocol !== "https:" && !(u.protocol === "http:" && ["localhost", "127.0.0.1"].includes(u.hostname))) throw new Error(`--previous must be https: ${origin}`);
  const get = (path, max, what) => fetchFn(new URL(path, u), { signal: AbortSignal.timeout(30_000), redirect: "error" }).then((r) => capped(r, max, what));
  const list = (await get("/assets/builds.txt", MAX_LIST, "its builds.txt")).toString("utf8");
  const lines = list.split("\n").map((l) => l.trim()).filter(Boolean).slice(0, keep);
  const builds = [], files = {};
  for (const line of lines) {
    if (line === current || builds.length >= keep - 1) continue;
    for (const nm of line.split(/\s+/)) {
      const m = NAME.exec(nm);
      if (!m) throw new Error(`its builds.txt names ${JSON.stringify(nm)}, not a script`);
      const js = await get("/assets/" + nm, MAX_SCRIPT, nm);
      if (hex16(js) !== m[1]) throw new Error(`${nm}: its content is not the one its name says`);
      files["assets/" + nm] = js;
    }
    builds.push(line);
  }
  return { builds, files };
}

// keptBuilds is what an earlier run left in out, checked the same way.
function keptBuilds(out, current, keep) {
  let list = "";
  try { list = readFileSync(join(out, "assets", "builds.txt"), "utf8"); } catch { return { builds: [], files: {} }; }
  const builds = [], files = {};
  for (const line of list.split("\n").map((l) => l.trim()).filter(Boolean)) {
    if (line === current || builds.length >= keep - 1) continue;
    const these = {};
    let whole = true;
    for (const nm of line.split(/\s+/)) {
      const m = NAME.exec(nm);
      let js = null;
      try { js = readFileSync(join(out, "assets", nm)); } catch {}
      if (!m || !js || hex16(js) !== m[1]) { whole = false; break; }
      these["assets/" + nm] = js;
    }
    if (whole) { builds.push(line); Object.assign(files, these); }
  }
  return { builds, files };
}

export async function build({ web, out, previous = "", firstDeploy = false, fetchFn = fetch, log = () => {} }) {
  const spec = readSpec(readFileSync(join(web, "build.json"), "utf8"));
  const src = {};
  for (const sc of spec.scripts) src[sc] = readFileSync(join(web, sc + ".js"));
  const page = render(spec, readFileSync(join(web, "index.html")), src);
  let kept;
  if (previous) {
    try {
      kept = await liveBuilds(previous, page.build, spec.keep, fetchFn);
    } catch (e) {
      if (!firstDeploy) throw new Error(`the live builds at ${previous} could not be kept (${e.message}); say --first-deploy if there are none`);
      log(`no live builds kept (${e.message}): --first-deploy`);
      kept = { builds: [], files: {} };
    }
  } else {
    kept = keptBuilds(out, page.build, spec.keep);
  }
  for (const c of spec.copy) cpSync(join(web, c), join(out, c), { recursive: true });
  const files = { ...kept.files, ...page.assets, "index.html": page.html, "assets/builds.txt": [page.build, ...kept.builds].join("\n") + "\n" };
  mkdirSync(join(out, "assets"), { recursive: true });
  for (const [rel, b] of Object.entries(files)) writeFileSync(join(out, rel), b);
  // Any other script in assets/ is past keep.
  for (const f of readdirSync(join(out, "assets"))) if (f.endsWith(".js") && !files["assets/" + f]) rmSync(join(out, "assets", f));
  log(`built ${page.build}; kept ${kept.builds.length ? kept.builds.join(" | ") : "none before it"}`);
  return { build: page.build, kept: kept.builds };
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const here = dirname(fileURLToPath(import.meta.url));
  const args = process.argv.slice(2);
  const opt = (k) => { const i = args.indexOf(k); return i >= 0 ? args[i + 1] : ""; };
  const out = resolve(opt("--out") || join(here, "..", "public"));
  const web = resolve(opt("--web") || join(here, "..", "..", "internal", "client", "web"));
  if (!existsSync(join(web, "build.json"))) { console.error(`build-viewer: no ${join(web, "build.json")}`); process.exit(1); }
  build({ web, out, previous: opt("--previous"), firstDeploy: args.includes("--first-deploy"), log: (m) => console.log("build-viewer: " + m) })
    .catch((e) => { console.error("build-viewer: " + e.message); process.exit(1); });
}
