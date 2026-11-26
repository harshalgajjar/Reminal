import { closeBack } from "./closeback";
import { placeCode, placeFromTrace } from "./place";
import type { Attachment, TunnelMeta } from "./types";
import { ALARM_ARMS, armAlarm, type AlarmArms } from "./alarm";
import { BodyBudget, readBody } from "./body";
import {
  NO_SOURCE, VISITOR_MAX_AGE_S, gateRight, gateWait, gateWrong, legacyCookie, normalizePIN, timingSafeEqual, toHex,
  visitorCookie, visitorCookieOK, type GateState, type Strikes,
} from "./gate";

// PIN handshakes are paced per address: a room passes on at most
// HANDSHAKE_BURST from one address (IPv6: one /64), then one per
// HANDSHAKE_REFILL_MS, counted only while the machine is connected. A paced
// handshake is answered with pake_busy (try again shortly), which a viewer
// that predates it ignores. Each forwarded handshake carries `src`, an opaque
// tag for its source, so the machine can keep a share of its own allowance
// per source. Same numbers as the Go relay (internal/relay/handshakelimit.go).
const HANDSHAKE_BURST = 10;
const HANDSHAKE_REFILL_MS = 60 * 1000;
// Parking and unparking (a viewer switching away and back) are paced the
// same way, more loosely: a switch is one of each.
const PARK_BURST = 60;
const PARK_REFILL_MS = 1000;

// How long a room is kept alive after the agent disconnects, giving the
// same agent a chance to reattach across a network blip.
const ORPHAN_TTL_MS = 10 * 60 * 1000;

// How far off a room's expiry stays while an authenticated agent or tunnel is
// connected. Its close arms the near expiry (ORPHAN_TTL_MS), but a socket can
// end with no close reaching the room: an eviction or a runtime restart while
// it is open, a close the hibernation API never delivers. With no alarm armed,
// such a room would keep its credentials and counts for good. This one comes
// round anyway: with nobody connected it clears the room as any expiry does;
// with the agent still there, alarm() arms it again, one wake a day.
const LIVE_EXPIRY_MS = 24 * 60 * 60 * 1000;

// How long a room remembers who it belongs to after that. Once the room has
// been cleared, only an agent with the same credential may take the session ID
// again within this window (a reboot, a `reminal restore`, a laptop asleep for
// the weekend). Kept as a hash of the credential, never the credential itself.
// Every reconnect renews it.
const OWNER_KEEP_MS = 30 * 24 * 60 * 60 * 1000;

// Per-tunnel-request timeout. If the tunnel agent doesn't reply in this
// window we 504 — covers the local server hanging or the WS dying
// mid-request.
const TUNNEL_REQ_TIMEOUT_MS = 30 * 1000;

// Largest single proxied-WebSocket frame we relay. Base64-wrapped in a
// tunnel_ws_data envelope this stays under the DO's ~1 MiB per-message cap, so
// one big frame can't blow the limit and drop the whole control socket. Matches
// the agent's backend read limit (tunnelChunkBytes).
const MAX_WS_FRAME_BYTES = 700 * 1024;

// Ceiling on concurrently proxied visitor WebSockets per tunnel, enforced before
// we accept the upgrade — so a public tunnel can't be driven into unbounded
// accept/close churn (and tunnel_ws_open amplification) that the agent-side cap
// would only catch after the fact. Matches the agent's maxWSStreams.
const MAX_VISITOR_SOCKETS = 512;
// Viewers one session takes at once — open or kept for a quick switch back:
// every viewer-tagged socket. A person has a few; refused before accepting,
// so nobody can open sockets on a session without end.
export const MAX_VIEWER_SOCKETS = 64;

// Largest request body we'll relay. Matches the agent's assembled-body ceiling
// (maxTunnelResponse), so anything bigger would be truncated into garbage
// anyway — a clean 413 beats a silently mangled upload, and it keeps the body
// plus its queued base64 chunks inside the DO's ~128 MB budget.
const MAX_REQUEST_BODY_BYTES = 64 * 1024 * 1024;
// Upper bound on the post-login redirect target rendered into the gate page.
// `to` is only ever a same-origin path (a few hundred bytes at most), but it is
// unauthenticated visitor input that gets HTML-escaped into an inline page — and
// the request-body ceiling above does NOT apply to /__auth (that branch returns
// before the size check). Left unbounded, a multi-megabyte `to` makes the DO
// build a same-sized-or-larger string in memory (escaping amplifies "/&< up to
// 6x) on every gate-rendering path (wrong PIN, lockout), occasionally tipping
// the DO into an out-of-memory crash. Over the cap we fall back to root: a
// legitimate value is never close to it, so this only ever rejects abuse.
const MAX_AUTH_TO_BYTES = 4096;

// Ceiling on the whole /__auth request body. The endpoint is UNAUTHENTICATED and
// parses a form, which needs the entire body in memory before we can look at
// it — so without this any stranger could POST a multi-MB body at a
// gated tunnel and pile pressure on the DO's ~128 MB budget. A real login form is
// tiny (a PIN plus a `to` capped at MAX_AUTH_TO_BYTES), so 64 KiB is orders of
// magnitude of headroom and only ever rejects abuse. Same intent as the main
// path's Content-Length pre-check.
const MAX_AUTH_BODY_BYTES = 64 * 1024;

// How long the visitor's WebSocket 101 waits for the agent to report which
// subprotocol the backend chose. Only ever waited on when the client offered a
// subprotocol AND the agent advertised ws_subproto, so a normal socket opens
// with no delay; past it we fall back to echoing the client's first offer
// rather than failing an otherwise-fine handshake.
const WS_OPEN_TIMEOUT_MS = 10 * 1000;

// Cookie name scoped per-session so multiple port-forwards can each
// have their own auth state in a single browser. See authCookieName.
const AUTH_COOKIE_PREFIX = "reminal_auth_";

export class SessionRoom {
  private state: DurableObjectState;
  // The code of the Cloudflare location this object runs in, once it has
  // been asked for (relayPlace). Kept in memory only: asked again after the
  // object has slept, which is when an agent next authenticates.
  private place = "";
  // Serializes PIN-gate attempts. bcrypt.compare is deliberately expensive (~1024
  // rounds) and the DO isolate is single-threaded, so a burst of concurrent
  // /__auth POSTs would otherwise (a) run many compares at once, pegging the CPU
  // until the isolate is reset — returning 500s to visitors AND dropping the
  // agent's control socket — and (b) race the failure counter's read-modify-write
  // so the lockout never engages within the burst, turning "5 guesses before a
  // wait" into "N concurrent guesses". Chaining each attempt
  // through this promise makes the check-compare-record section atomic and bounds
  // concurrent compares to one; once locked, later attempts take the fast 429 path
  // with no compare. Instance memory, like pendingTunnelReqs — a fresh DO resets
  // it to resolved, which is correct (no attempt is in flight).
  private authGate: Promise<void> = Promise.resolve();
  // What uploads being read at once hold together (body.ts): each is under
  // MAX_REQUEST_BODY_BYTES, and so are all of them — two at the ceiling
  // would not fit the room's memory.
  private uploads = new BodyBudget(MAX_REQUEST_BODY_BYTES);
  // pendingTunnelReqs lives in DO instance memory; a request keeps the
  // DO awake until it resolves or times out, so the map never has to
  // survive hibernation.
  // A tunnel response is one or more `tunnel_resp` chunks. The head chunk
  // carries status + headers and resolves the request (with a whole-buffer body
  // for a single chunk, or a ReadableStream for a multi-chunk one); later chunks
  // enqueue into that stream's controller until one arrives with more=false.
  private pendingTunnelReqs: Map<string, {
    resolve: (resp: { status: number; headers: Record<string, string>; setCookies?: string[]; multiHeaders?: Record<string, string[]>; body: ReadableStream<Uint8Array> | Uint8Array }) => void;
    timeout: ReturnType<typeof setTimeout>;
    controller?: ReadableStreamDefaultController<Uint8Array>; // set once a multi-chunk (streamed) response begins
  }> = new Map();

  // Visitor WebSocket handshakes waiting on the agent to report the backend's
  // chosen subprotocol (tunnel_ws_opened). Keyed by stream id; resolved with
  // the pick, or with null when the agent reports the dial failed or never
  // answers. Like pendingTunnelReqs this lives in instance memory — the
  // pending request keeps the DO awake until it settles.
  private pendingWSOpens: Map<string, {
    resolve: (subprotocol: string | null) => void;
    timeout: ReturnType<typeof setTimeout>;
  }> = new Map();

  // Overridable for tests only (wrangler dev --var); production uses the
  // constants above.
  private orphanTTL = ORPHAN_TTL_MS;
  private ownerKeep = OWNER_KEEP_MS;

  constructor(state: DurableObjectState, env?: { ORPHAN_TTL_MS?: string; OWNER_KEEP_MS?: string }) {
    this.state = state;
    const n = (v?: string) => (v && Number(v) > 0 ? Number(v) : 0);
    this.orphanTTL = n(env?.ORPHAN_TTL_MS) || ORPHAN_TTL_MS;
    this.ownerKeep = n(env?.OWNER_KEEP_MS) || OWNER_KEEP_MS;
    // Answer app-level keepalive pings in the runtime itself. Without this,
    // every `{"type":"ping"}` wakes the hibernated DO and counts as a billable
    // request; auto-response replies with the canned pong without ever invoking
    // webSocketMessage, so pings cost nothing and the DO stays asleep. The
    // request string must byte-match what clients send: the browser sends
    // JSON.stringify({type:'ping'}) and the Go client marshals
    // protocol.Message{Type:"ping"} (all other fields omitempty) — both are
    // exactly {"type":"ping"}. NOTE: this is DO-wide, so a proxied visitor
    // WebSocket whose app sends a text frame byte-equal to {"type":"ping"} has
    // it answered here and never forwarded to the backend. That exact shape is
    // vanishingly rare in real app protocols; living with it keeps every agent/
    // viewer ping off the billable wake path.
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
      // Host-mode (set by the front worker for port-<id>.<domain>): the app is
      // served at the origin root, so no /p/<id>/ prefix is added to redirects,
      // the auth cookie, or the gate — see handleTunnelHttp/handleTunnelAuth.
      const hostMode = request.headers.get("x-reminal-host-mode") === "1";
      if (request.headers.get("Upgrade")?.toLowerCase() === "websocket") {
        return this.handleTunnelWS(request, url, hostMode);
      }
      return this.handleTunnelHttp(request, url, hostMode);
    }

    // /ws/<id>/<role> — WebSocket upgrade path (agent / viewer / tunnel).
    const role = parts[2]?.toLowerCase();

    if (request.headers.get("Upgrade") !== "websocket") {
      return new Response("Expected WebSocket", { status: 426 });
    }

    if (role !== "agent" && role !== "viewer" && role !== "tunnel") {
      return new Response("invalid role", { status: 400 });
    }

    // Refused before accepting, as a visitor past its cap is: no socket, and
    // no accept-then-close churn to pay for.
    if (role === "viewer" && this.getSockets("viewer").length >= MAX_VIEWER_SOCKETS) {
      return new Response("reminal: too many viewers on this session\n", { status: 503 });
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

    if (rejectReason) {
      // Nothing is written for a room that is not ready: a viewer can name
      // any session id, and a room that was never used has no expiry to
      // clear what it would leave.
      server.serializeAttachment({ role, authed: false, rejected: true } satisfies Attachment);
      this.state.acceptWebSocket(server);
      server.send(JSON.stringify({ type: "error", error: rejectReason }));
      server.close(4002, rejectReason);
      return new Response(null, { status: 101, webSocket: client });
    }

    const src = role === "viewer" ? await addressTag(request.headers.get("cf-connecting-ip") ?? "", await this.tagSalt()) : undefined;
    // Where this socket came in: an agent is told it with where the session
    // is served (relayPlace); a viewer is told its own, and nothing more.
    const edge = placeCode(request.headers.get("x-reminal-edge"));
    server.serializeAttachment({ role, authed: false, src, ...(edge ? { edge } : {}) } satisfies Attachment);
    this.state.acceptWebSocket(server);

    // The expiry alarm is pushed out to LIVE_EXPIRY_MS once this connection
    // authenticates (handleAuth), not here: a connection that never does must
    // not keep a used room alive, so a used room with nothing authenticated in
    // it has its expiry armed now. A room never used is left as it is: it holds
    // nothing to clear, so nothing is written for it (any id can be named);
    // a socket there that never authenticates holds nothing either, and is
    // closed when it says anything else or an agent claims the room.
    if ((role === "agent" || role === "tunnel") && !this.authedAgent() && !this.authedTunnel() && (await this.used())) {
      await this.armExpiry();
    }
    return new Response(null, { status: 101, webSocket: client });
  }

  async webSocketMessage(ws: WebSocket, message: string | ArrayBuffer) {
    const attachment = ws.deserializeAttachment() as Attachment;
    if (attachment.rejected) return; // told its error and closing; nothing to send it

    // A proxied WebSocket visitor: forward the frame verbatim to the agent,
    // before any control-message parsing (these are opaque app frames).
    if (attachment.role === "visitor") {
      const tunnel = this.authedTunnel();
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

    // Viewers send JSON text; a binary frame from one is not a message.
    if (attachment.role === "viewer" && typeof message !== "string") return;

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

        if (attachment.role === "viewer" && (parsed.type === "park" || parsed.type === "unpark")) {
          await this.park(ws, attachment, parsed.type === "park");
          return;
        }
        // A parked viewer says nothing to the agent until it unparks.
        if (attachment.role === "viewer" && attachment.parked) return;

        if (attachment.role === "viewer") {
          // A message names its type only as "type", and its source tag only
          // as "src": the agent reads field names without regard to case (and
          // the last spelling wins), and this room acts by the exact ones.
          if (Object.keys(parsed).some((k) => (k !== "type" && k.toLowerCase() === "type") || (k !== "src" && /^[sSſ][rR][cC]$/.test(k)))) return;
          // The source tag is this room's to set, never the client's: set on
          // the handshakes that carry one, removed from everything else.
          if (parsed.type === "own_init") {
            parsed.src = attachment.src ?? "";
            message = JSON.stringify(parsed);
          } else if (parsed.type !== "pake_init" && parsed.type !== "kex_init" && parsed.src !== undefined) {
            delete parsed.src;
            message = JSON.stringify(parsed);
          }
          if (parsed.type === "pake_init" || parsed.type === "kex_init") {
            parsed.src = attachment.src ?? "";
            if (this.agentPresent()) {
              const retry = await this.takeHandshake(attachment.src);
              if (retry > 0) {
                ws.send(JSON.stringify({ type: "pake_busy", ex_id: parsed.ex_id, retry_ms: retry }));
                return;
              }
            }
            message = JSON.stringify(parsed);
          }
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
          if (parsed.type === "tunnel_ws_opened") {
            this.handleTunnelWSOpened(parsed.data ?? "");
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
        if (att?.authed && !att.parked && v.readyState === WebSocket.OPEN) {
          v.send(message);
        }
      }
    } else if (attachment.role === "viewer") {
      // Only to an agent that has authenticated, as the Go relay does.
      this.authedAgent()?.send(message);
    }
    // tunnel sockets don't pass through opaque messages.
  }

  // The room's own bookkeeping for a socket that went, then the close
  // answered (closeBack) — for every role, a rejected socket's too.
  async webSocketClose(ws: WebSocket, code?: number, reason?: string) {
    try {
      await this.onSocketClose(ws, code, reason);
    } finally {
      closeBack(ws, code, reason);
    }
  }

  private async onSocketClose(ws: WebSocket, code?: number, reason?: string) {
    const attachment = ws.deserializeAttachment() as Attachment;
    if (attachment.rejected) return;

    if (attachment.role === "agent") {
      // An agent socket that never authenticated, or one replaced by another
      // that has, leaving changes nothing for the viewers. If none is left at
      // all, the room's expiry starts (or keeps) counting.
      if (!attachment.authed || this.authedAgent(ws)) {
        if (!this.authedAgent(ws) && !this.authedTunnel() && (await this.used())) await this.armExpiry();
        return;
      }
      this.closeParked(); // the agent they would resume is gone
      for (const v of this.getSockets("viewer")) {
        const att = v.deserializeAttachment() as Attachment;
        if (att?.authed && !att.parked && v.readyState === WebSocket.OPEN) {
          v.send(JSON.stringify({ type: "agent_offline" }));
        }
      }
      await armAlarm(this.state, "SessionRoom", Date.now() + this.orphanTTL);
    } else if (attachment.role === "viewer") {
      const remaining = this.getSockets("viewer").filter(v => v !== ws);
      if (remaining.length === 0) {
        await this.state.storage.put("viewerAuthed", false);
      }
      const agent = this.authedAgent();
      // A parked socket was no viewer to the agent: its going changes nothing there.
      if (agent && !attachment.parked) {
        agent.send(JSON.stringify({
          type: "closed",
          error: "viewer disconnected",
          count: remaining.filter(v => !(v.deserializeAttachment() as Attachment)?.parked).length,
        }));
      }
    } else if (attachment.role === "tunnel") {
      // A tunnel socket that never authenticated, or one replaced by another
      // that has, leaving changes nothing for the visitors.
      if (!attachment.authed || this.authedTunnel(ws)) return;
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
      // Release any visitor handshake still held for a subprotocol answer that
      // can no longer come, so it falls back immediately instead of sitting out
      // the full timeout on a tunnel that is already gone.
      for (const streamId of [...this.pendingWSOpens.keys()]) {
        this.settleWSOpen(streamId, null);
      }
      // Every proxied visitor WebSocket rode over this control socket; close them
      // so browsers reconnect instead of hanging on a dead stream.
      for (const v of this.getSockets("visitor")) {
        try { v.close(1011, "reminal: tunnel disconnected"); } catch { /* already closing */ }
      }
      await armAlarm(this.state, "SessionRoom", Date.now() + this.orphanTTL);
    } else if (attachment.role === "visitor") {
      // Visitor hung up: tell the agent to close the backend connection.
      const tunnel = this.authedTunnel();
      if (tunnel?.readyState === WebSocket.OPEN && attachment.streamId) {
        // Carry the browser's close code + reason so the backend learns why the
        // visitor left, instead of just seeing the socket vanish.
        tunnel.send(JSON.stringify({
          type: "tunnel_ws_close",
          data: JSON.stringify({
            stream_id: attachment.streamId,
            ...(typeof code === "number" && code > 0 ? { code } : {}),
            ...(reason ? { reason: String(reason).slice(0, 120) } : {}),
          }),
        }));
      }
    }
  }

  async webSocketError(ws: WebSocket) {
    return this.webSocketClose(ws);
  }

  async alarm() {
    // Only an authenticated agent or tunnel keeps the room, and its expiry
    // stays armed, far off, for when it goes with no close reaching the room.
    // Sockets that never authenticated are closed with it.
    if (this.authedAgent() || this.authedTunnel()) {
      await armAlarm(this.state, "SessionRoom", Date.now() + LIVE_EXPIRY_MS);
      return;
    }
    for (const role of ["agent", "tunnel"]) {
      for (const s of this.getSockets(role)) {
        try { s.close(4001, "authentication required"); } catch { /* already closing */ }
      }
    }
    for (const v of this.getSockets("viewer")) {
      if (v.readyState === WebSocket.OPEN) {
        v.send(JSON.stringify({ type: "closed", error: "agent session expired" }));
        v.close(1000, "expired");
      }
    }
    // Clear the room, but keep who it belongs to until that runs out; the
    // alarm comes back then to clear that too. The alarm count stays with
    // it, or clearing would reset the bound armAlarm keeps.
    const [ownerHash, ownerUntil, arms] = await Promise.all([
      this.state.storage.get<string>("ownerHash"),
      this.state.storage.get<number>("ownerUntil"),
      this.state.storage.get<AlarmArms>(ALARM_ARMS),
    ]);
    await this.state.storage.deleteAll();
    if (ownerHash && ownerUntil && Date.now() < ownerUntil) {
      await this.state.storage.put({ ownerHash, ownerUntil });
      if (arms) await this.state.storage.put(ALARM_ARMS, arms);
      await armAlarm(this.state, "SessionRoom", ownerUntil);
    }
  }

  // ---- auth ----

  // relayPlace is what an agent is told with its auth_ok: {at, edge}, the
  // codes of the location this object runs in and of the one the agent came
  // in at; either left out when it is not known, null when neither is. The
  // object asks Cloudflare's trace page where it is, once, and gives it
  // 400 ms: an authentication never waits on it for longer.
  private async relayPlace(edge?: string): Promise<{ at?: string; edge?: string } | null> {
    if (!this.place) {
      try {
        const res = await fetch("https://www.cloudflare.com/cdn-cgi/trace", { signal: AbortSignal.timeout(400) });
        if (res.ok) this.place = placeFromTrace(await res.text());
      } catch { /* not known this time */ }
    }
    const out: { at?: string; edge?: string } = {};
    if (this.place) out.at = this.place;
    if (edge) out.edge = edge;
    return out.at || out.edge ? out : null;
  }

  private async handleAuth(
    ws: WebSocket,
    attachment: Attachment,
    msg: { type?: string; pin?: string; pin_hash?: string; token?: string },
  ): Promise<string | null> {
    if (msg.type !== "auth") {
      return "authentication required";
    }

    // The PIN-attempt lockout belongs to a forwarded port's visitor gate
    // (handleTunnelAuth); agents and tunnels authenticate here without it.
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
        // Already migrated to a token — nothing else authenticates. Constant-time
        // compare: the token is a high-entropy reattach credential and a plain
        // !== would leak a matching prefix's length by timing.
        if (!timingSafeEqual(token, storedToken)) {
          return "session credentials mismatch";
        }
      } else if (storedPinHash) {
        // Legacy session: require the same pin_hash. If the agent also brought a
        // token, migrate to token-only and drop the offline-crackable pin_hash.
        if (!timingSafeEqual(pinHash, storedPinHash)) {
          return "session credentials mismatch";
        }
        if (token) {
          await this.state.storage.put("token", token);
          await this.state.storage.delete("pinHash");
        }
      } else {
        // No credential stored: a brand-new session, or one whose room was
        // cleared. A cleared room still remembers who it belonged to, and
        // only that agent may have it back.
        const ownerHash = (await this.state.storage.get<string>("ownerHash")) ?? "";
        const ownerUntil = (await this.state.storage.get<number>("ownerUntil")) ?? 0;
        if (ownerHash && Date.now() < ownerUntil) {
          const presented = await credentialHashes(token, pinHash);
          if (!presented.some((h) => timingSafeEqual(h, ownerHash))) {
            return "session credentials mismatch";
          }
        }
        // Register whatever proves control, preferring the token so no
        // PIN-derived value is ever stored.
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
      // The near expiry gives way to the far-off one kept while it is
      // connected (LIVE_EXPIRY_MS); its close brings the near one back.
      await armAlarm(this.state, "SessionRoom", Date.now() + LIVE_EXPIRY_MS);
      // The visitor gate's attempt count is its own; reconnecting leaves it.
      // Remember who this room belongs to, for after it is cleared.
      const owner = await credentialHashes(
        (await this.state.storage.get<string>("token")) ?? "",
        (await this.state.storage.get<string>("pinHash")) ?? "",
      );
      await this.state.storage.put({ ownerHash: owner[0], ownerUntil: Date.now() + this.ownerKeep });
      ws.serializeAttachment({ role: attachment.role, authed: true } satisfies Attachment);
      // The agent is told where the session is served (place.ts).
      const relay = attachment.role === "agent" ? await this.relayPlace(attachment.edge) : null;
      ws.send(JSON.stringify(relay ? { type: "auth_ok", relay } : { type: "auth_ok" }));

      if (attachment.role === "agent") {
        // An agent come (back): parked sockets hold a key from before it.
        this.closeParked();
        for (const v of this.getSockets("viewer")) {
          const att = v.deserializeAttachment() as Attachment;
          if (att?.authed && !att.parked && v.readyState === WebSocket.OPEN) {
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
    ws.serializeAttachment({ role: "viewer", authed: true, src: attachment.src } satisfies Attachment);
    // A viewer is told where its OWN socket came in, which it could find out
    // for itself, and never where the session is served (place.ts).
    ws.send(JSON.stringify(attachment.edge ? { type: "auth_ok", relay: { edge: attachment.edge } } : { type: "auth_ok" }));

    const agent = this.authedAgent();
    if (agent) {
      ws.send(JSON.stringify({ type: "connected" }));
      {
        agent.send(JSON.stringify({
          type: "connected",
          count: this.activeViewers(),
        }));
      }
    } else {
      ws.send(JSON.stringify({ type: "agent_offline" }));
    }
    return null;
  }

  // ---- tunnel: register + request/response correlation ----

  private async handleTunnelRegister(dataJSON: string) {
    let info: { port?: number; pin_hash?: string; public?: boolean; caps?: string[] } = {};
    try {
      info = JSON.parse(dataJSON);
    } catch {
      return;
    }
    const port = typeof info.port === "number" ? info.port : 0;
    const pinHash = info.pin_hash ?? "";
    const isPublic = !!info.public;
    if (!port || !pinHash) return;

    // Keep the auth-cookie signing key STABLE across re-registrations of the
    // same tunnel. The agent re-sends tunnel_register on every reconnect (its
    // relay socket is dropped every few minutes — Cloudflare caps WebSocket
    // duration), and rotating the key each time would invalidate every visitor's
    // auth cookie, bouncing them back to the PIN gate mid-session. So reuse the
    // existing key whenever the PIN is unchanged; only mint a fresh one for a
    // brand-new tunnel or when the PIN actually changes — the latter is exactly
    // when old cookies SHOULD stop granting access (new credentials, or a prior
    // session-ID reused by a different expose).
    const prev = await this.state.storage.get<TunnelMeta>("tunnelMeta");
    let signingKey: string;
    if (prev && prev.signingKey && prev.pinHash === pinHash) {
      signingKey = prev.signingKey;
    } else {
      const keyBytes = new Uint8Array(32);
      crypto.getRandomValues(keyBytes);
      signingKey = toHex(keyBytes);
    }

    // Capabilities are re-advertised on every registration, so read them fresh
    // rather than inheriting from prev — a downgrade must actually take effect.
    const reqChunk = Array.isArray(info.caps) && info.caps.includes("req_chunk");
    const wsSubproto = Array.isArray(info.caps) && info.caps.includes("ws_subproto");
    const longPin = Array.isArray(info.caps) && info.caps.includes("long_pin");

    // A tunnel registered before visitor cookies keeps taking the cookie its
    // visitors already hold, for a while (see visitorAuthed). A new one, or a
    // new PIN, never takes it.
    const legacyUntil = prev && prev.signingKey === signingKey ? prev.legacyUntil : 0;
    const meta: TunnelMeta = { port, pinHash, public: isPublic, signingKey, reqChunk, wsSubproto, legacyUntil, longPin };
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
      // Abort BEFORE delivering the trailing chunk. Enqueuing every remaining
      // byte and only then erroring lets the stream look like it completed
      // normally — the visitor gets a clean short body instead of a failure.
      const earlyEnd = !more && typeof resp.error === "string" && resp.error ? resp.error : null;
      if (earlyEnd) {
        try {
          entry.controller.error(new Error("reminal: upstream ended early: " + earlyEnd));
        } catch { /* already closed/cancelled */ }
        this.pendingTunnelReqs.delete(reqID);
        return;
      }
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

    // Head chunk — carries status + headers (+ any repeated Set-Cookie list).
    clearTimeout(entry.timeout);
    const status = typeof resp.status === "number" ? resp.status : 502;
    const headers: Record<string, string> = resp.headers ?? {};
    const setCookies: string[] | undefined = Array.isArray(resp.set_cookies) ? resp.set_cookies : undefined;
    // Headers the backend sent more than once (Vary, Link, WWW-Authenticate …).
    // The flat `headers` map holds only the first of each; these carry them all.
    const multiHeaders: Record<string, string[]> | undefined =
      resp.multi_headers && typeof resp.multi_headers === "object" ? resp.multi_headers : undefined;

    if (!more) {
      // Single-chunk response (small body, or an older single-message agent).
      this.pendingTunnelReqs.delete(reqID);
      entry.resolve({ status, headers, setCookies, multiHeaders, body: chunk });
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
    entry.resolve({ status, headers, setCookies, multiHeaders, body: stream });
  }

  // ---- tunnel: WebSocket proxy ----

  // handleTunnelWS upgrades a visitor WebSocket and hands the agent a matching
  // stream id so it can dial the local backend. Frames then flow both ways as
  // tunnel_ws_data over the agent's control socket (see webSocketMessage).
  private async handleTunnelWS(request: Request, url: URL, hostMode = false): Promise<Response> {
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
      if (!(await this.visitorAuthed(request, sessionId, meta, hostMode))) {
        return new Response("reminal: authentication required\n", { status: 401 });
      }
      if (fromAnotherTunnel(request)) {
        return new Response("reminal: refused, sent from another site\n", { status: 403 });
      }
    }

    const tunnel = this.authedTunnel();
    if (!tunnel || tunnel.readyState !== WebSocket.OPEN) {
      return new Response("reminal: tunnel offline\n", { status: 503 });
    }

    // Refuse before accepting, so abuse can't force unbounded accept/close churn
    // or tunnel_ws_open amplification through the DO.
    if (this.getSockets("visitor").length >= MAX_VISITOR_SOCKETS) {
      return new Response("reminal: too many connections\n", { status: 503 });
    }

    const streamId = crypto.randomUUID();
    const pair = new WebSocketPair();
    const [client, server] = Object.values(pair);
    server.serializeAttachment({ role: "visitor", authed: true, streamId } satisfies Attachment);
    this.state.acceptWebSocket(server);

    const headers: Record<string, string> = {};
    request.headers.forEach((v, k) => {
      if (k.toLowerCase() === "cookie") {
        // Only in host-mode, where this tunnel owns its own origin. See the
        // note in handleTunnelHttp: in path-mode every tunnel shares the relay
        // origin, so forwarding app cookies would hand one tunnel's backend the
        // cookies another tunnel's app set.
        if (!hostMode) return;
        const kept = stripAuthCookie(v, sessionId);
        if (kept) headers[k] = kept;
        return;
      }
      if (k.toLowerCase().startsWith("x-reminal-")) return; // don't leak internal routing headers
      headers[k] = v;
    });
    // Same as the HTTP path: forward the real public host so the backend's WS
    // upgrade sees a Host matching the Origin the browser sends (apps like
    // UniFi's live portal enforce same-origin on the WebSocket too).
    const wsPublicHost = request.headers.get("x-reminal-public-host");
    if (wsPublicHost) headers["host"] = wsPublicHost;
    // Register BEFORE sending the open so an agent that answers instantly still
    // finds a waiter. Only when a subprotocol was actually offered and the agent
    // can report the backend's pick — otherwise there is nothing to negotiate
    // and the handshake must not wait at all.
    const offeredProto = request.headers.get("Sec-WebSocket-Protocol");
    let openedPromise: Promise<string | null> | null = null;
    if (offeredProto && meta.wsSubproto) {
      openedPromise = new Promise<string | null>((resolve) => {
        const timeout = setTimeout(() => {
          this.pendingWSOpens.delete(streamId);
          resolve(null);
        }, WS_OPEN_TIMEOUT_MS);
        this.pendingWSOpens.set(streamId, { resolve, timeout });
      });
    }
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

    // Complete the handshake with the subprotocol the BACKEND chose. Echoing
    // the client's first offer instead told the browser it was speaking a
    // protocol the backend had not selected — a protocol violation that
    // silently misinterprets frames for any subprotocol-using app (graphql-ws,
    // MQTT-over-WS, ActionCable). An older agent never reports the pick, so
    // there we still fall back to the echo rather than break the handshake.
    const respHeaders: Record<string, string> = {};
    if (offeredProto) {
      let chosen: string | null = null;
      if (openedPromise) chosen = await openedPromise;
      if (chosen === null) chosen = offeredProto.split(",")[0].trim();
      if (chosen) respHeaders["Sec-WebSocket-Protocol"] = chosen;
    }

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
    // A close can arrive because the backend DIAL failed, i.e. before any
    // tunnel_ws_opened. Settle the waiting handshake now instead of letting it
    // sit out the full timeout.
    this.settleWSOpen(streamId, null);
    const sock = this.getVisitorSocket(streamId);
    if (sock) {
      // Pass the backend's close code + reason through so the browser learns
      // WHY the socket closed. Some codes can't be sent from this side; fall
      // back to a plain 1000 rather than leaving the socket hanging.
      const code = typeof m.code === "number" && m.code > 0 ? m.code : 1000;
      const reason = typeof m.reason === "string" ? m.reason.slice(0, 120) : "";
      try {
        sock.close(code, reason);
      } catch {
        try { sock.close(1000, reason); } catch { /* already closing */ }
      }
    }
  }

  // handleTunnelWSOpened carries the subprotocol the backend picked, releasing
  // the visitor's held 101 (see handleTunnelWS).
  private handleTunnelWSOpened(dataJSON: string) {
    let m: any = null;
    try { m = JSON.parse(dataJSON); } catch { return; }
    if (!m?.stream_id) return;
    this.settleWSOpen(m.stream_id, typeof m.subprotocol === "string" ? m.subprotocol : "");
  }

  // settleWSOpen resolves a pending handshake exactly once and clears its timer.
  private settleWSOpen(streamId: string, subprotocol: string | null) {
    const pending = this.pendingWSOpens.get(streamId);
    if (!pending) return;
    this.pendingWSOpens.delete(streamId);
    clearTimeout(pending.timeout);
    pending.resolve(subprotocol);
  }

  private getVisitorSocket(streamId: string): WebSocket | null {
    for (const ws of this.state.getWebSockets()) {
      const att = ws.deserializeAttachment() as Attachment;
      if (att?.role === "visitor" && att.streamId === streamId) return ws;
    }
    return null;
  }

  // ---- tunnel: HTTP proxy ----

  private async handleTunnelHttp(request: Request, url: URL, hostMode = false): Promise<Response> {
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
        headers: pageHeaders(),
      });
    }

    // Where the app "root" and the gate's POST target live: at the origin root
    // in host-mode, under /p/<id>/ in path-mode.
    const base = hostMode ? "" : `/p/${sessionId}`;
    const authAction = `${base}/__auth`;

    // POST __auth — submit PIN.
    if (rest === "/__auth" && request.method === "POST") {
      return this.handleTunnelAuth(request, sessionId, meta, hostMode);
    }

    // Public tunnels skip the gate entirely.
    if (!meta.public) {
      if (!(await this.visitorAuthed(request, sessionId, meta, hostMode))) {
        // In host-mode the visitor's real path is `rest`; in path-mode it's the
        // full /p/<id>/… pathname. Either way keep the query.
        // Only a real page load should get the HTML gate. An app's XHR/fetch
        // would otherwise receive a 200 whose body is our login page: it parses
        // as garbage ("Unexpected token '<'"), and clients that retry rather
        // than treat it as an auth failure sit in a reconnect loop for minutes
        // instead of failing fast. Give those a machine-readable 401.
        const rawWantTo = (hostMode ? rest : url.pathname) + url.search;
        // Same bound as the POST path: this comes from the URL (already capped by
        // the edge), but keep it symmetric so no gate-rendering path can be handed
        // an over-length target.
        const wantTo = rawWantTo.length > MAX_AUTH_TO_BYTES ? `${base}/` : rawWantTo;
        if (!isNavigationRequest(request)) {
          return new Response("reminal: authentication required\n", {
            status: 401,
            headers: { "Content-Type": "text/plain" },
          });
        }
        return new Response(pinGatePage(sessionId, wantTo, "", authAction), {
          status: 200,
          headers: pageHeaders(),
        });
      }
      // The visitor holds the cookie, but the browser sent it on a request
      // another tunnel's page made (see fromAnotherTunnel): refuse it.
      if (fromAnotherTunnel(request)) {
        return new Response("reminal: refused, sent from another site\n", {
          status: 403,
          headers: { "Content-Type": "text/plain" },
        });
      }
    }

    // Forward to the tunnel WS.
    const tunnel = this.authedTunnel();
    if (!tunnel || tunnel.readyState !== WebSocket.OPEN) {
      return new Response("reminal: tunnel offline\n", {
        status: 503,
        headers: { "Content-Type": "text/plain" },
      });
    }

    // Refuse an upload we'd only mangle anyway: the agent caps an assembled
    // request body at the same ceiling, and buffering + chunk-queueing much more
    // than this risks the DO's ~128 MB budget. Check the declared length first so
    // an oversized body is rejected before we read it into memory.
    const declaredLen = Number(request.headers.get("content-length") ?? "");
    if (Number.isFinite(declaredLen) && declaredLen > MAX_REQUEST_BODY_BYTES) {
      return new Response("reminal: request body too large\n", {
        status: 413,
        headers: { "Content-Type": "text/plain" },
      });
    }
    const reqID = crypto.randomUUID();
    // Missing or wrong Content-Length (chunked upload): read no further than
    // the ceiling, and no more than the uploads already being read leave.
    const bodyArr = await readBody(request.body, MAX_REQUEST_BODY_BYTES, this.uploads);
    if (bodyArr === "large") {
      return new Response("reminal: request body too large\n", {
        status: 413,
        headers: { "Content-Type": "text/plain" },
      });
    }
    if (bodyArr === "busy") {
      return new Response("reminal: too many uploads at once, try again in a moment\n", {
        status: 503,
        headers: { "Content-Type": "text/plain", "Retry-After": "5" },
      });
    }
    // Held until it is sent on (or refused below): then the next may be read.
    const sent = () => this.uploads.give(bodyArr.byteLength);
    const headers: Record<string, string> = {};
    request.headers.forEach((v, k) => {
      const lk = k.toLowerCase();
      // The agent re-adds X-Forwarded-* itself; we shouldn't trust
      // what Cloudflare passed (already added cf-* headers etc.).
      if (lk === "cookie") {
        // Forward the app's own cookies (minus ours) so it can keep the visitor
        // logged in after its login sets one — but ONLY in host-mode, where the
        // tunnel owns its own origin (port-<id>.reminal.app) and the browser
        // scopes cookies to it. In path-mode every tunnel shares the relay
        // origin, so an app cookie set at Path=/ by one tunnel is sent to all of
        // them; forwarding it would leak one tunnel's session cookie to another
        // tunnel's backend operator. There we keep dropping the whole header.
        if (!hostMode) return;
        const kept = stripAuthCookie(v, sessionId);
        if (kept) headers[k] = kept;
        return;
      }
      if (lk.startsWith("x-reminal-")) return; // don't leak our internal routing headers
      headers[k] = v;
    });
    // The Host header does not survive Cloudflare's DO fetch, so the agent would
    // send the backend Host: 127.0.0.1:<port>. index.ts stashed the real public
    // host in x-reminal-public-host; forward it as the Host so the backend sees a
    // Host that matches the Origin/Referer the browser sends — webmin/UniFi
    // reject a login POST whose Referer/Origin host differs from the served host.
    const publicHost = request.headers.get("x-reminal-public-host");
    if (publicHost) headers["host"] = publicHost;

    const promise = new Promise<{ status: number; headers: Record<string, string>; setCookies?: string[]; multiHeaders?: Record<string, string[]>; body: ReadableStream<Uint8Array> | Uint8Array }>((resolve) => {
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

    // A big upload can't ride one WS message (the DO caps a message near 1 MiB).
    // Send the head tunnel_req with the first chunk; if there's more, mark
    // body_more and stream the rest as tunnel_req_body chunks. 700 KiB raw →
    // ~933 KiB base64.
    const REQ_BODY_CHUNK = 700 * 1024;
    // The head tunnel_req co-locates method+url+HEADERS with the first body
    // chunk. Request headers are not tiny — a big Cookie or many forwarded
    // headers reach ~100 KiB — and the inner JSON is escaped again into the
    // outer message (its quotes double), so a full 700 KiB first chunk beside
    // large headers can push the head message past the DO cap and drop the whole
    // control socket (the symmetric hazard the response head chunk guards against
    // on the agent). Measure the head's non-body overhead against the OUTER wire
    // message and shrink the first chunk to fit; normal requests keep the full
    // 700 KiB so nothing changes for them.
    const MAX_TUNNEL_MSG = 1000 * 1024; // safe ceiling under the ~1 MiB DO cap
    const headOverhead = JSON.stringify({
      type: "tunnel_req",
      data: JSON.stringify({
        req_id: reqID,
        method: request.method,
        url: rest + (url.search ?? ""),
        headers,
        body: "",
        body_more: true,
      }),
    }).length;
    if (headOverhead >= MAX_TUNNEL_MSG) {
      // Headers alone exceed the per-message cap: no body split can make the head
      // fit, and sending it would drop the tunnel. Fail just this request.
      sent();
      return new Response(
        "reminal: request headers too large to relay\n",
        { status: 431, headers: { "Content-Type": "text/plain" } },
      );
    }
    // base64 of N raw bytes is 4*ceil(N/3); the body base64 needs no JSON
    // escaping, so its wire cost is exactly that. Largest first chunk that fits.
    let firstChunkMax = REQ_BODY_CHUNK;
    const bodyBudget = MAX_TUNNEL_MSG - headOverhead;
    if (bodyBudget < Math.ceil(REQ_BODY_CHUNK / 3) * 4) {
      firstChunkMax = Math.max(0, Math.floor(bodyBudget / 4) * 3);
    }
    const bodyMore = bodyArr.byteLength > firstChunkMax;
    if (bodyMore && !meta.reqChunk) {
      // Agent predates chunked request bodies: it would ignore the follow-on
      // tunnel_req_body messages and hand the backend a TRUNCATED body — a
      // silently corrupted upload. Refuse instead, and say why.
      sent();
      return new Response(
        "reminal: this upload is too large for the reminal version running on the target machine.\nRun `reminal upgrade` there, then retry.\n",
        { status: 413, headers: { "Content-Type": "text/plain" } },
      );
    }
    // send() throws if the socket closed since the readyState check above, and it
    // can: the body read awaits in between, and under load the control socket
    // does drop. An escaping throw reaches the visitor as an opaque
    // "error code: 1101" and strands the pendingTunnelReqs entry registered just
    // above, holding its 30s timer for a request that will never be answered.
    // Same guard, same reasoning, as the WS-open path above.
    try {
      tunnel.send(JSON.stringify({
        type: "tunnel_req",
        data: JSON.stringify({
          req_id: reqID,
          method: request.method,
          url: rest + (url.search ?? ""),
          headers,
          body: bytesToBase64(bodyMore ? bodyArr.subarray(0, firstChunkMax) : bodyArr),
          ...(bodyMore ? { body_more: true } : {}),
        }),
      }));
      if (bodyMore) {
        for (let off = firstChunkMax; off < bodyArr.byteLength; off += REQ_BODY_CHUNK) {
          const end = Math.min(off + REQ_BODY_CHUNK, bodyArr.byteLength);
          tunnel.send(JSON.stringify({
            type: "tunnel_req_body",
            data: JSON.stringify({
              req_id: reqID,
              body: bytesToBase64(bodyArr.subarray(off, end)),
              more: end < bodyArr.byteLength,
            }),
          }));
        }
      }
      sent();
    } catch {
      sent();
      const pending = this.pendingTunnelReqs.get(reqID);
      if (pending) {
        clearTimeout(pending.timeout);
        this.pendingTunnelReqs.delete(reqID);
      }
      return new Response("reminal: tunnel offline\n", {
        status: 503,
        headers: { "Content-Type": "text/plain" },
      });
    }

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
    // In host-mode the app is at the origin root, so absolute-PATH Locations
    // ("/folder/") already resolve correctly and are left untouched — only a
    // loopback absolute-URL is rewritten, down to its bare path.
    for (const key of Object.keys(out.headers)) {
      if (key.toLowerCase() !== "location") continue;
      const v = out.headers[key];
      if (!v) continue;
      if (!hostMode && v.startsWith("/") && !v.startsWith(base + "/") && !v.startsWith("//")) {
        out.headers[key] = base + v;
        continue;
      }
      const loop = loopbackLocationPath(v);
      if (loop !== null) {
        out.headers[key] = base + sameOriginPath(loop); // base === "" in host-mode
        continue;
      }
      // App redirected to our own public host but with a stray port (webmin
      // appends :10000) — collapse it to a same-origin path the tunnel serves.
      const self = selfHostLocationPath(v, publicHost);
      if (self !== null) {
        out.headers[key] = base + sameOriginPath(self);
      }
    }

    // Build the response headers, then append each Set-Cookie individually — a
    // plain object collapses repeats to one, which would drop all but one of an
    // app's login cookies. `Headers.append` preserves them, and Cloudflare emits
    // one Set-Cookie line each to the visitor.
    //
    // Every name and value here came off the wire from the app, and the Headers
    // methods THROW on anything that isn't a valid HTTP token. Go's parser is
    // laxer than the Fetch spec, so it hands us names a browser never would —
    // "X-Bad Name" with a space gets through it untouched. Passing the whole
    // object to `new Headers()` meant ONE such header threw and took the entire
    // response with it, reaching the visitor as an opaque "error code: 1101".
    // Set them one at a time and drop only the offenders.
    const respHeaders = new Headers();
    const putHeader = (name: string, value: string, append = false) => {
      try {
        if (append) respHeaders.append(name, value);
        else respHeaders.set(name, value);
      } catch {
        // Invalid per the Fetch spec — a bad token in the name, or a control
        // character in the value. It isn't forwardable, but the rest of the
        // response still is.
      }
    };
    for (const [name, value] of Object.entries(out.headers)) putHeader(name, value);
    // Restore headers the backend sent more than once. out.headers carried only
    // the first value of each, so drop that one and re-append the full list —
    // otherwise a dropped Vary dimension lets a cache serve the wrong variant.
    for (const [name, values] of Object.entries(out.multiHeaders ?? {})) {
      if (!Array.isArray(values) || values.length === 0) continue;
      try {
        respHeaders.delete(name);
      } catch {
        continue; // name isn't a valid token; its values aren't forwardable either
      }
      for (const v of values) putHeader(name, v, true);
    }
    // Only the list below reaches the visitor as Set-Cookie. A Set-Cookie in
    // the flat or the repeated headers comes from an agent that isn't ours
    // (ours sends them in setCookies), and would skip forwardableCookie.
    respHeaders.delete("Set-Cookie");
    if (hostMode) {
      for (const c of out.setCookies ?? []) {
        const fc = forwardableCookie(c, sessionId);
        if (fc) putHeader("Set-Cookie", fc, true);
      }
    } else {
      // Path-mode is symmetric with the inbound gate above: every tunnel shares
      // the relay origin, and since we never forward cookies back to the app, a
      // Set-Cookie here can only pollute the shared jar — including letting one
      // tunnel's app write a reminal_auth_<otherId> at Path=/ that shadows
      // another tunnel's gate cookie and forces its visitor to re-authenticate.
      respHeaders.delete("Set-Cookie");
    }
    // The Response constructor only accepts 200-599. A backend answering with
    // anything outside that — a bare 101 from an app that hijacks the socket,
    // say — made this line THROW, and an unhandled Worker exception reaches the
    // visitor as an opaque "error code: 1101" with no hint of the cause.
    // Surface it as a readable 502 instead: the tunnel is fine, it is the
    // upstream response that cannot be represented over HTTP to the visitor.
    if (!Number.isInteger(out.status) || out.status < 200 || out.status > 599) {
      if (out.body instanceof ReadableStream) {
        // Discarding the stream without cancelling would pin the agent-side
        // request entry until the stall watchdog fires (same reasoning as the
        // 204/205/304 and HEAD paths below).
        try { out.body.cancel(); } catch { /* already closed */ }
      }
      return new Response(
        `reminal: upstream returned status ${out.status}, which cannot be proxied\n`,
        { status: 502, headers: { "Content-Type": "text/plain" } },
      );
    }
    // 204, 205 and 304 carry no body by definition, and the Response constructor
    // throws if handed one — the same opaque "error code: 1101" by another route.
    // Go only suppresses the body for 204 and 304, so a 205 arrives with its
    // bytes intact; and a 304 keeps whatever Content-Length its app sent, which
    // now contradicts the empty body. Drop the body and the stale framing.
    if (out.status === 204 || out.status === 205 || out.status === 304) {
      respHeaders.delete("Content-Length");
      respHeaders.delete("Transfer-Encoding");
      if (out.body instanceof ReadableStream) {
        // Let the agent-side stream go, or the request entry stays pinned until
        // the stall watchdog fires.
        try { out.body.cancel(); } catch { /* already closed */ }
      }
      return new Response(null, { status: out.status, headers: respHeaders });
    }
    // A HEAD reply reports the size a GET would return, and sends no body. Handing
    // the constructor the empty body we received makes the runtime compute the
    // length from THAT — zero — throwing away the size the backend reported, which
    // is the one thing the method exists to deliver. Pass no body and keep the
    // header the agent forwarded for exactly this case.
    if (request.method === "HEAD") {
      if (out.body instanceof ReadableStream) {
        try { out.body.cancel(); } catch { /* already closed */ }
      }
      return new Response(null, { status: out.status, headers: respHeaders });
    }
    return new Response(out.body as BodyInit, { status: out.status, headers: respHeaders });
  }

  private async handleTunnelAuth(request: Request, sessionId: string, meta: TunnelMeta, hostMode = false): Promise<Response> {
    // In host-mode the tunnel owns the whole origin, so the cookie + redirects
    // are scoped to "/"; in path-mode they're confined to /p/<id>/.
    const root = hostMode ? "/" : `/p/${sessionId}/`;
    const authAction = hostMode ? "/__auth" : `/p/${sessionId}/__auth`;
    // Reject an oversized body BEFORE formData() buffers it. This endpoint is
    // unauthenticated, so without this a stranger could POST megabytes at a gated
    // tunnel and lean on the DO's memory. A declared length over the cap can only
    // be abuse — a real login form is well under it. A chunked body with no
    // Content-Length is read no further than the cap either (readBody below).
    const authLen = Number(request.headers.get("content-length") ?? "");
    if (Number.isFinite(authLen) && authLen > MAX_AUTH_BODY_BYTES) {
      return new Response(pinGatePage(sessionId, root, "Incorrect PIN.", authAction), {
        status: 401,
        headers: pageHeaders(),
      });
    }
    // formData() REJECTS on a body it cannot parse as a form — a JSON, XML,
    // text/html or octet-stream POST, or a multipart without a usable boundary.
    // Unguarded, that rejection escapes the Durable Object and the visitor gets
    // an opaque "error code: 1101" 500 — and unlike the other throws of this
    // shape (un-proxyable status, illegal header, body on a 205), this one needs
    // no cooperating backend: any stranger can fire it at a gated tunnel with one
    // curl. The gate page is the honest answer, the same as a wrong PIN: whatever
    // that body was, it did not carry a usable one.
    let form: FormData;
    try {
      const read = await readBody(request.body, MAX_AUTH_BODY_BYTES);
      if (typeof read === "string") throw new Error(read);
      form = await new Response(read, { headers: { "Content-Type": request.headers.get("content-type") ?? "" } }).formData();
    } catch {
      return new Response(pinGatePage(sessionId, root, "Incorrect PIN.", authAction), {
        status: 401,
        headers: pageHeaders(),
      });
    }
    const pin = String(form.get("pin") ?? "");
    const rawTo = String(form.get("to") ?? root);
    // Bound the redirect target: over the cap it can only be abuse (see
    // MAX_AUTH_TO_BYTES), so fall back to root rather than render it.
    const to = rawTo.length > MAX_AUTH_TO_BYTES ? root : rawTo;

    // Serialize the lockout check + compare + record through authGate (see its
    // declaration): concurrent attempts must not race the counter or run bcrypt
    // in parallel. Acquire by chaining onto the previous attempt; release in a
    // finally so a throw can't wedge the gate.
    const src = (await addressTag(request.headers.get("cf-connecting-ip") ?? "", await this.tagSalt())) ?? NO_SOURCE;
    const prevAttempt = this.authGate;
    let releaseGate!: () => void;
    this.authGate = new Promise<void>((r) => { releaseGate = r; });
    let gateResponse: Response | null;
    try {
      await prevAttempt;
      const now = Date.now();
      const stored = await this.state.storage.get(["gateSources", "gateTunnel", "gateKnown"]);
      const g: GateState = {
        sources: (stored.get("gateSources") as Record<string, Strikes> | undefined) ?? {},
        tunnel: stored.get("gateTunnel") as Strikes | undefined,
        known: (stored.get("gateKnown") as Record<string, number> | undefined) ?? {},
      };
      const wait = gateWait(g, src, now, !meta.longPin);
      if (wait > 0) {
        gateResponse = new Response(pinGatePage(sessionId, to, `Too many failed attempts. Try again in ${waitText(wait)}.`, authAction), {
          status: 429,
          headers: pageHeaders({ "Retry-After": String(Math.ceil(wait / 1000)) }),
        });
      } else {
        const { compare } = await import("bcryptjs");
        if (!pin || !(await compare(normalizePIN(pin), meta.pinHash))) {
          const w = gateWrong(g, src, now);
          await this.state.storage.put({ gateSources: w.sources, gateTunnel: w.tunnel });
          gateResponse = new Response(pinGatePage(sessionId, to, "Incorrect PIN.", authAction), {
            status: 401,
            headers: pageHeaders(),
          });
        } else {
          const r = gateRight(g, src, now);
          await this.state.storage.put({ gateSources: r.sources, gateKnown: r.known });
          await this.state.storage.delete(["failedAttempts", "lockedUntil"]); // the counter before these
          gateResponse = null; // success — fall through to issue the auth cookie
        }
      }
    } finally {
      releaseGate();
    }
    if (gateResponse) return gateResponse;

    const cookieVal = await visitorCookie(meta.signingKey, Date.now());
    const cookie =
      `${authCookieName(sessionId, hostMode)}=${cookieVal}; ` +
      `Path=${root}; ` +
      `Max-Age=${VISITOR_MAX_AGE_S}; ` +
      `HttpOnly; Secure; SameSite=Lax`;
    // Defensive: only redirect same-origin. Host-mode allows any absolute path
    // on the tunnel's own origin; path-mode stays confined to /p/<id>/.
    //
    // A leading "/" followed by "/" OR "\" is a network-path reference: the
    // browser reads what follows as the authority and leaves the origin. Checking
    // only for "//" missed "/\evil.com" (and "/\/evil.com"), because browsers
    // treat "\" as "/" in http(s) URLs — so that resolved to https://evil.com,
    // an open redirect off the trusted gate. Reject a second char of "/" or "\".
    const safeTo = hostMode
      ? (/^\/(?![/\\])/.test(to) ? to : "/")
      : (to.startsWith(`/p/${sessionId}/`) ? to : `/p/${sessionId}/`);
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

  // takeHandshake takes one PIN handshake from the address tag src, and
  // returns 0, or how many milliseconds until one is available. Kept in
  // storage, not memory, because the room sleeps between messages.
  private async takeHandshake(src?: string): Promise<number> {
    if (!src) return 0;
    return this.takeToken("hs:" + src, HANDSHAKE_BURST, HANDSHAKE_REFILL_MS);
  }

  private async takeToken(key: string, burst: number, refillMs: number): Promise<number> {
    const now = Date.now();
    const stored = await this.state.storage.get<{ t: number; at: number }>(key);
    if (!stored && Math.random() < 0.05) await this.pruneHandshakes(now);
    const b = stored ?? { t: burst, at: now };
    const tokens = Math.min(burst, b.t + (now - b.at) / refillMs);
    if (tokens < 1) {
      await this.state.storage.put(key, { t: tokens, at: now });
      return Math.ceil((1 - tokens) * refillMs);
    }
    await this.state.storage.put(key, { t: tokens - 1, at: now });
    return 0;
  }

  // activeViewers counts the viewers the agent has: authed and not parked.
  private activeViewers(): number {
    return this.getSockets("viewer").filter(v => {
      const a = v.deserializeAttachment() as Attachment;
      return a?.authed && !a.parked;
    }).length;
  }

  // closeParked closes the parked viewer sockets: the agent they would
  // resume has left or been replaced, and its key is not theirs.
  private closeParked() {
    for (const v of this.getSockets("viewer")) {
      const a = v.deserializeAttachment() as Attachment;
      if (a?.parked) {
        try { v.close(4002, "agent changed"); } catch { /* already closing */ }
      }
    }
  }

  // park parks or unparks the viewer socket that sent it — never another —
  // and tells the agent its count of viewers. Paced per address; unparking
  // needs an agent to go back to. Refused, the viewer dials afresh.
  private async park(ws: WebSocket, attachment: Attachment, park: boolean) {
    const retry = await this.takeToken("pk:" + (attachment.src ?? ""), PARK_BURST, PARK_REFILL_MS);
    if (retry > 0) {
      ws.send(JSON.stringify({ type: "park_refused", error: "too many switches just now" }));
      return;
    }
    const agent = this.authedAgent();
    if (!park && !agent) {
      ws.send(JSON.stringify({ type: "park_refused", error: "agent not connected" }));
      return;
    }
    const changed = !!attachment.parked !== park;
    ws.serializeAttachment({ ...attachment, parked: park } satisfies Attachment);
    if (changed && agent) {
      agent.send(JSON.stringify(park
        ? { type: "closed", error: "viewer parked", count: this.activeViewers() }
        : { type: "connected", count: this.activeViewers() }));
    }
    ws.send(JSON.stringify({ type: park ? "parked" : "unparked" }));
  }

  // pruneHandshakes drops counts that have refilled completely.
  private async pruneHandshakes(now: number) {
    const stale: string[] = [];
    for (const [prefix, burst, refill] of [["hs:", HANDSHAKE_BURST, HANDSHAKE_REFILL_MS], ["pk:", PARK_BURST, PARK_REFILL_MS]] as const) {
      const all = await this.state.storage.list<{ t: number; at: number }>({ prefix });
      for (const [k, v] of all) {
        if (v.t + (now - v.at) / refill >= burst) stale.push(k);
      }
    }
    if (stale.length) await this.state.storage.delete(stale);
  }

  // tagSalt is this room's random salt for address tags.
  private async tagSalt(): Promise<string> {
    let salt = await this.state.storage.get<string>("hsSalt");
    if (!salt) {
      salt = [...crypto.getRandomValues(new Uint8Array(16))].map((b) => b.toString(16).padStart(2, "0")).join("");
      await this.state.storage.put("hsSalt", salt);
    }
    return salt;
  }

  // used says whether this room has held an authenticated agent or tunnel:
  // the first keeps its credential (token or pinHash), and every one keeps
  // who the room belongs to (ownerHash) until the room is cleared for good.
  // A room with none of them has nothing to clear.
  private async used(): Promise<boolean> {
    const kept = await this.state.storage.get(["ownerHash", "token", "pinHash"]);
    return kept.size > 0;
  }

  // armExpiry sets the room's expiry unless one at least as near is set. A
  // used room keeps a far-off alarm for its owner record (OWNER_KEEP_MS), or
  // for an agent it last saw connected (LIVE_EXPIRY_MS); neither may count as
  // expiry being armed. alarm() restores the first afterwards, and an agent
  // or tunnel authenticating the second.
  private async armExpiry() {
    const at = await this.state.storage.getAlarm();
    const due = Date.now() + this.orphanTTL;
    if (at === null || at > due) await armAlarm(this.state, "SessionRoom", due);
  }

  private agentPresent(): boolean {
    return this.authedAgent() !== null;
  }

  // authedTunnel is the open tunnel socket that has authenticated (other than
  // except), or null. Only that one receives visitors' traffic.
  private authedTunnel(except?: WebSocket): WebSocket | null {
    for (const t of this.getSockets("tunnel")) {
      if (t === except || t.readyState !== WebSocket.OPEN) continue;
      if ((t.deserializeAttachment() as Attachment)?.authed) return t;
    }
    return null;
  }

  // authedAgent is the open agent socket that has authenticated (other than
  // except), or null.
  private authedAgent(except?: WebSocket): WebSocket | null {
    for (const a of this.getSockets("agent")) {
      if (a === except || a.readyState !== WebSocket.OPEN) continue;
      if ((a.deserializeAttachment() as Attachment)?.authed) return a;
    }
    return null;
  }

  private async loadMeta() {
    const [agentAuthed, viewerAuthed] = await Promise.all([
      this.state.storage.get<boolean>("agentAuthed"),
      this.state.storage.get<boolean>("viewerAuthed"),
    ]);
    return { agentAuthed: !!agentAuthed, viewerAuthed: !!viewerAuthed };
  }

  // visitorAuthed reports whether request carries a cookie that lets its
  // visitor past the PIN gate: a visitor cookie still within its age, or, on a
  // tunnel registered before visitor cookies, the one cookie its visitors were
  // given then, until VISITOR_MAX_AGE_S after this relay first saw it. The
  // grace keeps those visitors in without letting that cookie last forever.
  private async visitorAuthed(request: Request, sessionId: string, meta: TunnelMeta, hostMode: boolean): Promise<boolean> {
    const cookies = parseCookies(request.headers.get("Cookie") ?? "");
    const value = cookies[authCookieName(sessionId, hostMode)] ?? "";
    if (!value) return false;
    const now = Date.now();
    if (await visitorCookieOK(meta.signingKey, value, now)) return true;
    if (meta.legacyUntil === 0) return false;
    if (!timingSafeEqual(value, await legacyCookie(meta.signingKey))) return false;
    let until = meta.legacyUntil;
    if (until === undefined) {
      until = now + VISITOR_MAX_AGE_S * 1000;
      const stored = await this.state.storage.get<TunnelMeta>("tunnelMeta");
      if (stored && stored.signingKey === meta.signingKey && stored.legacyUntil === undefined) {
        await this.state.storage.put("tunnelMeta", { ...stored, legacyUntil: until });
      }
    }
    return now < until;
  }
}

// ---- shared crypto / encoding helpers ----

// credentialHashes is what a room keeps of an agent's credential once it is
// cleared: SHA-256 of the token, or of the legacy pin_hash. The first entry is
// the one to store (the token when there is one).
async function credentialHashes(token: string, pinHash: string): Promise<string[]> {
  const h = async (label: string, v: string) =>
    [...new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(label + v)))]
      .map((b) => b.toString(16).padStart(2, "0")).join("");
  const out: string[] = [];
  if (token) out.push(await h("token:", token));
  if (pinHash) out.push(await h("pin_hash:", pinHash));
  return out;
}

// addressTag is what a room keeps of a viewer's address: a salted hash of the
// address, or of its /64 for IPv6, enough to count its handshakes.
async function addressTag(ip: string, salt: string): Promise<string | undefined> {
  if (!ip) return undefined;
  const d = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(salt + "|" + addressGroup(ip)));
  return [...new Uint8Array(d)].slice(0, 12).map((b) => b.toString(16).padStart(2, "0")).join("");
}

// addressGroup is the address, or the /64 an IPv6 address is in.
export function addressGroup(ip: string): string {
  if (!ip.includes(":")) return ip;
  // IPv4-mapped IPv6, in any of its spellings, groups by the IPv4 address.
  const dotted = /^(?:0*:)*(?:0*:)?ffff:(\d+\.\d+\.\d+\.\d+)$/i.exec(ip) ?? /^::ffff:(\d+\.\d+\.\d+\.\d+)$/i.exec(ip);
  if (dotted) return dotted[1];
  const hexMapped = /^(?:0*:)*(?:0*:)?ffff:([0-9a-f]{1,4}):([0-9a-f]{1,4})$/i.exec(ip);
  if (hexMapped) {
    const hi = parseInt(hexMapped[1], 16), lo = parseInt(hexMapped[2], 16);
    return [hi >> 8, hi & 255, lo >> 8, lo & 255].join(".");
  }
  const [head, tail = ""] = ip.split("::");
  const h = head ? head.split(":") : [];
  const t = tail ? tail.split(":") : [];
  const full = ip.includes("::") ? [...h, ...Array(8 - h.length - t.length).fill("0"), ...t] : h;
  return full.slice(0, 4).map((x) => (parseInt(x || "0", 16) || 0).toString(16)).join(":") + "::/64";
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
  // WHATWG URL normalises decimal/hex/octal/short IPv4 and expanded ::1 into the
  // forms below, so those are covered — but an IPv4-MAPPED IPv6 loopback
  // (http://[::ffff:127.0.0.1]/) normalises to [::ffff:7f00:1] and would slip
  // through, leaking a redirect that points the visitor at their OWN machine.
  // The v4-mapped 127.0.0.0/8 range is exactly [::ffff:7f00:*]..[::ffff:7fff:*]
  // (127 = 0x7f); match both its hex and (defensively) dotted serialisations.
  const isLoopback =
    h === "localhost" || h === "::1" || h === "[::1]" || h.startsWith("127.") ||
    h.startsWith("[::ffff:7f") || h.startsWith("[::ffff:127.");
  return isLoopback ? u.pathname + u.search : null;
}

// forwardableCookie is an app's Set-Cookie as the visitor may receive it:
// with no Domain attribute, so it is host-only for the tunnel's own origin; or
// "" when it names reminal's own gate cookie, which no app may set.
//
// Every port-<id>.reminal.app is the same site, so a cookie one tunnel's app
// set for Domain=reminal.app would reach every other tunnel's app too: an
// app could log a visitor into another tunnel's app as someone else. Host-only
// is strictly the narrower scope, and can never widen one. It also mends the
// commonest Domain an app sends: one that believes it serves 127.0.0.1 emits
// "Domain=127.0.0.1", which the browser rightly refuses on port-<id>.reminal.app
// (the login appears to succeed and is gone by the next request). Cookies
// carrying the __Host- prefix become valid rather than invalid here, since that
// prefix forbids Domain outright. The rest of the cookie is left byte for byte.
function forwardableCookie(cookie: string, sessionId: string): string {
  const parts = cookie.split(";");
  const eq0 = parts[0].indexOf("=");
  const name = (eq0 < 0 ? "" : parts[0].slice(0, eq0)).trim();
  if (isAuthCookie(name, sessionId)) return "";
  const kept = parts.filter((part, i) => {
    if (i === 0) return true; // the name=value pair itself is never an attribute
    const eq = part.indexOf("=");
    const attr = (eq < 0 ? part : part.slice(0, eq)).trim().toLowerCase();
    return attr !== "domain";
  });
  return kept.join(";");
}

// sameOriginPath collapses leading slashes on a rewritten Location path. A
// URL's pathname keeps a doubled slash ("https://host//evil.com/x" →
// "//evil.com/x"), and in host-mode we emit it with an empty base — so without
// this the browser would read "//evil.com/x" as a protocol-relative URL and
// leave the origin entirely: an open redirect on a *.reminal.app host.
function sameOriginPath(p: string): string {
  return "/" + p.replace(/^\/+/, "");
}

// selfHostLocationPath returns the bare path+query of an absolute Location that
// points back at our OWN public host — regardless of port. Apps that build
// self-referential redirects from the Host we hand them can tack on their own
// listening port (webmin emits https://<public-host>:10000/), which the browser
// can't reach. Any redirect to our public host is same-origin, so collapse it to
// a path the tunnel actually serves.
function selfHostLocationPath(loc: string, publicHost: string | null): string | null {
  if (!publicHost) return null;
  let u: URL;
  try {
    u = new URL(loc);
  } catch {
    return null;
  }
  // Parse rather than split on ":" — a bracketed IPv6 literal ("[::1]:8443")
  // would otherwise compare as "[" and silently never match.
  let want: string;
  try {
    want = new URL(`http://${publicHost}`).hostname.toLowerCase();
  } catch {
    return null;
  }
  return u.hostname.toLowerCase() === want ? u.pathname + u.search : null;
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

// isNavigationRequest reports whether this looks like a browser loading a page,
// as opposed to an app's XHR/fetch/subresource. Only the former should be shown
// the HTML PIN gate. Sec-Fetch-Mode is authoritative in every current browser;
// the Accept sniff covers clients that don't send it.
function isNavigationRequest(request: Request): boolean {
  const mode = request.headers.get("sec-fetch-mode");
  if (mode) return mode === "navigate";
  if (request.headers.get("x-requested-with")) return false; // classic XHR
  return (request.headers.get("accept") ?? "").includes("text/html");
}

// authCookieName is the PIN gate's cookie. On a tunnel's own origin it carries
// the __Host- prefix, so the browser keeps only one set by that very host, at
// Path=/, with no Domain: another port-<id>.reminal.app (same site, and an
// attacker's tunnel can be one) cannot set or shadow it. Under /p/<id>/ on the
// relay's host it is confined to that path, which __Host- does not allow.
function authCookieName(sessionId: string, hostMode: boolean): string {
  return (hostMode ? "__Host-" : "") + AUTH_COOKIE_PREFIX + sessionId;
}

// isAuthCookie: either form of the gate's cookie for this tunnel. Browsers
// match the __Host- prefix without regard to case.
function isAuthCookie(name: string, sessionId: string): boolean {
  const n = name.toLowerCase();
  const base = (AUTH_COOKIE_PREFIX + sessionId).toLowerCase();
  return n === base || n === "__host-" + base;
}

// fromAnotherTunnel reports a request that another tunnel's page made to this
// one. Every port-<id>.reminal.app is the same site, so the gate cookie
// (SameSite=Lax) goes along on it: an attacker's tunnel could otherwise post
// forms and run fetches against a visitor's other tunnels as them. Browsers
// mark such a request Sec-Fetch-Site: same-site (a tunnel's own requests are
// same-origin). Only a plain link followed, a GET navigation, still passes,
// as it would from any other site.
function fromAnotherTunnel(request: Request): boolean {
  if (request.headers.get("sec-fetch-site") !== "same-site") return false;
  return request.method !== "GET" || request.headers.get("sec-fetch-mode") !== "navigate";
}

function parseCookies(header: string): Record<string, string> {
  const out: Record<string, string> = {};
  if (!header) return out;
  for (const part of header.split(";")) {
    const eq = part.indexOf("=");
    if (eq < 0) continue;
    const name = part.slice(0, eq).trim();
    // The first of a repeated name wins. A browser sends the most specific
    // first (the longest Path, then the oldest); a later one of the same name
    // is a cookie some other host of the site set at a wider scope, to shadow
    // the visitor's own.
    if (Object.prototype.hasOwnProperty.call(out, name)) continue;
    const raw = part.slice(eq + 1).trim();
    // decodeURIComponent THROWS on a malformed escape, and this runs over every
    // cookie the visitor carries — not just ours. One app cookie containing a
    // bare "%" (a percentage in a value) would otherwise throw here and 500 the
    // whole request, breaking the tunnel for that visitor until they cleared it.
    try {
      out[name] = decodeURIComponent(raw);
    } catch {
      out[name] = raw;
    }
  }
  return out;
}

// Remove ONLY reminal's own PIN-gate cookie from a Cookie header, forwarding
// every other cookie to the app untouched. The old behaviour dropped the whole
// header — which also stripped the app's OWN session cookie, so an app like
// webmin couldn't stay logged in after its login set a cookie (the visitor's
// browser sent it back, but the relay never passed it on). Values are kept as
// their original substrings (no decode/re-encode) so nothing is corrupted.
function stripAuthCookie(header: string, sessionId: string): string {
  if (!header) return "";
  return header
    .split(";")
    .map((p) => p.trim())
    .filter((p) => {
      if (!p) return false;
      const eq = p.indexOf("=");
      const name = (eq < 0 ? p : p.slice(0, eq)).trim();
      return !isAuthCookie(name, sessionId);
    })
    .join("; ");
}

// ---- HTML pages ----

// pageHeaders are the headers of a page the relay itself builds for a tunnel
// (its PIN gate, its not-found page) — never of the forwarded app's own
// responses. The page is not to be shown inside another site's frame, where a
// visitor could be led to type the PIN into it unawares, and it sends no
// referrer, so the tunnel's address goes nowhere a link on it leads.
function pageHeaders(extra: Record<string, string> = {}): Record<string, string> {
  return {
    "Content-Type": "text/html; charset=utf-8",
    "Content-Security-Policy": "frame-ancestors 'none'",
    "X-Frame-Options": "DENY",
    "Referrer-Policy": "no-referrer",
    ...extra,
  };
}

function pinGatePage(sessionId: string, to: string, errorMsg: string, authAction: string): string {
  const escTo = escapeHtml(to);
  const escAction = escapeHtml(authAction);
  const escErr = errorMsg ? `<p class="err">${escapeHtml(errorMsg)}</p>` : "";
  // Inline page; reads URL fragment (#p=<PIN>) and auto-submits so the
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
<form class="card" method="POST" action="${escAction}" id="f">
  <h1><span>re</span>minal</h1>
  <p class="sub">PIN required to reach <code>${sessionId}</code></p>
  ${escErr}
  <input type="hidden" name="to" value="${escTo}">
  <input name="pin" type="text" autocapitalize="characters" autocorrect="off" spellcheck="false" autocomplete="off" autofocus placeholder="PIN" required>
  <button type="submit">Continue</button>
  <p class="foot">A reminal port forward sits behind this page.</p>
</form>
<script>
  // Auto-fill from URL fragment (#p=<PIN>) and submit. Fragments never
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

// waitText is a wait as the gate says it: in minutes, or hours once it is
// over one.
function waitText(ms: number): string {
  const min = Math.ceil(ms / 60000);
  if (min < 60) return min === 1 ? "1 minute" : `${min} minutes`;
  const h = Math.ceil(min / 60);
  return h === 1 ? "1 hour" : `${h} hours`;
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
