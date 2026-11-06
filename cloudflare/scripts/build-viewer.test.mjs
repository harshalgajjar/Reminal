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

// web: a viewer's sources, app.js as given.
function web(app = "console.log('one')\n") {
  const d = mkdtempSync(join(tmpdir(), "web-"));
  writeFileSync(join(d, "build.json"), SPEC);
  writeFileSync(join(d, "index.html"), '<!doctype html>\n<script data-build="header"></script>\n<script data-build="pick"></script>\n<script data-build="app"></script>\n<script data-build="moments"></script>\n');
  writeFileSync(join(d, "header.js"), "fit()\n");
  writeFileSync(join(d, "moments.js"), MO);
  writeFileSync(join(d, "app.js"), app);
  writeFileSync(join(d, "sw.js"), "// sw\n");
  writeFileSync(join(d, "reload.js"), "// guard\n");
  // The dropdown (pick.js), a script of the viewer's.
  writeFileSync(join(d, "pick.js"), PK);
  writeFileSync(join(d, "manifest.webmanifest"), "{}");
  for (const sub of ["icons", "vendor"]) { mkdirSync(join(d, sub)); writeFileSync(join(d, sub, "x"), "x"); }
  return d;
}
// out: a new ./public, as the tree has it (_headers alone).
const out = () => { const d = join(mkdtempSync(join(tmpdir(), "cf-")), "public"); mkdirSync(d); writeFileSync(join(d, "_headers"), "/*\n"); return d; };
const MO = "mo()\n";
const PK = "// pick\n";
const lineOf = (app) => [name("header", Buffer.from("fit()\n")), name("app", Buffer.from(app)), name("moments", Buffer.from(MO)), name("pick", Buffer.from(PK))].join(" ");
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
  const files = { "/assets/builds.txt": apps.map(lineOf).join("\n") + "\n", ...extra };
  for (const a of apps) {
    files["/assets/" + name("header", Buffer.from("fit()\n"))] = "fit()\n";
    files["/assets/" + name("app", Buffer.from(a))] = a;
    files["/assets/" + name("moments", Buffer.from(MO))] = MO;
    files["/assets/" + name("pick", Buffer.from(PK))] = PK;
  }
  return live(files);
}
const scripts = (o) => readdirSync(join(o, "assets")).filter((f) => f.endsWith(".js")).sort();

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
  writeFileSync(join(w, "index.html"), Buffer.concat([Buffer.from([0xef, 0xbb, 0xbf]), Buffer.from("<html>"), Buffer.from([0xe9, 0xff]), Buffer.from('\r\n<script data-build="header"></script>\r\n<script data-build="pick"></script>\r\n<script data-build="app"></script>\r\n<script data-build="moments"></script>\r\n')]));
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
    ['"header", "app"', '"Header", "app"'], ['"sw.js"', '"../sw.js"'], ['"sw.js"', '"index.html"'], ['"sw.js"', '"assets/x.js"'],
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
  assert.deepEqual(scripts(o), [name("app", Buffer.from("v4\n")), name("app", Buffer.from("v5\n")), name("header", Buffer.from("fit()\n")), name("moments", Buffer.from(MO)), name("pick", Buffer.from(PK))].sort());
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
  const h = name("header", Buffer.from("fit()\n")), a = name("app", Buffer.from("v4\n")), m = name("moments", Buffer.from(MO));
  const { fetchFn } = live({ "/assets/builds.txt": `${h} ${a} ${m}\n`, ["/assets/" + h]: "fit()\n", ["/assets/" + a]: "v4\n", ["/assets/" + m]: MO });
  const o = out();
  const r = await build({ web: web("v5\n"), out: o, previous: other, otherOrigin: other, fetchFn });
  assert.deepEqual(r.kept, [`${h} ${a} ${m}`]);
  assert.deepEqual(readFileSync(join(o, "assets", "builds.txt"), "utf8").trim().split("\n"), [lineOf("v5\n"), `${h} ${a} ${m}`]);
  assert.deepEqual(scripts(o), [a, name("app", Buffer.from("v5\n")), h, m, name("pick", Buffer.from(PK))].sort());
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
  const h = name("header", Buffer.from("fit()\n"));
  const m = name("moments", Buffer.from(MO));
  for (const line of [name("x", evil), `${name("app", evil)} ${h}`, `${h} ${name("app", evil)} ${name("app", evil)}`, `${h}  ${name("app", evil)}`, `${h} ${m}`, `${h} ${name("app", evil)} ${m} ${m}`, "../../etc/passwd"]) {
    const { fetchFn } = live({ "/assets/builds.txt": line + "\n", ["/assets/" + name("x", evil)]: evil, ["/assets/" + name("app", evil)]: evil, ["/assets/" + h]: "fit()\n" });
    await assert.rejects(build({ web: web(), out: out(), previous: other, otherOrigin: other, fetchFn }), /not one build of header app moments pick/, line);
  }
});

test("a live script whose content is not its name's fails the deploy", async () => {
  const p = "/assets/" + name("app", Buffer.from("v4\n"));
  const h = "/assets/" + name("header", Buffer.from("fit()\n"));
  const { fetchFn } = live({ "/assets/builds.txt": lineOf("v4\n") + "\n", [h]: "fit()\n", [p]: "something else\n" });
  await assert.rejects(build({ web: web("v5\n"), out: out(), previous: other, otherOrigin: other, fetchFn }), /not the one its name says/);
});

test("a live script past its cap, or builds past the total, fail the deploy", async () => {
  const h = name("header", Buffer.from("fit()\n"));
  const big = "x".repeat(MAX_SCRIPT + 1);
  const n = name("app", Buffer.from(big)), m = name("moments", Buffer.from(MO));
  const one = live({ "/assets/builds.txt": `${h} ${n} ${m}\n`, ["/assets/" + h]: "fit()\n", ["/assets/" + n]: big, ["/assets/" + m]: MO });
  await assert.rejects(build({ web: web(), out: out(), previous: other, otherOrigin: other, fetchFn: one.fetchFn }), /more than/);
  // Two builds, each script under its cap, together past the total.
  const [h1, a1, h2, a2] = ["h", "a", "g", "b"].map((c) => c.repeat(MAX_SCRIPT - 1));
  const files = { "/assets/builds.txt": `${name("header", Buffer.from(h1))} ${name("app", Buffer.from(a1))} ${m}\n${name("header", Buffer.from(h2))} ${name("app", Buffer.from(a2))} ${m}\n`, ["/assets/" + m]: MO };
  for (const [sc, v] of [["header", h1], ["app", a1], ["header", h2], ["app", a2]]) files["/assets/" + name(sc, Buffer.from(v))] = v;
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
