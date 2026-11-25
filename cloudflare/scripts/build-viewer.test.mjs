// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// build-viewer.mjs's own rules (run by cloudflare/viewer_build_test.go).
import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, existsSync, readdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import { build, name, sri, readSpec, MAX_SCRIPT, MAX_TOTAL, LIVE } from "./build-viewer.mjs";

const SPEC = readFileSync(new URL("../../internal/client/web/build.json", import.meta.url), "utf8");
// LIST is build.json's scripts. Every page, file and builds.txt line below is
// made from it, so these tests hold whatever the list is; they ask only that
// it has "app" (the script they vary) and at least three scripts.
const LIST = JSON.parse(SPEC).scripts;
const MO = "mo()\n";
const PK = "// pick\n";
const VU = "// voiceui\n";
// src is what a script's source holds here: app.js as given.
const src = (s, app = "console.log('one')\n") => (s === "app" ? app : s === "header" ? "fit()\n" : s === "moments" ? MO : `// ${s}\n`);
// The page's markers, in an order that is not the list's: the first, then the rest from the last.
const markers = (list = LIST, nl = "\n") => [list[0], ...list.slice(1).reverse()].map((s) => `<script data-build="${s}"></script>${nl}`).join("");
// nm is the name script s is built to.
const nm = (s, app) => name(s, Buffer.from(src(s, app)));
// NOT_ONE is the refusal of a line that is not one build of the list.
const NOT_ONE = new RegExp("not one build of " + LIST.join(" "));

// web: a viewer's sources, app.js as given, for the scripts of list (spec is its build.json).
function web(app = "console.log('one')\n", list = LIST, spec = SPEC) {
  const d = mkdtempSync(join(tmpdir(), "web-"));
  writeFileSync(join(d, "build.json"), spec);
  writeFileSync(join(d, "index.html"), "<!doctype html>\n" + markers(list));
  for (const s of list) writeFileSync(join(d, s + ".js"), src(s, app));
  writeFileSync(join(d, "sw.js"), "// sw\n");
  writeFileSync(join(d, "reload.js"), "// guard\n");
  writeFileSync(join(d, "manifest.webmanifest"), "{}");
  for (const sub of ["icons", "vendor", "voice"]) { mkdirSync(join(d, sub)); writeFileSync(join(d, sub, "x"), "x"); }
  return d;
}
// out: a new ./public, as the tree has it (_headers alone).
const out = () => { const d = join(mkdtempSync(join(tmpdir(), "cf-")), "public"); mkdirSync(d); writeFileSync(join(d, "_headers"), "/*\n"); return d; };
// lineOf is the builds.txt line of the build with this app.js: the first n scripts of list.
const lineOf = (app, n = LIST.length, list = LIST) => list.slice(0, n).map((s) => nm(s, app)).join(" ");
// filesOf is those scripts as a live Worker serves them, by path.
const filesOf = (app, n = LIST.length, list = LIST) => Object.fromEntries(list.slice(0, n).map((s) => ["/assets/" + nm(s, app), src(s, app)]));
const other = "https://live.example";

// A live Worker: its builds.txt and scripts, as files by path.
function live(files) {
  const asked = [];
  const fetchFn = async (url) => {
    const p = new URL(url).pathname;
    asked.push(p);
    const b = files[p];
    if (b === undefined) return new Response("no", { status: 404 });
    return new Response(b);
  };
  return { fetchFn, asked };
}
// liveOf: a live Worker serving these builds (apps), newest first.
function liveOf(apps, extra = {}) {
  const files = { "/assets/builds.txt": apps.map((a) => lineOf(a)).join("\n") + "\n", ...extra };
  for (const a of apps) Object.assign(files, filesOf(a));
  return live(files);
}
const scripts = (o) => readdirSync(join(o, "assets")).filter((f) => f.endsWith(".js")).sort();
// namesIn is the script names these builds.txt lines hold, each once, sorted.
const namesIn = (...lines) => [...new Set(lines.join(" ").split(" "))].sort();

test("each script is named by its content and its tag stamped with its SRI", async () => {
  const o = out();
  const r = await build({ web: web(), out: o, dev: true });
  const html = readFileSync(join(o, "index.html"), "utf8");
  const js = Buffer.from("console.log('one')\n");
  assert.ok(html.includes(`<script src="/assets/${name("app", js)}" integrity="${sri(js)}" crossorigin="anonymous"></script>`));
  assert.ok(!html.includes("data-build"));
  assert.equal(readFileSync(join(o, "assets", name("app", js)), "utf8"), "console.log('one')\n");
  assert.equal(readFileSync(join(o, "assets", "builds.txt"), "utf8"), r.build + "\n");
  assert.ok(existsSync(join(o, "sw.js")) && existsSync(join(o, "icons", "x")) && existsSync(join(o, "vendor", "x")) && existsSync(join(o, "_headers")));
});

test("the page is stamped as its bytes, not as text", async () => {
  const w = web("a('$&$1')\n");
  writeFileSync(join(w, "index.html"), Buffer.concat([Buffer.from([0xef, 0xbb, 0xbf]), Buffer.from("<html>"), Buffer.from([0xe9, 0xff]), Buffer.from("\r\n" + markers(LIST, "\r\n"))]));
  const o = out();
  await build({ web: w, out: o, dev: true });
  const html = readFileSync(join(o, "index.html"));
  assert.deepEqual([...html.subarray(0, 11)], [0xef, 0xbb, 0xbf, ...Buffer.from("<html>"), 0xe9, 0xff]);
  assert.ok(html.includes(`/assets/${name("app", Buffer.from("a('$&$1')\n"))}"`));
});

test("a rule or a key this build does not know is refused", () => {
  for (const [from, to] of [
    ['"sha384"', '"sha256"'], ["{sha256:16}", "{sha256:12}"], ['"keep": 3', '"keep": 0'],
    ['"version": 1', '"version": 2'], ['"keep": 3', '"keep": 3, "minify": true'],
    [JSON.stringify(LIST[0]), JSON.stringify(LIST[0][0].toUpperCase() + LIST[0].slice(1))], ['"sw.js"', '"../sw.js"'], ['"sw.js"', '"index.html"'], ['"sw.js"', '"assets/x.js"'],
  ]) {
    assert.ok(SPEC.includes(from), from);
    assert.throws(() => readSpec(SPEC.replace(from, to)), undefined, to);
  }
});

test("a live Worker's newest builds are kept, at most keep, each checked against its name", async () => {
  const o = out();
  const { fetchFn } = liveOf(["v4\n", "v3\n", "v2\n", "v1\n"]);
  const r = await build({ web: web("v5\n"), out: o, previous: other, otherOrigin: other, fetchFn });
  // keep 3: this one and the live list's two newest — never v2 nor v1.
  assert.deepEqual(r.kept, [lineOf("v4\n"), lineOf("v3\n")]);
  const listed = readFileSync(join(o, "assets", "builds.txt"), "utf8").trim().split("\n");
  assert.deepEqual(listed, [lineOf("v5\n"), lineOf("v4\n"), lineOf("v3\n")]);
  assert.deepEqual(scripts(o), [...new Set(listed.join(" ").split(" "))].sort());
  // The deploy's [build] after it serves what it kept, for this build.
  const again = await build({ web: web("v5\n"), out: o, otherOrigin: other });
  assert.deepEqual(again.kept, [lineOf("v4\n"), lineOf("v3\n")]);
  assert.deepEqual(scripts(o), [...new Set(listed.join(" ").split(" "))].sort());
});

test("the live list's newest three only: a live list of the same build and older ones", async () => {
  const { fetchFn, asked } = liveOf(["v5\n", "v4\n", "v3\n", "v2\n"]);
  const r = await build({ web: web("v5\n"), out: out(), previous: other, otherOrigin: other, fetchFn });
  assert.deepEqual(r.kept, [lineOf("v4\n"), lineOf("v3\n")]);
  assert.ok(!asked.includes("/assets/" + name("app", Buffer.from("v2\n"))), "fetched past the newest three");
});

// A plain `wrangler deploy` after a wrangler dev of another tree, or with no
// --previous run at all, deploys nothing that run left behind: it fails.
test("a deploy's build without a --previous run for this build fails, and never ships what a dev run left", async () => {
  const o = out();
  await build({ web: web("/* a branch's app.js */\n"), out: o, dev: true });
  await assert.rejects(build({ web: web("main\n"), out: o }), /--previous/);
  const { fetchFn } = liveOf(["v4\n"]);
  await build({ web: web("v5\n"), out: o, previous: other, otherOrigin: other, fetchFn });
  // A dev run of a branch, then the deploy's [build] of the tree --previous ran for.
  await build({ web: web("/* a branch's app.js */\n"), out: o, dev: true });
  const r = await build({ web: web("v5\n"), out: o, otherOrigin: other });
  assert.deepEqual(r.kept, [lineOf("v4\n")]);
  assert.deepEqual(scripts(o), namesIn(lineOf("v4\n"), lineOf("v5\n")));
  // A tree --previous did not run for.
  await assert.rejects(build({ web: web("v6\n"), out: o, otherOrigin: other }), /run --previous again/);
  // --first-deploy keeps nothing, and says so for the [build] after it.
  const f = out();
  assert.deepEqual((await build({ web: web("v5\n"), out: f, firstDeploy: true })).kept, []);
  assert.deepEqual((await build({ web: web("v5\n"), out: f })).kept, []);
});

// The deploy that adds a script at the end of build.json's list finds builds
// made before it, without that script: they are kept, as they were.
test("a live build from before a script was added is kept, with only its own scripts", async () => {
  const before = lineOf("v4\n", LIST.length - 1);
  const { fetchFn } = live({ "/assets/builds.txt": before + "\n", ...filesOf("v4\n", LIST.length - 1) });
  const o = out();
  const r = await build({ web: web("v5\n"), out: o, previous: other, otherOrigin: other, fetchFn });
  assert.deepEqual(r.kept, [before]);
  assert.deepEqual(readFileSync(join(o, "assets", "builds.txt"), "utf8").trim().split("\n"), [lineOf("v5\n"), before]);
  assert.deepEqual(scripts(o), namesIn(lineOf("v5\n"), before));
});

// A build that adds two or more scripts at once leaves every live build
// that many short: any first part of the list is a build of its own, down to
// the first script alone.
test("a live build from before several scripts were added is kept, down to the first alone", async () => {
  const short = lineOf("v4\n", LIST.length - 2);
  const first = name(LIST[0], Buffer.from("first0()\n"));
  const { fetchFn } = live({ "/assets/builds.txt": `${short}\n${first}\n`, ...filesOf("v4\n", LIST.length - 2), ["/assets/" + first]: "first0()\n" });
  const o = out();
  const r = await build({ web: web("v5\n"), out: o, previous: other, otherOrigin: other, fetchFn });
  assert.deepEqual(r.kept, [short, first]);
  assert.deepEqual(readFileSync(join(o, "assets", "builds.txt"), "utf8").trim().split("\n"), [lineOf("v5\n"), short, first]);
  assert.deepEqual(scripts(o), namesIn(lineOf("v5\n"), short, first));
});

// Fewer scripts is one build only as the first part of the list, in order:
// not a later script without the ones before it, not a name the list does
// not have, and not an empty name.
test("a shorter live line that is not the first part of the list fails the deploy", async () => {
  assert.ok(LIST.length >= 3, "these lines need three scripts");
  const [s0, s1, s2] = LIST.map((s) => nm(s, "v4\n"));
  const extra = name("extra", Buffer.from("x\n"));
  for (const line of [s1, `${s1} ${s2}`, `${s0} ${s2}`, `${s2} ${s1} ${s0}`, extra, `${s0} ${extra}`, `${s0} `, ` ${s0}`]) {
    const { fetchFn } = live({ "/assets/builds.txt": line + "\n", ...filesOf("v4\n"), ["/assets/" + extra]: "x\n" });
    await assert.rejects(build({ web: web(), out: out(), previous: other, otherOrigin: other, fetchFn }), NOT_ONE, JSON.stringify(line));
  }
});

// The list is build.json's, whatever it holds: with a script more at its end
// the page is built with it too, and a live build of the list before it, one
// short now, is kept.
test("a list with a script added at its end builds, and keeps the builds from before it", async () => {
  const list = [...LIST, "extra"];
  const spec = JSON.stringify({ ...JSON.parse(SPEC), scripts: list });
  const o = out();
  const before = lineOf("v4\n");
  const { fetchFn } = live({ "/assets/builds.txt": before + "\n", ...filesOf("v4\n") });
  const r = await build({ web: web("v5\n", list, spec), out: o, previous: other, otherOrigin: other, fetchFn });
  assert.deepEqual(r.kept, [before]);
  const now = lineOf("v5\n", list.length, list);
  assert.deepEqual(readFileSync(join(o, "assets", "builds.txt"), "utf8").trim().split("\n"), [now, before]);
  assert.deepEqual(scripts(o), namesIn(now, before));
  const html = readFileSync(join(o, "index.html"), "utf8");
  assert.ok(html.includes(`/assets/${nm("extra")}"`) && !html.includes("data-build"));
});

test("a kept file that was changed fails the deploy", async () => {
  const o = out();
  const { fetchFn } = liveOf(["v4\n"]);
  await build({ web: web("v5\n"), out: o, previous: other, otherOrigin: other, fetchFn });
  writeFileSync(join(o + ".kept", "assets", name("app", Buffer.from("v4\n"))), "tampered\n");
  await assert.rejects(build({ web: web("v5\n"), out: o, otherOrigin: other }), /not the one its name says/);
});

test("--previous reads only the live Worker, unless --other-origin", async () => {
  const { fetchFn, asked } = liveOf(["v4\n"]);
  await assert.rejects(build({ web: web("v5\n"), out: out(), previous: other, fetchFn }), /--other-origin/);
  assert.deepEqual(asked, []);
  const r = await build({ web: web("v5\n"), out: out(), previous: LIVE, fetchFn });
  assert.deepEqual(r.kept, [lineOf("v4\n")]);
  await assert.rejects(build({ web: web(), out: out(), previous: "http://live.example", otherOrigin: "http://live.example", fetchFn }), /https/);
});

test("a live list that is not build.json's scripts in order fails the deploy", async () => {
  const evil = Buffer.from("document.title='pwned'\n");
  const [s0, , s2] = LIST.map((s) => nm(s, "v4\n"));
  const e1 = name(LIST[1], evil);
  for (const line of [name("x", evil), `${e1} ${s0}`, `${s0} ${e1} ${e1}`, `${s0}  ${e1}`, `${s0} ${s2}`, `${s0} ${e1} ${s2} ${s2}`, "../../etc/passwd"]) {
    const { fetchFn } = live({ "/assets/builds.txt": line + "\n", ["/assets/" + name("x", evil)]: evil, ["/assets/" + e1]: evil, ["/assets/" + s0]: src(LIST[0]) });
    await assert.rejects(build({ web: web(), out: out(), previous: other, otherOrigin: other, fetchFn }), NOT_ONE, line);
  }
});

test("a live script whose content is not its name's fails the deploy", async () => {
  const { fetchFn } = live({ "/assets/builds.txt": lineOf("v4\n") + "\n", ...filesOf("v4\n"), ["/assets/" + nm("app", "v4\n")]: "something else\n" });
  await assert.rejects(build({ web: web("v5\n"), out: out(), previous: other, otherOrigin: other, fetchFn }), /not the one its name says/);
});

test("a live script past its cap, or builds past the total, fail the deploy", async () => {
  const big = "x".repeat(MAX_SCRIPT + 1);
  const one = live({ "/assets/builds.txt": lineOf(big) + "\n", ...filesOf(big) });
  await assert.rejects(build({ web: web(), out: out(), previous: other, otherOrigin: other, fetchFn: one.fetchFn }), /more than/);
  // Two builds, each script under its cap, together past the total: two
  // scripts of each as large as a script may be, the rest as they are.
  const [a1, b1, a2, b2] = ["h", "a", "g", "b"].map((c) => c.repeat(MAX_SCRIPT - 1));
  const files = {}, lines = [];
  for (const [x, y] of [[a1, b1], [a2, b2]]) {
    const parts = LIST.map((s, i) => (i === 0 ? x : i === 1 ? y : src(s)));
    lines.push(LIST.map((s, i) => name(s, Buffer.from(parts[i]))).join(" "));
    LIST.forEach((s, i) => { files["/assets/" + name(s, Buffer.from(parts[i]))] = parts[i]; });
  }
  files["/assets/builds.txt"] = lines.join("\n") + "\n";
  assert.ok(4 * (MAX_SCRIPT - 1) > MAX_TOTAL);
  await assert.rejects(build({ web: web(), out: out(), previous: other, otherOrigin: other, fetchFn: live(files).fetchFn }), /builds are more than/);
});

test("a deploy that cannot read the live builds fails, unless it is the first", async () => {
  const none = live({});
  await assert.rejects(build({ web: web(), out: out(), previous: other, otherOrigin: other, fetchFn: none.fetchFn }), /--first-deploy/);
  const r = await build({ web: web(), out: out(), previous: other, otherOrigin: other, firstDeploy: true, fetchFn: none.fetchFn });
  assert.deepEqual(r.kept, []);
});

test("each run empties ./public but _headers, so a file no longer built is no longer deployed", async () => {
  const o = out();
  writeFileSync(join(o, "old.html"), "x");
  mkdirSync(join(o, "gone"));
  await build({ web: web(), out: o, dev: true });
  assert.ok(!existsSync(join(o, "old.html")) && !existsSync(join(o, "gone")) && existsSync(join(o, "_headers")));
  // Never a directory it did not make.
  const d = mkdtempSync(join(tmpdir(), "not-"));
  writeFileSync(join(d, "keep.txt"), "x");
  await assert.rejects(build({ web: web(), out: d, dev: true }), /not a directory this build made/);
  assert.ok(existsSync(join(d, "keep.txt")));
});

// Builds kept from another Worker (a staging one) are deployed only by a
// deploy that names that origin itself: a production deploy of the same tree
// refuses them, and the --previous run's --other-origin is never carried over.
test("a deploy refuses builds kept from an origin it does not name itself", async () => {
  const o = out();
  const { fetchFn } = liveOf(["v4\n"]);
  await build({ web: web("v5\n"), out: o, previous: other, otherOrigin: other, fetchFn });
  await assert.rejects(build({ web: web("v5\n"), out: o }), /kept from https:\/\/live\.example.*--other-origin https:\/\/live\.example/);
  await assert.rejects(build({ web: web("v5\n"), out: o, otherOrigin: "https://elsewhere.example" }), /kept from/);
  assert.deepEqual((await build({ web: web("v5\n"), out: o, otherOrigin: other })).kept, [lineOf("v4\n")]);
  // And builds kept from the live Worker are not a deploy of another's.
  const p = out();
  await build({ web: web("v5\n"), out: p, previous: LIVE, fetchFn });
  await assert.rejects(build({ web: web("v5\n"), out: p, otherOrigin: other }), /kept from https:\/\/live\.reminal\.app/);
});

// Only --dev, or wrangler's own dev, builds the page alone: an environment
// variable a deploy inherits does not, and wrangler sets WRANGLER_COMMAND over
// any value in it (deploy for a deploy).
test("only --dev or wrangler's dev builds the page alone", () => {
  const cli = new URL("./build-viewer.mjs", import.meta.url).pathname;
  const run = (args, env) => spawnSync(process.execPath, [cli, "--web", web(), "--out", out(), ...args], { env: { ...process.env, ...env }, encoding: "utf8" });
  assert.equal(run(["--dev"], {}).status, 0);
  assert.equal(run([], { WRANGLER_COMMAND: "dev" }).status, 0);
  for (const env of [{}, { WRANGLER_COMMAND: "deploy" }, { DEV: "1" }]) {
    const r = run([], env);
    assert.notEqual(r.status, 0, JSON.stringify(env));
    assert.match(r.stderr, /no builds were kept/);
  }
});
