import { describe, it } from "node:test";
import assert from "node:assert/strict";
import worker, { isSessionJoin, liveJoinURL } from "./index.js";

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
