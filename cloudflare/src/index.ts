import { SessionRoom } from "./session";
import { RendezvousRoom } from "./rendezvous";
import { handlePush, handlePushKey, handlePushToken, type PushEnv } from "./push";
import { RateLimiter, allow, inIsolate, type LimiterEnv } from "./limiter";
import { addressGroup } from "./session";
import { handleVoiceAsset, voiceAssetsTooMany, VOICE_ASSETS_PREFIX, type VoiceAssetsEnv } from "./voiceassets";

export { SessionRoom, RendezvousRoom, RateLimiter };

export interface Env extends PushEnv, LimiterEnv, VoiceAssetsEnv {
  SESSION: DurableObjectNamespace;
  RENDEZVOUS: DurableObjectNamespace;
  ASSETS: Fetcher;
  // CRITICAL_MIN is the version below which reminal clients FORCE an upgrade
  // (set in wrangler.toml [vars] when shipping a security/critical fix; empty =
  // nothing forced). Served at /version so clients pick it up on their next
  // ≤24h check without anyone running `--force`.
  CRITICAL_MIN?: string;
  // TUNNEL_SUFFIX is the base domain whose `port-<id>.` subdomains reach this
  // Worker. Set it only where those subdomains actually route here (production
  // has *.reminal.app), because it makes a forwarded app reachable ONLY on its
  // own origin: a request for /p/<id>/ arriving on any other host — under the
  // suffix or not, such as the workers.dev hostname the Worker also answers
  // on — is redirected there rather than served.
  //
  // Why that matters: the viewer keeps this origin's remembered sessions and
  // its owner key in localStorage and IndexedDB, and anything served from the
  // same origin can read them. A forwarded app is somebody's arbitrary HTML,
  // so it belongs on an origin of its own. Deployments without wildcard
  // subdomains (a workers.dev staging relay, `wrangler dev`) leave this empty
  // and keep serving at the path.
  TUNNEL_SUFFIX?: string;
}

// internalHeaders copies a request's headers with every x-reminal-* stripped.
// Those headers are OURS to set on the hop into the Durable Object (routing
// state like x-reminal-host-mode and x-reminal-public-host); a client must not
// be able to forge them. Spoofing host-mode on a /p/<id>/ request, for
// instance, would scope the PIN-gate cookie to "/" instead of "/p/<id>/" and
// leak it across tunnels sharing the relay origin.
function internalHeaders(src: Headers): Headers {
  const h = new Headers(src);
  for (const k of [...h.keys()]) {
    if (k.toLowerCase().startsWith("x-reminal-")) h.delete(k);
  }
  return h;
}

// What one caller (its address, an IPv6 one by its /64) may ask of the relay
// a minute, before any session's or rendezvous's object is touched — every
// name makes one: sockets opened (/ws, /rv), and a forwarded app's requests
// (/p, its own host: a page's assets come many at once). Counted where the
// count is the count (the RateLimiter), each isolate's own only where that
// cannot be asked.
// An address is not one machine: machines behind one NAT, or a hosting
// provider's shared egress, share it, and after a relay's restart all their
// agents come back at once. So the machines' own sockets (/ws agent, tunnel) are counted
// apart from the viewers' and copies', with room for many machines.
const SOCKETS_PER_MIN = 120;
const AGENT_SOCKETS_PER_MIN = 1200;
const TUNNEL_PER_MIN = 1200;
const socketAsks = new Map<string, number[]>();
const agentSocketAsks = new Map<string, number[]>();

function caller(request: Request): string {
  return addressGroup(request.headers.get("cf-connecting-ip") ?? "");
}

async function socketsWithin(env: Env, request: Request, machine: boolean): Promise<boolean> {
  const who = caller(request);
  const [scope, counts, perMin] = machine ? ["agent-sockets", agentSocketAsks, AGENT_SOCKETS_PER_MIN] as const : ["sockets", socketAsks, SOCKETS_PER_MIN] as const;
  const c = await allow(env, scope, who, perMin, 60);
  return c === true || (c === null && inIsolate(counts, who, perMin, 60));
}

// A forwarded app's requests are many (a page is its assets), and so are the
// voice panel's (its libraries and models, some read in ranges), so the
// limiter is not asked for each: this isolate counts every one, and the
// limiter is asked once for each batch of a caller's requests here (counting
// batches, perMin / batch of them a minute), its answer standing for the
// whole batch, those that come while it is asked included. A batch lasts no
// longer than the minute it was counted in.
function batchedWithin(scope: string, perMin: number, batch: number) {
  const asks = new Map<string, number[]>();
  const batches = new Map<string, { left: number; at: number; ok: Promise<boolean> }>();
  return async (env: Env, request: Request): Promise<boolean> => {
    const who = caller(request);
    if (!inIsolate(asks, who, perMin, 60)) return false;
    const now = Date.now();
    let b = batches.get(who);
    batches.delete(who);
    if (!b || b.left <= 0 || now - b.at >= 60_000) {
      b = { left: batch, at: now, ok: allow(env, scope, who, perMin / batch, 60).then((c) => c !== false) };
    }
    b.left--;
    // Most recently asked last: past 5000 callers, the oldest half goes.
    batches.set(who, b);
    if (batches.size > 5000) for (const k of [...batches.keys()].slice(0, 2500)) batches.delete(k);
    return b.ok;
  };
}
const TUNNEL_BATCH = 20;
const tunnelWithin = batchedWithin("tunnel", TUNNEL_PER_MIN, TUNNEL_BATCH);
// The voice panel's first load is a few dozen files; a page reloaded often,
// or many people behind one address, stay well inside this.
const VOICE_ASSETS_PER_MIN = 600;
const voiceAssetsWithin = batchedWithin("voice-assets", VOICE_ASSETS_PER_MIN, 20);

function tooMany(): Response {
  return new Response("too many requests — try again in a minute", { status: 429 });
}

// tunnelOrigin returns the origin a forward should be served from, or "" when
// this request is already there (or the deployment has no tunnel subdomains).
//
// A forwarded app is somebody's arbitrary HTML. Served at /p/<id>/ on the
// relay's own host it would share an origin with the viewer, which is where
// this browser keeps its remembered sessions and its owner key — so it is sent
// to an origin of its own instead. That holds for every host the Worker
// answers on, not only those under the suffix: the workers.dev hostname
// serves the viewer too. Exported for the tests.
export function tunnelOrigin(
  hostHeader: string,
  suffixVar: string | undefined,
  sessionId: string,
): string {
  const suffix = (suffixVar || "").trim().toLowerCase();
  if (!suffix) return ""; // no wildcard subdomains here (staging, wrangler dev)
  const host = (hostHeader || "").trim().toLowerCase().split(":")[0];
  const own = `https://port-${sessionId.toLowerCase()}.${suffix}`;
  if (!host.endsWith("." + suffix)) return own; // the suffix itself, or another host
  if (/^port-[a-z0-9]+$/i.test(host.slice(0, host.length - suffix.length - 1))) {
    return ""; // already on the forward's own origin
  }
  return own;
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);

    // Subdomain-per-tunnel: port-<id>.<domain> serves the forwarded app at the
    // ROOT of its own origin, so the app's absolute-path assets, service worker,
    // and same-origin/CORS all resolve (which a shared /p/<id>/ path prefix
    // breaks). Routed to the same SessionRoom DO as /p/<id>/, but tagged
    // host-mode via a header so the DO serves at root (no prefix rewriting).
    // Matches the id as the first label regardless of base domain, so it works
    // for reminal.app and for Host-spoofed tests. IDs are uppercase on the wire
    // elsewhere but hostnames are lowercased by browsers, so we re-uppercase.
    // Match on the Host header (equivalent to url.hostname in production, but
    // also correct behind a local `wrangler dev`, where url.hostname is
    // "localhost" while the Host header carries the real tunnel hostname).
    const hostHeader = request.headers.get("host") || url.hostname;
    const hostSub = hostHeader.match(/^port-([a-z0-9]+)\./i);
    if (hostSub) {
      if (!(await tunnelWithin(env, request))) return tooMany();
      const sessionId = hostSub[1].toUpperCase();
      const id = env.SESSION.idFromName(sessionId);
      const stub = env.SESSION.get(id);
      const doUrl = new URL(request.url);
      doUrl.pathname = `/p/${sessionId}${url.pathname === "/" ? "/" : url.pathname}`;
      const hdrs = internalHeaders(request.headers);
      hdrs.set("x-reminal-host-mode", "1");
      // The Host header does not survive the DO fetch, so carry the real public
      // host in a private header for the DO to forward to the agent's backend.
      hdrs.set("x-reminal-public-host", hostHeader);
      return stub.fetch(new Request(new Request(doUrl.toString(), request), { headers: hdrs }));
    }

    // Shell-session WS: /ws/<id>/agent | viewer | tunnel
    // tunnel is for port-forward agents (registered by `reminal expose`).
    const wsMatch = url.pathname.match(/^\/ws\/([A-Z0-9]+)\/(agent|viewer|tunnel)$/i);
    if (wsMatch) {
      const sessionId = wsMatch[1].toUpperCase();
      const role = wsMatch[2].toLowerCase();
      if (!(await socketsWithin(env, request, role !== "viewer"))) return tooMany();
      const id = env.SESSION.idFromName(sessionId);
      const stub = env.SESSION.get(id);
      const doUrl = new URL(request.url);
      doUrl.pathname = `/ws/${sessionId}/${role}`;
      return stub.fetch(new Request(doUrl.toString(), request));
    }

    // Copy/paste rendezvous WS: /rv/<code>/source | paste
    // Routed to a per-code RendezvousRoom DO that blindly pairs the two.
    const rvMatch = url.pathname.match(/^\/rv\/([A-Z0-9]+)\/(source|paste)$/i);
    if (rvMatch) {
      if (!(await socketsWithin(env, request, false))) return tooMany();
      const code = rvMatch[1].toUpperCase();
      const role = rvMatch[2].toLowerCase();
      const id = env.RENDEZVOUS.idFromName(code);
      const stub = env.RENDEZVOUS.get(id);
      const doUrl = new URL(request.url);
      doUrl.pathname = `/rv/${code}/${role}`;
      return stub.fetch(new Request(doUrl.toString(), request));
    }

    // Port-forward HTTP routes: /p/<id>/[__auth | rest-of-path]
    // Routed to the DO so it can talk to its tunnel WS + manage cookies.
    const portMatch = url.pathname.match(/^\/p\/([A-Z0-9]+)(\/.*)?$/i);
    if (portMatch) {
      const sessionId = portMatch[1].toUpperCase();
      const rest = portMatch[2] || "/";
      // Canonicalise the id's case BEFORE anything gets scoped to this path.
      // Routing here is case-insensitive, but cookie Path matching is not
      // (RFC 6265 5.1.4): a visitor arriving on /p/<lowercase>/ was handed a PIN
      // cookie scoped to /p/<UPPERCASE>/, so the cookie stopped applying to the
      // URL they were actually on and they were asked for the PIN again on every
      // fresh visit. Nothing reminal prints is lowercase (ids are generated from
      // an uppercase alphabet), but a hand-typed or hand-derived link is.
      //
      // WebSockets are left alone: they cannot follow redirects, lowercase
      // upgrades work today, and the DO uppercases the id for lookup anyway.
      // 308 rather than 301/302 so a POST to /p/<lower>/__auth keeps its method
      // and body. The Location is relative because the Host header, not
      // url.hostname, carries the real tunnel host behind `wrangler dev`.
      if (
        portMatch[1] !== sessionId &&
        (request.headers.get("upgrade") || "").toLowerCase() !== "websocket"
      ) {
        return new Response(null, {
          status: 308,
          headers: { Location: `/p/${sessionId}${rest}${url.search}` },
        });
      }
      // Send it to the forward's own origin where one exists. Serving it here
      // would put somebody's arbitrary HTML on the same origin as the viewer,
      // which is where this browser keeps its remembered sessions and its
      // owner key. A 308 keeps the method and body, so a POST to __auth is
      // unaffected, and links already in circulation still work.
      const ownOrigin = tunnelOrigin(hostHeader, env.TUNNEL_SUFFIX, sessionId);
      if (ownOrigin) {
        return new Response(null, {
          status: 308,
          headers: { Location: `${ownOrigin}${rest}${url.search}` },
        });
      }
      if (!(await tunnelWithin(env, request))) return tooMany();
      const id = env.SESSION.idFromName(sessionId);
      const stub = env.SESSION.get(id);
      const doUrl = new URL(request.url);
      doUrl.pathname = `/p/${sessionId}${rest}`;
      // The Host header does not survive the DO fetch, so carry the real public
      // host in a private header for the DO to forward to the agent's backend.
      const hdrs = internalHeaders(request.headers);
      hdrs.set("x-reminal-public-host", hostHeader);
      return stub.fetch(new Request(new Request(doUrl.toString(), request), { headers: hdrs }));
    }

    // Phone alerts: machines hand over end-to-end sealed alerts to be signed
    // and forwarded (see push.ts). Relay host only — never a tunnel host,
    // which returned above.
    if (url.pathname === "/push/key") return handlePushKey(env);
    if (url.pathname === "/push") return handlePush(request, env);
    if (url.pathname === "/push/token") return handlePushToken(request, env);

    // The voice panel's libraries and models, from our R2 bucket (see
    // voiceassets.ts). Relay host only, as above.
    if (url.pathname.startsWith(VOICE_ASSETS_PREFIX)) {
      if (!(await voiceAssetsWithin(env, request))) return voiceAssetsTooMany();
      return handleVoiceAsset(request, env);
    }

    // Version beacon: the online, maintainer-controlled critical-upgrade switch.
    // Clients fetch this during their ≤24h version check; if their version is
    // below critical_min they force-upgrade (see internal/updater). Short cache
    // so a newly-set critical_min propagates within minutes, not the 24h asset
    // cache. No secrets here — just the floor version.
    if (url.pathname === "/version") {
      return new Response(
        JSON.stringify({ critical_min: env.CRITICAL_MIN ?? "" }),
        {
          headers: {
            "content-type": "application/json",
            "cache-control": "public, max-age=300",
            "access-control-allow-origin": "*",
          },
        },
      );
    }

    return env.ASSETS.fetch(request);
  },
} satisfies ExportedHandler<Env>;
