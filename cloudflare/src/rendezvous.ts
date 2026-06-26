// RendezvousRoom brokers a single `reminal copy` → `reminal paste` transfer.
//
// It is deliberately BLIND: it pairs a `source` WebSocket with a `paste`
// WebSocket (keyed by the short code, which is the DO name) and relays
// frames between them verbatim. The code-authenticated X25519 handshake
// (see internal/crypto/kex.go) runs end-to-end through this room, so the
// relay never learns the code, the transfer key, the filename, or the
// bytes — only ciphertext transits, and nothing is stored at rest.
//
// Security model for a SHORT code:
//   - The source must be online, so there is no stored ciphertext to attack
//     offline. A paste is one LIVE guess.
//   - The offer is burned on the FIRST paste pairing (consumed = true) and
//     also when the source socket closes. So a code is good for exactly one
//     attempt; a mistype is spent and the user re-runs `reminal copy`.
//   - A burned/expired/never-existed code all yield the SAME merged
//     "code is either too old or invalid" message, so the relay never
//     confirms whether a given code was real.
//   - A server-side TTL alarm caps how long an un-pasted offer lingers.
//
// This is why a ~40-bit code is safe here where it wouldn't be for a
// store-and-forward drop.

const RV_TTL_MS = 60 * 60 * 1000; // hard server cap; the source may close earlier

type RvRole = "source" | "paste";
interface RvAttachment {
  role: RvRole;
  rejected?: boolean;
}

export class RendezvousRoom {
  private state: DurableObjectState;

  constructor(state: DurableObjectState) {
    this.state = state;
  }

  async fetch(request: Request): Promise<Response> {
    const url = new URL(request.url);
    const parts = url.pathname.split("/").filter(Boolean); // ["rv", <code>, <role>]
    const role = parts[2]?.toLowerCase();

    if (request.headers.get("Upgrade") !== "websocket") {
      return new Response("Expected WebSocket", { status: 426 });
    }
    if (role !== "source" && role !== "paste") {
      return new Response("invalid role", { status: 400 });
    }

    const consumed = !!(await this.state.storage.get<boolean>("consumed"));
    const pair = new WebSocketPair();
    const [client, server] = Object.values(pair);

    if (role === "source") {
      if (consumed) {
        return this.reject(client, server, "source", 4404, "code is either too old or invalid");
      }
      if (this.getSocket("source")) {
        // A live source already holds this code — the caller should pick
        // another code and retry. Distinct from the consumed/expired case.
        return this.reject(client, server, "source", 4409, "code already in use");
      }
      server.serializeAttachment({ role: "source" } satisfies RvAttachment);
      this.state.acceptWebSocket(server);
      // Arm the lifetime cap from first source connect.
      await this.state.storage.setAlarm(Date.now() + RV_TTL_MS);
      return new Response(null, { status: 101, webSocket: client });
    }

    // role === "paste"
    const source = this.getSocket("source");
    if (consumed || !source || source.readyState !== WebSocket.OPEN) {
      return this.reject(client, server, "paste", 4404, "code is either too old or invalid");
    }
    if (this.getSocket("paste")) {
      return this.reject(client, server, "paste", 4409, "a paste is already in progress");
    }
    // Burn-on-pairing: one live guess per code. Set BEFORE accepting so a
    // racing second paste can't also pair, and a mistyped code is spent
    // (re-run `reminal copy` for a fresh one).
    await this.state.storage.put("consumed", true);
    server.serializeAttachment({ role: "paste" } satisfies RvAttachment);
    this.state.acceptWebSocket(server);
    return new Response(null, { status: 101, webSocket: client });
  }

  // reject accepts the socket only long enough to deliver a close frame with
  // a reason the CLI can surface, then leaves no live socket behind (the
  // rejected attachment is skipped by getSocket).
  private reject(client: WebSocket, server: WebSocket, role: RvRole, code: number, reason: string): Response {
    server.serializeAttachment({ role, rejected: true } satisfies RvAttachment);
    this.state.acceptWebSocket(server);
    try {
      server.send(JSON.stringify({ type: "error", error: reason }));
      server.close(code, reason);
    } catch { /* best-effort */ }
    return new Response(null, { status: 101, webSocket: client });
  }

  async webSocketMessage(ws: WebSocket, message: string | ArrayBuffer) {
    const att = ws.deserializeAttachment() as RvAttachment;
    if (!att || att.rejected) return;

    // Answer keepalive pings locally without bothering the peer.
    if (typeof message === "string" && message.length < 64) {
      try {
        const parsed = JSON.parse(message);
        if (parsed?.type === "ping") {
          ws.send(JSON.stringify({ type: "pong" }));
          return;
        }
      } catch { /* not JSON — fall through to relay it */ }
    }

    // Blind passthrough to the peer.
    const peer = this.getSocket(att.role === "source" ? "paste" : "source");
    if (peer && peer.readyState === WebSocket.OPEN) {
      peer.send(message);
    }
  }

  async webSocketClose(ws: WebSocket) {
    const att = ws.deserializeAttachment() as RvAttachment;
    if (!att || att.rejected) return;

    if (att.role === "source") {
      // Source gone → offer is dead. By protocol the source only closes
      // AFTER it has received the paste's TypeCopyAck, which the paste sends
      // only after writing the whole file — so by the time we get here the
      // paste already has everything and there's nothing in flight to
      // truncate. Closing the paste now is what unblocks it (it's parked on
      // a read waiting for exactly this).
      const paste = this.getSocket("paste");
      if (paste && paste.readyState === WebSocket.OPEN) {
        try { paste.close(1000, "transfer complete"); } catch { /* best-effort */ }
      }
      await this.state.storage.put("consumed", true);
    } else if (att.role === "paste") {
      // Paste gone (e.g. a wrong-code attempt that failed the handshake):
      // nudge a source still waiting on key-confirmation so it doesn't hang
      // until TTL. This is a message, not a close, and the source isn't
      // receiving a stream at this point — so there's no truncation race.
      const source = this.getSocket("source");
      if (source && source.readyState === WebSocket.OPEN) {
        try { source.send(JSON.stringify({ type: "error", error: "paste closed" })); } catch { /* best-effort */ }
      }
    }
  }

  async webSocketError(ws: WebSocket) {
    return this.webSocketClose(ws);
  }

  async alarm() {
    for (const ws of this.state.getWebSockets()) {
      try { ws.close(1000, "expired"); } catch { /* best-effort */ }
    }
    await this.state.storage.deleteAll();
  }

  private getSocket(role: RvRole): WebSocket | null {
    for (const ws of this.state.getWebSockets()) {
      const att = ws.deserializeAttachment() as RvAttachment;
      if (att?.role === role && !att.rejected) return ws;
    }
    return null;
  }
}
