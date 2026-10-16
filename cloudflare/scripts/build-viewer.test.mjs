// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// build-viewer.mjs's own rules (run by cloudflare/viewer_build_test.go).
import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, existsSync, readdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { build, name, sri, readSpec, MAX_SCRIPT } from "./build-viewer.mjs";

const SPEC = readFileSync(new URL("../../internal/client/web/build.json", import.meta.url), "utf8");

// web: a viewer's sources, app.js as given.
function web(app = "console.log('one')\n") {
  const d = mkdtempSync(join(tmpdir(), "web-"));
  writeFileSync(join(d, "build.json"), SPEC);
  writeFileSync(join(d, "index.html"), '<!doctype html>\n<script data-build="header"></script>\n<script data-build="app"></script>\n');
  writeFileSync(join(d, "header.js"), "fit()\n");
  writeFileSync(join(d, "app.js"), app);
  writeFileSync(join(d, "sw.js"), "// sw\n");
  writeFileSync(join(d, "manifest.webmanifest"), "{}");
  for (const sub of ["icons", "vendor"]) { mkdirSync(join(d, sub)); writeFileSync(join(d, sub, "x"), "x"); }
  return d;
}
const out = () => mkdtempSync(join(tmpdir(), "out-"));
const lineOf = (app) => [name("header", Buffer.from("fit()\n")), name("app", Buffer.from(app))].join(" ");

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
  }
  return live(files);
}

test("each script is named by its content and its tag stamped with its SRI", async () => {
  const o = out();
  const r = await build({ web: web(), out: o });
  const html = readFileSync(join(o, "index.html"), "utf8");
  const js = Buffer.from("console.log('one')\n");
  assert.ok(html.includes(`<script src="/assets/${name("app", js)}" integrity="${sri(js)}" crossorigin="anonymous"></script>`));
  assert.ok(!html.includes("data-build"));
  assert.equal(readFileSync(join(o, "assets", name("app", js)), "utf8"), "console.log('one')\n");
  assert.equal(readFileSync(join(o, "assets", "builds.txt"), "utf8"), r.build + "\n");
  assert.ok(existsSync(join(o, "sw.js")) && existsSync(join(o, "icons", "x")) && existsSync(join(o, "vendor", "x")));
});

test("a rule this build does not know is refused", () => {
  for (const [from, to] of [['"sha384"', '"sha256"'], ["{sha256:16}", "{sha256:12}"], ['"keep": 3', '"keep": 0']]) {
    assert.throws(() => readSpec(SPEC.replace(from, to)), undefined, to);
  }
});

test("a live Worker's newest builds are kept, at most keep, each checked against its name", async () => {
  const o = out();
  const { fetchFn } = liveOf(["v4\n", "v3\n", "v2\n", "v1\n"]);
  const r = await build({ web: web("v5\n"), out: o, previous: "https://live.example", fetchFn });
  // keep 3: this one and the live list's two newest — never v2 nor v1.
  assert.deepEqual(r.kept, [lineOf("v4\n"), lineOf("v3\n")]);
  const listed = readFileSync(join(o, "assets", "builds.txt"), "utf8").trim().split("\n");
  assert.deepEqual(listed, [lineOf("v5\n"), lineOf("v4\n"), lineOf("v3\n")]);
  const scripts = readdirSync(join(o, "assets")).filter((f) => f.endsWith(".js")).sort();
  assert.deepEqual(scripts, [...new Set(listed.join(" ").split(" "))].sort());
  // A later run (wrangler's [build]) keeps what this one fetched.
  const again = await build({ web: web("v5\n"), out: o });
  assert.deepEqual(again.kept, [lineOf("v4\n"), lineOf("v3\n")]);
});

test("the live list's newest three only: a live list of the same build and older ones", async () => {
  const { fetchFn, asked } = liveOf(["v5\n", "v4\n", "v3\n", "v2\n"]);
  const r = await build({ web: web("v5\n"), out: out(), previous: "https://live.example", fetchFn });
  assert.deepEqual(r.kept, [lineOf("v4\n"), lineOf("v3\n")]);
  assert.ok(!asked.includes("/assets/" + name("app", Buffer.from("v2\n"))), "fetched past the newest three");
});

test("a live script whose content is not its name's fails the deploy", async () => {
  const p = "/assets/" + name("app", Buffer.from("v4\n"));
  const h = "/assets/" + name("header", Buffer.from("fit()\n"));
  const { fetchFn } = live({ "/assets/builds.txt": lineOf("v4\n") + "\n", [h]: "fit()\n", [p]: "something else\n" });
  await assert.rejects(build({ web: web("v5\n"), out: out(), previous: "https://live.example", fetchFn }), /not the one its name says/);
});

test("a live script past the cap, or a list naming what is not a script, fails the deploy", async () => {
  const big = "x".repeat(MAX_SCRIPT + 1);
  const n = name("app", Buffer.from(big));
  const { fetchFn } = live({ "/assets/builds.txt": n + "\n", ["/assets/" + n]: big });
  await assert.rejects(build({ web: web(), out: out(), previous: "https://live.example", fetchFn }), /more than/);
  const odd = live({ "/assets/builds.txt": "../../etc/passwd\n" });
  await assert.rejects(build({ web: web(), out: out(), previous: "https://live.example", fetchFn: odd.fetchFn }), /not a script/);
});

test("a deploy that cannot read the live builds fails, unless it is the first", async () => {
  const none = live({});
  await assert.rejects(build({ web: web(), out: out(), previous: "https://live.example", fetchFn: none.fetchFn }), /--first-deploy/);
  const r = await build({ web: web(), out: out(), previous: "https://live.example", firstDeploy: true, fetchFn: none.fetchFn });
  assert.deepEqual(r.kept, []);
  await assert.rejects(build({ web: web(), out: out(), previous: "http://live.example", fetchFn: none.fetchFn }), /https/);
});

test("a kept build whose file was changed is dropped, never served", async () => {
  const o = out();
  const { fetchFn } = liveOf(["v4\n"]);
  await build({ web: web("v5\n"), out: o, previous: "https://live.example", fetchFn });
  writeFileSync(join(o, "assets", name("app", Buffer.from("v4\n"))), "tampered\n");
  const r = await build({ web: web("v5\n"), out: o });
  assert.deepEqual(r.kept, []);
  assert.ok(!existsSync(join(o, "assets", name("app", Buffer.from("v4\n")))));
});
