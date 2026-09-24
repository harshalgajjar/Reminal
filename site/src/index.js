// The marketing site for reminal.
//
// Deliberately its own Worker, separate from reminal-relay: the relay carries
// live sessions at live.reminal.app, and a copy tweak here should never be
// able to take that down.
//
// Everything is static except a few things this handles by hand:
//
//   * short install URLs — `curl -fsSL https://reminal.app/install.sh | sh` is
//     what goes on the site, in posts, and in people's shell history, so it
//     redirects to the script on main rather than pinning a copy that quietly
//     goes stale;
//   * one canonical host — reminal.dev and any www. variant fold into
//     reminal.app so links, OG cards and analytics don't fragment.
//   * stray `/?s=` on this host — someone typing the marketing domain with a
//     session id is sent to live.reminal.app, which is the real viewer.
//   * /downloads/ — files kept in the DOWNLOADS bucket rather than on the
//     public releases page (see serveDownload).

const RAW = "https://raw.githubusercontent.com/harshalgajjar/Reminal/main";
const REPO = "https://github.com/harshalgajjar/Reminal";

const CANONICAL_HOST = "reminal.app";
const LIVE_ORIGIN = "https://live.reminal.app";

// Hosts we own and fold into the canonical one. workers.dev and localhost are
// absent on purpose — that's where the thing gets tested before DNS exists.
const ALIASES = new Set([
  "reminal.dev",
  "www.reminal.dev",
  "www.reminal.app",
]);

const REDIRECTS = new Map([
  ["/install.sh", `${RAW}/install.sh`],
  ["/install.ps1", `${RAW}/install.ps1`],
  ["/github", REPO],
  ["/repo", REPO],
  ["/security", `${REPO}/blob/main/SECURITY.md`],
  ["/licensing", `${REPO}/blob/main/LICENSING.md`],
  ["/releases", `${REPO}/releases`],
]);

// Every page that lives at a directory index. Each answers 200 only with its
// trailing slash, and ASSETS bounces the bare form there with a 307 — a
// *temporary* redirect, which tells a crawler the no-slash URL is the real one
// and to keep asking. Two URLs then compete for one page and neither
// accumulates the other's signal. Listing the routes explicitly (rather than
// slash-anything-without-a-dot) keeps a typo a clean 404 instead of a redirect
// into one.
const PAGES = new Set([
  "/agents",
  "/privacy",
  "/terms",
  "/guides/macbook-lid-closed",
  "/guides/claude-code-from-phone",
  "/guides/run-agents-overnight",
  "/guides/agents-different-machines",
  "/guides/review-agent-work",
]);

// A join URL typed on the marketing host. Path must stay `/` so /agents/?s=
// cannot steal a page. The real viewer is live.reminal.app.
export function isSessionJoin(url) {
  return url.pathname === "/" && url.searchParams.has("s");
}

export function liveJoinURL(url) {
  return LIVE_ORIGIN + "/" + url.search + url.hash;
}

// Downloads: files kept in the DOWNLOADS bucket, one key per file, at
// /downloads/<key>. Never indexed and never listed — what is here is fetched
// by the software that knows its name, not found by a search engine — and a
// path that names no file is a plain 404, folders included.
//
// A build's name carries its version, so its bytes never change and it is
// cached for good. Everything else — a manifest, an install script — is
// fetched fresh every time, so a new release is seen the moment it lands.
const DOWNLOADS_PREFIX = "/downloads/";

// downloadKey turns a request path into a bucket key: "" for a path that is
// under /downloads/ but names no file it may serve, null for one that is not
// under /downloads/ at all.
export function downloadKey(pathname) {
  if (!pathname.startsWith(DOWNLOADS_PREFIX)) return null;
  let key;
  try {
    key = decodeURIComponent(pathname.slice(DOWNLOADS_PREFIX.length));
  } catch {
    return "";
  }
  if (!/^[A-Za-z0-9._/-]+$/.test(key)) return "";
  if (key.split("/").some((part) => part === "" || part === "." || part === "..")) return "";
  return key;
}

const DOWNLOAD_TYPES = [
  [/\.json$/, "application/json; charset=utf-8"],
  [/\.(sh|sha256|txt)$/, "text/plain; charset=utf-8"],
  [/\.(tar\.gz|tgz)$/, "application/gzip"],
  [/\.zip$/, "application/zip"],
];

export function downloadHeaders(key) {
  const h = new Headers({
    "x-robots-tag": "noindex, nofollow",
    "x-content-type-options": "nosniff",
  });
  const type = DOWNLOAD_TYPES.find(([re]) => re.test(key));
  h.set("content-type", type ? type[1] : "application/octet-stream");
  h.set("cache-control", /\.(tar\.gz|tgz|zip)$/.test(key) ? "public, max-age=31536000, immutable" : "no-cache");
  return h;
}

export async function serveDownload(request, env, key) {
  const notFound = () =>
    new Response("not found\n", { status: 404, headers: { "x-robots-tag": "noindex, nofollow", "content-type": "text/plain; charset=utf-8" } });
  if (request.method !== "GET" && request.method !== "HEAD") {
    return new Response("method not allowed\n", { status: 405, headers: { allow: "GET, HEAD" } });
  }
  if (!key || !env.DOWNLOADS) return notFound();
  const obj = request.method === "HEAD" ? await env.DOWNLOADS.head(key) : await env.DOWNLOADS.get(key);
  if (!obj) return notFound();
  const h = downloadHeaders(key);
  if (obj.httpEtag) h.set("etag", obj.httpEtag);
  if (typeof obj.size === "number") h.set("content-length", String(obj.size));
  return new Response(request.method === "HEAD" ? null : obj.body, { status: 200, headers: h });
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url);

    // Empty 204 the page times to show the visitor their own round-trip to
    // the nearest Cloudflare edge. Uncached and bodiless so the number is
    // network latency and nothing else.
    if (url.pathname === "/ping") {
      return new Response(null, {
        status: 204,
        headers: {
          "cache-control": "no-store, no-cache, must-revalidate",
          "access-control-allow-origin": "*",
          "timing-allow-origin": "*",
        },
      });
    }

    // Session joins leave this host before alias folding, so
    // www.reminal.app/?s=X goes straight to live.reminal.app/?s=X.
    if (isSessionJoin(url)) {
      return Response.redirect(liveJoinURL(url), 302);
    }

    if (ALIASES.has(url.hostname)) {
      url.hostname = CANONICAL_HOST;
      return Response.redirect(url.toString(), 301);
    }

    // Before the trailing-slash forgiveness below: a download is a file, and
    // a folder under /downloads/ is a 404, not a redirect to somewhere else.
    const key = downloadKey(url.pathname);
    if (key !== null) {
      return serveDownload(request, env, key);
    }

    // Trailing slashes are forgiven so /install.sh/ isn't a 404 someone has to
    // debug from a phone.
    const path = url.pathname.length > 1 ? url.pathname.replace(/\/+$/, "") : url.pathname;
    const target = REDIRECTS.get(path);
    if (target) {
      // 302, not 301: the install scripts move around, and a permanent
      // redirect cached in someone's shell is a bug you can't recall.
      return Response.redirect(target, 302);
    }

    // 308, not the 307 ASSETS would send: these page URLs are settled, and a
    // permanent redirect is what folds the bare form's signal into the slashed
    // one instead of leaving the pair split.
    if (PAGES.has(path) && url.pathname !== `${path}/`) {
      url.pathname = `${path}/`;
      return Response.redirect(url.toString(), 308);
    }

    return env.ASSETS.fetch(request);
  },
};
