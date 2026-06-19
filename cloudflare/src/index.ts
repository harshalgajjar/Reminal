import { SessionRoom } from "./session";

export { SessionRoom };

export interface Env {
  SESSION: DurableObjectNamespace;
  ASSETS: Fetcher;
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);
    const match = url.pathname.match(/^\/ws\/([A-Z0-9]+)\/(agent|viewer)$/i);

    if (match) {
      const sessionId = match[1].toUpperCase();
      const role = match[2].toLowerCase();
      const id = env.SESSION.idFromName(sessionId);
      const stub = env.SESSION.get(id);
      const doUrl = new URL(request.url);
      doUrl.pathname = `/ws/${sessionId}/${role}`;
      return stub.fetch(new Request(doUrl.toString(), request));
    }

    return env.ASSETS.fetch(request);
  },
} satisfies ExportedHandler<Env>;
