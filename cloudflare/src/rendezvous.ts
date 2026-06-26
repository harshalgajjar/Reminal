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
//     offline. Each paste is one LIVE, rate-bounded guess.
//   - At most MAX_PASTE_ATTEMPTS paste pairings are allowed per code; the
//     cap (plus the source going away) burns the offer. A few attempts
//     tolerate a typo without making a short code brute-forceable online.
//   - The offer is also burned the moment the SOURCE socket closes — a
//     successful stream ends with the source closing, and a give-up does
//     too, so a code is never reusable after its source is gone.
//   - A burned/expired/never-existed code all yield the SAME merged
//     "code is either too old or invalid" message, so the relay never
//     confirms whether a given code was real.
//   - A server-side TTL alarm caps how long an un-pasted offer lingers.
//
// This is why a ~40-bit code is safe here where it wouldn't be for a
// store-and-forward drop.

const RV_TTL_MS = 60 * 60 * 1000; // hard server cap; the source may close earlier
const MAX_PASTE_ATTEMPTS = 5; // online guesses before the code is burned

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
    const attempts = (await this.state.storage.get<number>("attempts")) ?? 0;
    if (attempts >= MAX_PASTE_ATTEMPTS) {
      // Out of guesses — burn the offer and drop the source too.
      await this.state.storage.put("consumed", true);
      try { source.close(4404, "too many attempts"); } catch { /* best-effort */ }
      return this.reject(client, server, "paste", 4404, "code is either too old or invalid");
    }
    if (this.getSocket("paste")) {
      return this.reject(client, server, "paste", 4409, "a paste is already in progress");
    }
    // Count this guess BEFORE accepting so a racing second paste can't slip
    // past the cap.
    await this.state.storage.put("attempts", attempts + 1);
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
      // Source gone → offer is dead for good. A close after streaming =
      // success; a mid-transfer drop = failure the user recovers from by
      // re-running `reminal copy`. Either way: burned. Tell any paired
      // paste to stop waiting.
      const paste = this.getSocket("paste");
      if (paste && paste.readyState === WebSocket.OPEN) {
        try { paste.close(1000, "source closed"); } catch { /* best-effort */ }
      }
      await this.state.storage.put("consumed", true);
    }
    // A paste leaving (e.g. a wrong-code attempt) does NOT close the source:
    // it stays available for the next attempt until the cap or TTL.
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
