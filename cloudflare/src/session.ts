import type { Attachment, TunnelMeta } from "./types";

const MAX_ATTEMPTS = 5;
const LOCKOUT_MS = 5 * 60 * 1000;

// How long a room is kept alive after the agent disconnects, giving the
// same agent a chance to reattach across a network blip.
const ORPHAN_TTL_MS = 10 * 60 * 1000;

// Per-tunnel-request timeout. If the tunnel agent doesn't reply in this
// window we 504 — covers the local server hanging or the WS dying
// mid-request.
const TUNNEL_REQ_TIMEOUT_MS = 30 * 1000;

// Largest single proxied-WebSocket frame we relay. Base64-wrapped in a
// tunnel_ws_data envelope this stays under the DO's ~1 MiB per-message cap, so
// one big frame can't blow the limit and drop the whole control socket. Matches
// the agent's backend read limit (tunnelChunkBytes).
const MAX_WS_FRAME_BYTES = 700 * 1024;

// Cookie name scoped per-session so multiple port-forwards can each
// have their own auth state in a single browser.
const AUTH_COOKIE_PREFIX = "reminal_auth_";
const COOKIE_MAX_AGE = 30 * 24 * 3600; // 30 days

export class SessionRoom {
  private state: DurableObjectState;
  // pendingTunnelReqs lives in DO instance memory; a request keeps the
  // DO awake until it resolves or times out, so the map never has to
  // survive hibernation.
  // A tunnel response is one or more `tunnel_resp` chunks. The head chunk
  // carries status + headers and resolves the request (with a whole-buffer body
  // for a single chunk, or a ReadableStream for a multi-chunk one); later chunks
  // enqueue into that stream's controller until one arrives with more=false.
  private pendingTunnelReqs: Map<string, {
    resolve: (resp: { status: number; headers: Record<string, string>; body: ReadableStream<Uint8Array> | Uint8Array }) => void;
    timeout: ReturnType<typeof setTimeout>;
    controller?: ReadableStreamDefaultController<Uint8Array>; // set once a multi-chunk (streamed) response begins
  }> = new Map();

  constructor(state: DurableObjectState) {
    this.state = state;
    // Answer app-level keepalive pings in the runtime itself. Without this,
    // every `{"type":"ping"}` wakes the hibernated DO and counts as a billable
    // request; auto-response replies with the canned pong without ever invoking
    // webSocketMessage, so pings cost nothing and the DO stays asleep. The
    // request string must byte-match what clients send: the browser sends
    // JSON.stringify({type:'ping'}) and the Go client marshals
    // protocol.Message{Type:"ping"} (all other fields omitempty) — both are
    // exactly {"type":"ping"}.
    this.state.setWebSocketAutoResponse(
      new WebSocketRequestResponsePair(
        JSON.stringify({ type: "ping" }),
        JSON.stringify({ type: "pong" }),
      ),
    );
  }

  async fetch(request: Request): Promise<Response> {
    const url = new URL(request.url);
    const parts = url.pathname.split("/").filter(Boolean);

    // /p/<id>/... — port-forward proxy. A visitor WebSocket (Upgrade:
    // websocket) is multiplexed to the agent's backend; everything else is a
    // plain HTTP request/response.
    if (parts[0] === "p") {
      if (request.headers.get("Upgrade")?.toLowerCase() === "websocket") {
        return this.handleTunnelWS(request, url);
      }
      return this.handleTunnelHttp(request, url);
    }

    // /ws/<id>/<role> — WebSocket upgrade path (agent / viewer / tunnel).
    const role = parts[2]?.toLowerCase();

    if (request.headers.get("Upgrade") !== "websocket") {
      return new Response("Expected WebSocket", { status: 426 });
    }

    if (role !== "agent" && role !== "viewer" && role !== "tunnel") {
      return new Response("invalid role", { status: 400 });
    }

    const meta = await this.loadMeta();

    let rejectReason: string | null = null;
    if (role === "viewer") {
      if (!meta.agentAuthed) {
        rejectReason = "session not found or not ready";
      }
    }
    // NB: for role === "agent" / "tunnel" we DON'T eagerly reject on
    // the presence of an existing socket. After a network blip the
    // Cloudflare DO may still hold the dead WS in its sockets list
    // until webSocketClose fires (sometimes seconds later), and
    // rejecting the genuine reconnect attempt locked the user out
    // for the full backoff cycle on every wake-from-sleep. Instead
    // we accept the new socket, do the PIN-hash check in handleAuth,
    // and evict any prior socket of the same role once we've proven
    // the new one is the same agent reconnecting (not an impostor).

    const pair = new WebSocketPair();
    const [client, server] = Object.values(pair);
    server.serializeAttachment({ role, authed: false } satisfies Attachment);
    this.state.acceptWebSocket(server);

    if (rejectReason) {
      server.serializeAttachment({ role, authed: false, rejected: true } satisfies Attachment);
      server.send(JSON.stringify({ type: "error", error: rejectReason }));
      server.close(4002, rejectReason);
      return new Response(null, { status: 101, webSocket: client });
    }

    if (role === "agent" || role === "tunnel") {
      await this.state.storage.deleteAlarm();
    }

    return new Response(null, { status: 101, webSocket: client });
  }

  async webSocketMessage(ws: WebSocket, message: string | ArrayBuffer) {
    const attachment = ws.deserializeAttachment() as Attachment;

    // A proxied WebSocket visitor: forward the frame verbatim to the agent,
    // before any control-message parsing (these are opaque app frames).
    if (attachment.role === "visitor") {
      const tunnel = this.getSocket("tunnel");
      if (!tunnel || tunnel.readyState !== WebSocket.OPEN) {
        try { ws.close(1011, "reminal: tunnel offline"); } catch { /* already closing */ }
        return;
      }
      const binary = typeof message !== "string";
      const raw = binary ? new Uint8Array(message as ArrayBuffer) : new TextEncoder().encode(message as string);
      if (raw.byteLength > MAX_WS_FRAME_BYTES) {
        // Too big to relay within the DO message cap — close just this stream.
        try { ws.close(1009, "reminal: message too big"); } catch { /* already closing */ }
        return;
      }
      tunnel.send(JSON.stringify({
        type: "tunnel_ws_data",
        data: JSON.stringify({ stream_id: attachment.streamId, data: bytesToBase64(raw), binary }),
      }));
      return;
    }

    if (typeof message === "string") {
      let parsed: any = null;
      try {
        parsed = JSON.parse(message);
      } catch {
        // not JSON — fall through (no encrypted-binary path for tunnel)
      }

      if (parsed) {
        if (!attachment.authed) {
          const err = await this.handleAuth(ws, attachment, parsed);
          if (err) {
            ws.send(JSON.stringify({ type: "error", error: err }));
            ws.close(4001, err);
          }
          return;
        }

        if (parsed.type === "ping") {
          ws.send(JSON.stringify({ type: "pong" }));
          return;
        }

        // ---- Tunnel-specific control messages ----
        if (attachment.role === "tunnel") {
          if (parsed.type === "tunnel_register") {
            await this.handleTunnelRegister(parsed.data ?? "");
            return;
          }
          if (parsed.type === "tunnel_resp") {
            this.handleTunnelResp(parsed.data ?? "");
            return;
          }
          if (parsed.type === "tunnel_ws_data") {
            this.handleTunnelWSDataFromAgent(parsed.data ?? "");
            return;
          }
          if (parsed.type === "tunnel_ws_close") {
            this.handleTunnelWSCloseFromAgent(parsed.data ?? "");
            return;
          }
          // tunnel sockets don't broadcast to viewers; ignore anything else.
          return;
        }
        // shell-session control (data / resize / etc.) — fall through to forward
      }
    }

    if (!attachment.authed) {
      ws.close(4001, "authentication required");
      return;
    }

    if (attachment.role === "agent") {
      for (const v of this.getSockets("viewer")) {
        const att = v.deserializeAttachment() as Attachment;
        if (att?.authed && v.readyState === WebSocket.OPEN) {
          v.send(message);
        }
      }
    } else if (attachment.role === "viewer") {
      const agent = this.getSocket("agent");
      if (agent?.readyState === WebSocket.OPEN) {
        agent.send(message);
      }
    }
    // tunnel sockets don't pass through opaque messages.
  }

  async webSocketClose(ws: WebSocket) {
    const attachment = ws.deserializeAttachment() as Attachment;
    if (attachment.rejected) return;

    if (attachment.role === "agent") {
      for (const v of this.getSockets("viewer")) {
        const att = v.deserializeAttachment() as Attachment;
        if (att?.authed && v.readyState === WebSocket.OPEN) {
          v.send(JSON.stringify({ type: "agent_offline" }));
        }
      }
      await this.state.storage.setAlarm(Date.now() + ORPHAN_TTL_MS);
    } else if (attachment.role === "viewer") {
      const remaining = this.getSockets("viewer").filter(v => v !== ws);
      if (remaining.length === 0) {
        await this.state.storage.put("viewerAuthed", false);
      }
      const agent = this.getSocket("agent");
      if (agent?.readyState === WebSocket.OPEN) {
        agent.send(JSON.stringify({
          type: "closed",
          error: "viewer disconnected",
          count: remaining.length,
        }));
      }
    } else if (attachment.role === "tunnel") {
      // Fail any in-flight tunnel requests so visitors get a clear signal
      // rather than hanging until the per-request timeout. A request already
      // streaming (controller set) can't change its status any more — its
      // Response headers are on the wire — so end its body with an error;
      // one still awaiting its head resolves to a 502.
      for (const [, entry] of this.pendingTunnelReqs) {
        clearTimeout(entry.timeout);
        if (entry.controller) {
          try { entry.controller.error(new Error("reminal: tunnel disconnected")); } catch { /* already closed */ }
        } else {
          entry.resolve({
            status: 502,
            headers: { "Content-Type": "text/plain" },
            body: new TextEncoder().encode("reminal: tunnel disconnected\n"),
          });
        }
      }
      this.pendingTunnelReqs.clear();
      // Every proxied visitor WebSocket rode over this control socket; close them
      // so browsers reconnect instead of hanging on a dead stream.
      for (const v of this.getSockets("visitor")) {
        try { v.close(1011, "reminal: tunnel disconnected"); } catch { /* already closing */ }
      }
      await this.state.storage.setAlarm(Date.now() + ORPHAN_TTL_MS);
    } else if (attachment.role === "visitor") {
      // Visitor hung up: tell the agent to close the backend connection.
      const tunnel = this.getSocket("tunnel");
      if (tunnel?.readyState === WebSocket.OPEN && attachment.streamId) {
        tunnel.send(JSON.stringify({
          type: "tunnel_ws_close",
          data: JSON.stringify({ stream_id: attachment.streamId }),
        }));
      }
    }
  }

  async webSocketError(ws: WebSocket) {
    return this.webSocketClose(ws);
  }

  async alarm() {
    if (this.getSocket("agent") || this.getSocket("tunnel")) return;
    for (const v of this.getSockets("viewer")) {
      if (v.readyState === WebSocket.OPEN) {
        v.send(JSON.stringify({ type: "closed", error: "agent session expired" }));
        v.close(1000, "expired");
      }
    }
    await this.state.storage.deleteAll();
  }

  // ---- auth ----

  private async handleAuth(
    ws: WebSocket,
    attachment: Attachment,
    msg: { type?: string; pin?: string; pin_hash?: string; token?: string },
  ): Promise<string | null> {
    if (msg.type !== "auth") {
      return "authentication required";
    }

    const meta = await this.loadMeta();
    if (meta.lockedUntil && Date.now() < meta.lockedUntil) {
      return "too many failed attempts — try again in a few minutes";
    }

    if (attachment.role === "agent" || attachment.role === "tunnel") {
      // Accept-either: the agent proves control with a high-entropy reattach
      // *token* (preferred) and/or a legacy bcrypt *pin_hash*. The relay never
      // checks either against the PIN — they're opaque secrets matched only
      // against what the session's original agent registered. This keeps old
      // agents (pin_hash only) working while new agents move to token-only; a
      // legacy session migrates the first time its upgraded agent presents
      // pin_hash (to prove control) alongside a fresh token.
      const token = msg.token ?? "";
      const pinHash = msg.pin_hash ?? "";
      if (!token && !pinHash) {
        return "agent credential required";
      }
      const storedToken = (await this.state.storage.get<string>("token")) ?? "";
      const storedPinHash = (await this.state.storage.get<string>("pinHash")) ?? "";
      if (storedToken) {
        // Already migrated to a token — nothing else authenticates.
        if (token !== storedToken) {
          return "session credentials mismatch";
        }
      } else if (storedPinHash) {
        // Legacy session: require the same pin_hash. If the agent also brought a
        // token, migrate to token-only and drop the offline-crackable pin_hash.
        if (pinHash !== storedPinHash) {
          return "session credentials mismatch";
        }
        if (token) {
          await this.state.storage.put("token", token);
          await this.state.storage.delete("pinHash");
        }
      } else {
        // Brand-new session: register whatever proves control, preferring the
        // token so no PIN-derived value is ever stored.
        if (token) {
          await this.state.storage.put("token", token);
        } else {
          await this.state.storage.put("pinHash", pinHash);
        }
      }
      // Credential matches → this is the legitimate agent (or tunnel)
      // reclaiming the session. Evict any prior socket of the same
      // role: it's a stale WS that the DO hasn't yet noticed is dead
      // (the close handler races slower than the genuine reconnect
      // after a sleep/wake or network blip). Without this eviction
      // the user gets "another agent is already connected" on every
      // wake until the dead socket times out — sometimes 30+ seconds.
      for (const prior of this.getSockets(attachment.role)) {
        if (prior === ws) continue;
        const att = prior.deserializeAttachment() as Attachment;
        if (att?.rejected) continue;
        try {
          prior.send(JSON.stringify({ type: "error", error: "superseded by a fresh agent connection" }));
          prior.close(4000, "superseded");
        } catch { /* already closing — best-effort */ }
      }
      if (attachment.role === "agent") {
        await this.state.storage.put("agentAuthed", true);
      }
      await this.resetFailures();
      ws.serializeAttachment({ role: attachment.role, authed: true } satisfies Attachment);
      ws.send(JSON.stringify({ type: "auth_ok" }));

      if (attachment.role === "agent") {
        for (const v of this.getSockets("viewer")) {
          const att = v.deserializeAttachment() as Attachment;
          if (att?.authed && v.readyState === WebSocket.OPEN) {
            v.send(JSON.stringify({ type: "agent_online" }));
          }
        }
      }
      return null;
    }

    // viewer — the relay does NOT verify the PIN. A 6-digit PIN it could check
    // is offline-brute-forceable, and knowing it would let a malicious relay
    // unblind both ephemeral keys and MITM the EKE. Viewer PIN auth is done
    // END-TO-END by the EKE (a wrong PIN fails the AES-GCM unwrap, surfaced as
    // "PIN mismatch"). Gate only on the session being live; a `pin` from an
    // older viewer is ignored (still accepted, for backward compatibility).
    const agentAuthed = (await this.state.storage.get<boolean>("agentAuthed")) ?? false;
    if (!agentAuthed) return "session not ready";
    await this.state.storage.put("viewerAuthed", true);
    ws.serializeAttachment({ role: "viewer", authed: true } satisfies Attachment);
    ws.send(JSON.stringify({ type: "auth_ok" }));

    const agent = this.getSocket("agent");
    if (agent?.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: "connected" }));
      const att = agent.deserializeAttachment() as Attachment;
      if (att?.authed) {
        agent.send(JSON.stringify({
          type: "connected",
          count: this.getSockets("viewer").filter(v => {
            const a = v.deserializeAttachment() as Attachment;
            return a?.authed;
          }).length,
        }));
      }
    } else {
      ws.send(JSON.stringify({ type: "agent_offline" }));
    }
    return null;
  }

  // ---- tunnel: register + request/response correlation ----

  private async handleTunnelRegister(dataJSON: string) {
    let info: { port?: number; pin_hash?: string; public?: boolean } = {};
    try {
      info = JSON.parse(dataJSON);
    } catch {
      return;
    }
    const port = typeof info.port === "number" ? info.port : 0;
    const pinHash = info.pin_hash ?? "";
    const isPublic = !!info.public;
    if (!port || !pinHash) return;

    // Generate a per-session signing key for the auth cookie HMAC. New
    // each registration so a stale cookie from a prior session-ID reuse
    // doesn't accidentally grant access.
    const keyBytes = new Uint8Array(32);
    crypto.getRandomValues(keyBytes);
    const signingKey = toHex(keyBytes);

    const meta: TunnelMeta = { port, pinHash, public: isPublic, signingKey };
    await this.state.storage.put("tunnelMeta", meta);
  }

  private handleTunnelResp(dataJSON: string) {
    let resp: any = null;
    try {
      resp = JSON.parse(dataJSON);
    } catch {
      return;
    }
    const reqID = resp?.req_id;
    if (!reqID) return;
    const entry = this.pendingTunnelReqs.get(reqID);
    if (!entry) return;

    const chunk = typeof resp.body === "string" ? base64ToBytes(resp.body) : new Uint8Array();
    const more = resp.more === true;

    // Idle watchdog: a stalled stream mustn't pin the DO. Re-armed per chunk.
    const armStall = () => {
      entry.timeout = setTimeout(() => {
        try { entry.controller?.error(new Error("reminal: tunnel stream stalled")); } catch { /* already closed */ }
        this.pendingTunnelReqs.delete(reqID);
      }, TUNNEL_REQ_TIMEOUT_MS);
    };

    // Continuation chunk of an already-streaming response.
    if (entry.controller) {
      clearTimeout(entry.timeout);
      if (chunk.length) {
        try { entry.controller.enqueue(chunk); } catch { /* visitor cancelled the read */ }
      }
      if (more) {
        armStall();
      } else {
        try { entry.controller.close(); } catch { /* already closed/cancelled */ }
        this.pendingTunnelReqs.delete(reqID);
      }
      return;
    }

    // Head chunk — carries status + headers.
    clearTimeout(entry.timeout);
    const status = typeof resp.status === "number" ? resp.status : 502;
    const headers: Record<string, string> = resp.headers ?? {};

    if (!more) {
      // Single-chunk response (small body, or an older single-message agent).
      this.pendingTunnelReqs.delete(reqID);
      entry.resolve({ status, headers, body: chunk });
      return;
    }

    // Multi-chunk: open a stream, hand the Response back now, keep enqueuing as
    // later chunks arrive. Keep the entry in the map (with its controller) until
    // a chunk with more=false closes it.
    const stream = new ReadableStream<Uint8Array>({
      start: (controller) => {
        if (chunk.length) controller.enqueue(chunk);
        entry.controller = controller;
      },
      cancel: () => {
        // Visitor aborted the download; stop tracking it. The agent stops when
        // its next WS write is dropped.
        clearTimeout(entry.timeout);
        this.pendingTunnelReqs.delete(reqID);
      },
    });
    armStall();
    entry.resolve({ status, headers, body: stream });
  }

  // ---- tunnel: WebSocket proxy ----

  // handleTunnelWS upgrades a visitor WebSocket and hands the agent a matching
  // stream id so it can dial the local backend. Frames then flow both ways as
  // tunnel_ws_data over the agent's control socket (see webSocketMessage).
  private async handleTunnelWS(request: Request, url: URL): Promise<Response> {
    const m = url.pathname.match(/^\/p\/([A-Z0-9]+)(\/.*|$)/i);
    if (!m) {
      return new Response("Not found", { status: 404 });
    }
    const sessionId = m[1].toUpperCase();
    const rest = m[2] || "/";

    const meta = (await this.state.storage.get<TunnelMeta>("tunnelMeta")) ?? null;
    if (!meta) {
      return new Response("reminal: tunnel not found\n", { status: 404 });
    }

    // Non-public tunnels: a WebSocket can't render the HTML PIN gate, so the
    // visitor must already hold the auth cookie from a normal page load.
    if (!meta.public) {
      const cookies = parseCookies(request.headers.get("Cookie") ?? "");
      const cookieVal = cookies[AUTH_COOKIE_PREFIX + sessionId] ?? "";
      const expected = await hmacHex(meta.signingKey, "ok");
      if (cookieVal !== expected) {
        return new Response("reminal: authentication required\n", { status: 401 });
      }
    }

    const tunnel = this.getSocket("tunnel");
    if (!tunnel || tunnel.readyState !== WebSocket.OPEN) {
      return new Response("reminal: tunnel offline\n", { status: 503 });
    }

    const streamId = crypto.randomUUID();
    const pair = new WebSocketPair();
    const [client, server] = Object.values(pair);
    server.serializeAttachment({ role: "visitor", authed: true, streamId } satisfies Attachment);
    this.state.acceptWebSocket(server);

    const headers: Record<string, string> = {};
    request.headers.forEach((v, k) => {
      if (k.toLowerCase() === "cookie") return; // don't leak the reminal auth cookie to the app
      headers[k] = v;
    });
    try {
      tunnel.send(JSON.stringify({
        type: "tunnel_ws_open",
        data: JSON.stringify({ stream_id: streamId, url: rest + (url.search ?? ""), headers }),
      }));
    } catch {
      // Tunnel socket died between the readyState check and here — close the
      // freshly-accepted visitor side so the browser sees a clean failure.
      try { server.close(1011, "reminal: tunnel offline"); } catch { /* already closing */ }
    }

    // Echo the client's first requested subprotocol so a client that asked for
    // one still completes its handshake. (Negotiating against the backend's
    // actual pick is a known limitation.)
    const respHeaders: Record<string, string> = {};
    const proto = request.headers.get("Sec-WebSocket-Protocol");
    if (proto) respHeaders["Sec-WebSocket-Protocol"] = proto.split(",")[0].trim();

    return new Response(null, { status: 101, webSocket: client, headers: respHeaders });
  }

  // handleTunnelWSDataFromAgent delivers a backend→visitor frame to the right
  // visitor socket.
  private handleTunnelWSDataFromAgent(dataJSON: string) {
    let m: any = null;
    try { m = JSON.parse(dataJSON); } catch { return; }
    const streamId = m?.stream_id;
    if (!streamId) return;
    const sock = this.getVisitorSocket(streamId);
    if (!sock || sock.readyState !== WebSocket.OPEN) return;
    const bytes = typeof m.data === "string" ? base64ToBytes(m.data) : new Uint8Array();
    try {
      if (m.binary) sock.send(bytes);
      else sock.send(new TextDecoder().decode(bytes));
    } catch { /* visitor gone */ }
  }

  // handleTunnelWSCloseFromAgent closes the visitor socket because the backend
  // side ended.
  private handleTunnelWSCloseFromAgent(dataJSON: string) {
    let m: any = null;
    try { m = JSON.parse(dataJSON); } catch { return; }
    const streamId = m?.stream_id;
    if (!streamId) return;
    const sock = this.getVisitorSocket(streamId);
    if (sock) {
      try { sock.close(1000); } catch { /* already closing */ }
    }
  }

  private getVisitorSocket(streamId: string): WebSocket | null {
    for (const ws of this.state.getWebSockets()) {
      const att = ws.deserializeAttachment() as Attachment;
      if (att?.role === "visitor" && att.streamId === streamId) return ws;
    }
    return null;
  }

  // ---- tunnel: HTTP proxy ----

  private async handleTunnelHttp(request: Request, url: URL): Promise<Response> {
    // Match `/p/<id>` and slice the rest verbatim so the trailing slash
    // (and anything else) survives intact. The earlier split/join
    // approach dropped a trailing slash, which caused upstream apps to
    // redirect "/folder" → "/folder/", we'd re-prefix it, and the
    // browser would bounce back to "/folder" again → infinite loop.
    const m = url.pathname.match(/^\/p\/([A-Z0-9]+)(\/.*|$)/i);
    if (!m) {
      return new Response("Not found", { status: 404 });
    }
    const sessionId = m[1].toUpperCase();
    const rest = m[2] || "/";

    const meta = (await this.state.storage.get<TunnelMeta>("tunnelMeta")) ?? null;
    if (!meta) {
      return new Response(notFoundPage(), {
        status: 404,
        headers: { "Content-Type": "text/html; charset=utf-8" },
      });
    }

    // POST /p/<id>/__auth — submit PIN.
    if (rest === "/__auth" && request.method === "POST") {
      return this.handleTunnelAuth(request, sessionId, meta);
    }

    // Public tunnels skip the gate entirely.
    if (!meta.public) {
      const cookies = parseCookies(request.headers.get("Cookie") ?? "");
      const cookieVal = cookies[AUTH_COOKIE_PREFIX + sessionId] ?? "";
      const expected = await hmacHex(meta.signingKey, "ok");
      if (cookieVal !== expected) {
        const wantTo = url.pathname + url.search;
        return new Response(pinGatePage(sessionId, wantTo, ""), {
          status: 200,
          headers: { "Content-Type": "text/html; charset=utf-8" },
        });
      }
    }

    // Forward to the tunnel WS.
    const tunnel = this.getSocket("tunnel");
    if (!tunnel || tunnel.readyState !== WebSocket.OPEN) {
      return new Response("reminal: tunnel offline\n", {
        status: 503,
        headers: { "Content-Type": "text/plain" },
      });
    }

    const reqID = crypto.randomUUID();
    const bodyBytes = await request.arrayBuffer();
    const headers: Record<string, string> = {};
    request.headers.forEach((v, k) => {
      const lk = k.toLowerCase();
      // The agent re-adds X-Forwarded-* itself; we shouldn't trust
      // what Cloudflare passed (already added cf-* headers etc.).
      if (lk === "cookie") return; // don't leak the reminal auth cookie to the user's app
      headers[k] = v;
    });

    const promise = new Promise<{ status: number; headers: Record<string, string>; body: ReadableStream<Uint8Array> | Uint8Array }>((resolve) => {
      const timeout = setTimeout(() => {
        this.pendingTunnelReqs.delete(reqID);
        resolve({
          status: 504,
          headers: { "Content-Type": "text/plain" },
          body: new TextEncoder().encode("reminal: upstream timeout\n"),
        });
      }, TUNNEL_REQ_TIMEOUT_MS);
      this.pendingTunnelReqs.set(reqID, { resolve, timeout });
    });

    tunnel.send(JSON.stringify({
      type: "tunnel_req",
      data: JSON.stringify({
        req_id: reqID,
        method: request.method,
        url: rest + (url.search ?? ""),
        headers,
        body: bytesToBase64(new Uint8Array(bodyBytes)),
      }),
    }));

    const out = await promise;

    // Rewrite redirects so they stay under /p/<id>/. Upstream apps don't know
    // we're behind a prefix (and see their own Host as 127.0.0.1:<port>), so
    // they emit Location headers we have to fix:
    //   - absolute-PATH ("/folder/") — the browser would resolve it to the
    //     relay root (404); re-prefix to /p/<id>/folder/.
    //   - absolute-URL to LOOPBACK ("https://127.0.0.1:10000/x", "http://localhost/x",
    //     "http://[::1]/x") — the app built it from the Host we handed it. The
    //     browser would follow it to the VISITOR'S own machine; rewrite to the
    //     public path+query under the prefix. A loopback host is unambiguously
    //     wrong for a public tunnel, so this is always safe.
    // Absolute URLs to any OTHER host are left alone (real off-site redirects).
    const prefix = `/p/${sessionId}`;
    for (const key of Object.keys(out.headers)) {
      if (key.toLowerCase() !== "location") continue;
      const v = out.headers[key];
      if (!v) continue;
      if (v.startsWith("/") && !v.startsWith(prefix + "/") && !v.startsWith("//")) {
        out.headers[key] = prefix + v;
        continue;
      }
      const loop = loopbackLocationPath(v);
      if (loop !== null) {
        out.headers[key] = prefix + loop;
      }
    }

    return new Response(out.body as BodyInit, { status: out.status, headers: out.headers });
  }

  private async handleTunnelAuth(request: Request, sessionId: string, meta: TunnelMeta): Promise<Response> {
    const form = await request.formData();
    const pin = String(form.get("pin") ?? "");
    const to = String(form.get("to") ?? `/p/${sessionId}/`);

    const m = await this.loadMeta();
    if (m.lockedUntil && Date.now() < m.lockedUntil) {
      return new Response(pinGatePage(sessionId, to, "Too many failed attempts — try again in a few minutes."), {
        status: 429,
        headers: { "Content-Type": "text/html; charset=utf-8" },
      });
    }

    const { compare } = await import("bcryptjs");
    if (!pin || !(await compare(pin, meta.pinHash))) {
      await this.recordFailure();
      return new Response(pinGatePage(sessionId, to, "Incorrect PIN."), {
        status: 401,
        headers: { "Content-Type": "text/html; charset=utf-8" },
      });
    }
    await this.resetFailures();

    const cookieVal = await hmacHex(meta.signingKey, "ok");
    const cookie =
      `${AUTH_COOKIE_PREFIX}${sessionId}=${cookieVal}; ` +
      `Path=/p/${sessionId}/; ` +
      `Max-Age=${COOKIE_MAX_AGE}; ` +
      `HttpOnly; Secure; SameSite=Lax`;
    // Defensive: only redirect within /p/<id>/.
    const safeTo = to.startsWith(`/p/${sessionId}/`) ? to : `/p/${sessionId}/`;
    return new Response(null, {
      status: 302,
      headers: { Location: safeTo, "Set-Cookie": cookie },
    });
  }

  // ---- helpers ----

  private getSocket(role: string): WebSocket | null {
    for (const ws of this.state.getWebSockets()) {
      const att = ws.deserializeAttachment() as Attachment;
      if (att?.role === role && !att.rejected) return ws;
    }
    return null;
  }

  private getSockets(role: string): WebSocket[] {
    const out: WebSocket[] = [];
    for (const ws of this.state.getWebSockets()) {
      const att = ws.deserializeAttachment() as Attachment;
      if (att?.role === role && !att.rejected) out.push(ws);
    }
    return out;
  }

  private async loadMeta() {
    const [agentAuthed, viewerAuthed, lockedUntil, failedAttempts] = await Promise.all([
      this.state.storage.get<boolean>("agentAuthed"),
      this.state.storage.get<boolean>("viewerAuthed"),
      this.state.storage.get<number>("lockedUntil"),
      this.state.storage.get<number>("failedAttempts"),
    ]);
    return {
      agentAuthed: !!agentAuthed,
      viewerAuthed: !!viewerAuthed,
      lockedUntil: lockedUntil ?? 0,
      failedAttempts: failedAttempts ?? 0,
    };
  }

  private async recordFailure() {
    const meta = await this.loadMeta();
    let attempts = meta.failedAttempts + 1;
    if (attempts >= MAX_ATTEMPTS) {
      await this.state.storage.put("lockedUntil", Date.now() + LOCKOUT_MS);
      attempts = 0;
    }
    await this.state.storage.put("failedAttempts", attempts);
  }

  private async resetFailures() {
    await this.state.storage.put("failedAttempts", 0);
    await this.state.storage.delete("lockedUntil");
  }
}

// ---- shared crypto / encoding helpers ----

async function hmacHex(keyHex: string, message: string): Promise<string> {
  const keyBytes = fromHex(keyHex);
  const key = await crypto.subtle.importKey(
    "raw", keyBytes.buffer as ArrayBuffer, { name: "HMAC", hash: "SHA-256" }, false, ["sign"],
  );
  const data = new TextEncoder().encode(message);
  const sig = await crypto.subtle.sign("HMAC", key, data.buffer as ArrayBuffer);
  return toHex(new Uint8Array(sig));
}

// loopbackLocationPath returns the path+query of a Location header value IFF it
// is an absolute URL pointing at a loopback host — 127.0.0.0/8, localhost, or
// ::1, any port. That's the address the tunnel agent hands the upstream as its
// Host, so an app that builds an absolute self-redirect (canonical host / HTTPS
// / trailing slash) emits e.g. "https://127.0.0.1:10000/x"; following it would
// send the visitor to their OWN machine. Returns null for anything else — real
// off-site redirects and relative/opaque values are left untouched.
function loopbackLocationPath(loc: string): string | null {
  let u: URL;
  try {
    u = new URL(loc);
  } catch {
    return null; // not an absolute URL (relative path handled elsewhere)
  }
  const h = u.hostname.toLowerCase();
  const isLoopback = h === "localhost" || h === "::1" || h === "[::1]" || h.startsWith("127.");
  return isLoopback ? u.pathname + u.search : null;
}

function toHex(b: Uint8Array): string {
  let s = "";
  for (let i = 0; i < b.length; i++) s += b[i].toString(16).padStart(2, "0");
  return s;
}

function fromHex(s: string): Uint8Array {
  const out = new Uint8Array(s.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(s.slice(i * 2, i * 2 + 2), 16);
  return out;
}

function base64ToBytes(b64: string): Uint8Array {
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

function bytesToBase64(b: Uint8Array): string {
  let s = "";
  const stride = 0x1000;
  for (let i = 0; i < b.length; i += stride) {
    s += String.fromCharCode.apply(null, Array.from(b.subarray(i, i + stride)));
  }
  return btoa(s);
}

function parseCookies(header: string): Record<string, string> {
  const out: Record<string, string> = {};
  if (!header) return out;
  for (const part of header.split(";")) {
    const eq = part.indexOf("=");
    if (eq < 0) continue;
    out[part.slice(0, eq).trim()] = decodeURIComponent(part.slice(eq + 1).trim());
  }
  return out;
}

// ---- HTML pages ----

function pinGatePage(sessionId: string, to: string, errorMsg: string): string {
  const escTo = escapeHtml(to);
  const escErr = errorMsg ? `<p class="err">${escapeHtml(errorMsg)}</p>` : "";
  // Inline page; reads URL fragment (#p=NNNNNN) and auto-submits so the
  // QR-code / quick-link flow is one-tap. Fragment never leaves the
  // browser (referer / logs are safe).
  return `<!doctype html>
<html><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>reminal — PIN required</title>
<style>
  *{box-sizing:border-box;margin:0;padding:0}
  body{background:#0d1117;color:#e6edf3;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;
       min-height:100vh;display:flex;align-items:center;justify-content:center;padding:24px}
  .card{background:#161b22;border:1px solid #21262d;border-radius:12px;padding:24px;max-width:360px;width:100%}
  h1{font-size:18px;font-weight:600;margin-bottom:4px}
  h1 span{color:#58a6ff}
  p.sub{color:#8b949e;font-size:13px;margin-bottom:16px}
  p.err{color:#f85149;font-size:13px;margin-bottom:12px}
  input{width:100%;background:#0d1117;border:1px solid #30363d;border-radius:6px;color:#e6edf3;
        font-size:18px;font-family:Menlo,Monaco,monospace;letter-spacing:0.15em;padding:10px 12px;margin-bottom:12px}
  input:focus{outline:none;border-color:#58a6ff}
  button{width:100%;background:#238636;border:none;border-radius:6px;color:#fff;font-size:14px;
         font-weight:500;padding:10px;cursor:pointer}
  button:hover{background:#2ea043}
  .foot{margin-top:16px;font-size:11px;color:#6e7681}
</style>
</head><body>
<form class="card" method="POST" action="/p/${sessionId}/__auth" id="f">
  <h1><span>re</span>minal</h1>
  <p class="sub">PIN required to reach <code>${sessionId}</code></p>
  ${escErr}
  <input type="hidden" name="to" value="${escTo}">
  <input name="pin" type="text" inputmode="numeric" autocomplete="off" autofocus placeholder="PIN" required>
  <button type="submit">Continue</button>
  <p class="foot">A reminal port forward sits behind this page.</p>
</form>
<script>
  // Auto-fill from URL fragment (#p=NNNNNN) and submit. Fragments never
  // leave the browser, so links can safely embed the PIN.
  (function(){
    var m = (location.hash || '').match(/[#&]p=([^&]+)/);
    if (!m) return;
    var pin = decodeURIComponent(m[1]);
    var f = document.getElementById('f');
    f.pin.value = pin;
    history.replaceState(null, '', location.pathname + location.search);
    f.submit();
  })();
</script>
</body></html>`;
}

function notFoundPage(): string {
  return `<!doctype html>
<html><head><meta charset="utf-8"><title>Not found</title>
<style>body{background:#0d1117;color:#8b949e;font-family:-apple-system,sans-serif;padding:48px;text-align:center}</style>
</head><body><h1>reminal</h1><p>No port forward at this address.</p></body></html>`;
}

function escapeHtml(s: string): string {
  return s.replace(/[&<>"']/g, c => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]!));
}
