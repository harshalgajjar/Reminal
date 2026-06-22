import { SessionRoom } from "./session";

export { SessionRoom };

export interface Env {
  SESSION: DurableObjectNamespace;
  ASSETS: Fetcher;
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);

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
      return stub.fetch(new Request(doUrl.toString(), request));
    }

    return env.ASSETS.fetch(request);
  },
} satisfies ExportedHandler<Env>;
