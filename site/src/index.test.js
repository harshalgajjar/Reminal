import { describe, it } from "node:test";
import assert from "node:assert/strict";
import worker, { isSessionJoin, liveJoinURL, downloadKey } from "./index.js";

function url(href) {
  return new URL(href);
}

describe("isSessionJoin", () => {
  it("treats /?s= on the marketing host as a join URL", () => {
    const yes = [
      "https://reminal.app/?s=VW65K9YU",
      "https://reminal.app/?s=",
      "https://www.reminal.app/?s=abc&foo=1",
    ];
    for (const href of yes) {
      assert.equal(isSessionJoin(url(href)), true, href);
    }
  });

  it("leaves the marketing site alone", () => {
    const no = [
      "https://reminal.app/",
      "https://reminal.app/?utm_source=x",
      "https://reminal.app/agents/",
      "https://reminal.app/agents/?s=VW65K9YU",
      "https://reminal.app/install.sh",
      "https://reminal.app/privacy/",
      "https://reminal.app/ping",
    ];
    for (const href of no) {
      assert.equal(isSessionJoin(url(href)), false, href);
    }
  });
});

describe("liveJoinURL", () => {
  it("moves the query onto live.reminal.app", () => {
    assert.equal(
      liveJoinURL(url("https://reminal.app/?s=ABC12345")),
      "https://live.reminal.app/?s=ABC12345",
    );
  });
});

describe("fetch", () => {
  const env = {
    ASSETS: {
      fetch() {
        return new Response("site", { status: 200 });
      },
    },
  };

  it("sends reminal.app/?s= to the live viewer", async () => {
    const res = await worker.fetch(new Request("https://reminal.app/?s=ABC12345"), env);
    assert.equal(res.status, 302);
    assert.equal(res.headers.get("location"), "https://live.reminal.app/?s=ABC12345");
  });

  it("sends www.reminal.app/?s= straight to the live viewer", async () => {
    const res = await worker.fetch(new Request("https://www.reminal.app/?s=ABC12345"), env);
    assert.equal(res.status, 302);
    assert.equal(res.headers.get("location"), "https://live.reminal.app/?s=ABC12345");
  });

  it("keeps the homepage on the marketing site", async () => {
    const res = await worker.fetch(new Request("https://reminal.app/"), env);
    assert.equal(await res.text(), "site");
  });

  it("still folds aliases that are not join URLs", async () => {
    const res = await worker.fetch(new Request("https://www.reminal.app/agents"), env);
    assert.equal(res.status, 301);
    assert.equal(res.headers.get("location"), "https://reminal.app/agents");
  });
});

// A bucket that holds what the test gives it, answering like R2 does.
function bucket(files) {
  const obj = (key) =>
    key in files ? { body: files[key], size: files[key].length, httpEtag: `"${key.length}"` } : null;
  return { get: async (key) => obj(key), head: async (key) => obj(key) };
}

async function get(path, files, method = "GET") {
  return worker.fetch(new Request(`https://reminal.app${path}`, { method }), {
    DOWNLOADS: bucket(files),
    ASSETS: { fetch: async () => new Response("an asset", { status: 200 }) },
  });
}

describe("downloads", () => {
  const files = {
    "nightly/releases.json": '{"channel":"nightly"}',
    "nightly/reminal_9.9.9_linux_amd64.tar.gz": "a build",
    "nightly/install.sh": "#!/bin/sh",
  };

  it("serves a file by its name, never to be indexed", async () => {
    const r = await get("/downloads/nightly/reminal_9.9.9_linux_amd64.tar.gz", files);
    assert.equal(r.status, 200);
    assert.equal(await r.text(), "a build");
    assert.equal(r.headers.get("x-robots-tag"), "noindex, nofollow");
    assert.equal(r.headers.get("content-type"), "application/gzip");
    assert.match(r.headers.get("cache-control"), /immutable/);
  });

  it("serves a manifest and a script fresh every time", async () => {
    for (const [path, type] of [["/downloads/nightly/releases.json", /json/], ["/downloads/nightly/install.sh", /text\/plain/]]) {
      const r = await get(path, files);
      assert.equal(r.status, 200, path);
      assert.match(r.headers.get("content-type"), type, path);
      assert.equal(r.headers.get("cache-control"), "no-cache", path);
      assert.equal(r.headers.get("x-robots-tag"), "noindex, nofollow", path);
    }
  });

  it("answers HEAD without a body", async () => {
    const r = await get("/downloads/nightly/releases.json", files, "HEAD");
    assert.equal(r.status, 200);
    assert.equal(await r.text(), "");
    assert.equal(r.headers.get("content-length"), String(files["nightly/releases.json"].length));
  });

  it("lists nothing and invents nothing: folders and unknown names are a plain 404", async () => {
    for (const path of ["/downloads/", "/downloads/nightly/", "/downloads/nightly", "/downloads/nightly/nope.tar.gz"]) {
      const r = await get(path, files);
      assert.equal(r.status, 404, path);
      assert.equal(r.headers.get("x-robots-tag"), "noindex, nofollow", path);
    }
  });

  it("never reaches outside a key", () => {
    for (const bad of ["/downloads/../secrets", "/downloads/nightly/../x", "/downloads/nightly//x", "/downloads/%2e%2e/x", "/downloads/a b", "/downloads/%ZZ"]) {
      assert.equal(downloadKey(bad), "", bad);
    }
    assert.equal(downloadKey("/agents/"), null);
    assert.equal(downloadKey("/downloads/nightly/releases.json"), "nightly/releases.json");
  });

  it("only reads", async () => {
    const r = await get("/downloads/nightly/releases.json", files, "POST");
    assert.equal(r.status, 405);
  });

  it("leaves the rest of the site as it was", async () => {
    const r = await get("/agents/", files);
    assert.equal(await r.text(), "an asset");
  });
});
