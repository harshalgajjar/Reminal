import { SessionRoom } from "./session";
import { RendezvousRoom } from "./rendezvous";

export { SessionRoom, RendezvousRoom };

export interface Env {
  SESSION: DurableObjectNamespace;
  RENDEZVOUS: DurableObjectNamespace;
  ASSETS: Fetcher;
  // CRITICAL_MIN is the version below which reminal clients FORCE an upgrade
  // (set in wrangler.toml [vars] when shipping a security/critical fix; empty =
  // nothing forced). Served at /version so clients pick it up on their next
  // ≤24h check without anyone running `--force`.
  CRITICAL_MIN?: string;
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
      const sessionId = hostSub[1].toUpperCase();
      const id = env.SESSION.idFromName(sessionId);
      const stub = env.SESSION.get(id);
      const doUrl = new URL(request.url);
      doUrl.pathname = `/p/${sessionId}${url.pathname === "/" ? "/" : url.pathname}`;
      const hdrs = new Headers(request.headers);
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
      const id = env.SESSION.idFromName(sessionId);
      const stub = env.SESSION.get(id);
      const doUrl = new URL(request.url);
      doUrl.pathname = `/p/${sessionId}${rest}`;
      // The Host header does not survive the DO fetch, so carry the real public
      // host in a private header for the DO to forward to the agent's backend.
      const hdrs = new Headers(request.headers);
      hdrs.set("x-reminal-public-host", hostHeader);
      return stub.fetch(new Request(new Request(doUrl.toString(), request), { headers: hdrs }));
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
