import type { Attachment } from "./types";

const MAX_ATTEMPTS = 5;
const LOCKOUT_MS = 5 * 60 * 1000;

export class SessionRoom {
  private state: DurableObjectState;

  constructor(state: DurableObjectState) {
    this.state = state;
  }

  async fetch(request: Request): Promise<Response> {
    const url = new URL(request.url);
    const parts = url.pathname.split("/").filter(Boolean);
    const role = parts[2]?.toLowerCase();

    if (request.headers.get("Upgrade") !== "websocket") {
      return new Response("Expected WebSocket", { status: 426 });
    }

    if (role !== "agent" && role !== "viewer") {
      return new Response("invalid role", { status: 400 });
    }

    const meta = await this.loadMeta();

    if (role === "viewer") {
      if (!meta.agentAuthed) {
        return new Response("session not found or not ready", { status: 404 });
      }
      if (this.getSocket("viewer")) {
        return new Response("viewer already connected", { status: 409 });
      }
    } else if (this.getSocket("agent")) {
      return new Response("agent already connected", { status: 409 });
    }

    const pair = new WebSocketPair();
    const [client, server] = Object.values(pair);
    server.serializeAttachment({ role, authed: false } satisfies Attachment);
    this.state.acceptWebSocket(server);

    return new Response(null, { status: 101, webSocket: client });
  }

  async webSocketMessage(ws: WebSocket, message: string | ArrayBuffer) {
    const attachment = ws.deserializeAttachment() as Attachment;

    if (typeof message === "string") {
      try {
        const msg = JSON.parse(message) as { type: string; pin?: string; pin_hash?: string };

        if (!attachment.authed) {
          const err = await this.handleAuth(ws, attachment, msg);
          if (err) {
            ws.send(JSON.stringify({ type: "error", error: err }));
            ws.close(4001, err);
          }
          return;
        }

        if (msg.type === "ping") {
          ws.send(JSON.stringify({ type: "pong" }));
          return;
        }
      } catch {
        // encrypted payload
      }
    }

    if (!attachment.authed) {
      ws.close(4001, "authentication required");
      return;
    }

    const meta = await this.loadMeta();
    if (!meta.viewerAuthed) {
      return;
    }

    const targetRole = attachment.role === "agent" ? "viewer" : "agent";
    const target = this.getSocket(targetRole);
    if (target?.readyState === WebSocket.OPEN) {
      target.send(message);
    }
  }

  async webSocketClose(ws: WebSocket) {
    const attachment = ws.deserializeAttachment() as Attachment;
    if (attachment.role === "agent") {
      const viewer = this.getSocket("viewer");
      if (viewer) {
        viewer.send(JSON.stringify({ type: "closed", error: "agent disconnected" }));
        viewer.close();
      }
      await this.state.storage.deleteAll();
    } else if (attachment.role === "viewer") {
      await this.state.storage.put("viewerAuthed", false);
    }
  }

  private async handleAuth(
    ws: WebSocket,
    attachment: Attachment,
    msg: { type: string; pin?: string; pin_hash?: string },
  ): Promise<string | null> {
    if (msg.type !== "auth") {
      return "authentication required";
    }

    const meta = await this.loadMeta();
    if (meta.lockedUntil && Date.now() < meta.lockedUntil) {
      return "too many failed attempts — try again in a few minutes";
    }

    if (attachment.role === "agent") {
      if (!msg.pin_hash) {
        return "pin_hash required";
      }
      await this.state.storage.put("pinHash", msg.pin_hash);
      await this.state.storage.put("agentAuthed", true);
      await this.resetFailures();
      ws.serializeAttachment({ role: "agent", authed: true });
      ws.send(JSON.stringify({ type: "auth_ok" }));
      return null;
    }

    if (!msg.pin) {
      return "pin required";
    }
    const pinHash = (await this.state.storage.get<string>("pinHash")) ?? "";
    if (!pinHash) {
      return "session not ready";
    }

    const { compare } = await import("bcryptjs");
    if (!(await compare(msg.pin, pinHash))) {
      await this.recordFailure();
      return "incorrect PIN";
    }

    await this.state.storage.put("viewerAuthed", true);
    await this.resetFailures();
    ws.serializeAttachment({ role: "viewer", authed: true });
    ws.send(JSON.stringify({ type: "auth_ok" }));
    this.notifyConnected();
    return null;
  }

  private notifyConnected() {
    const msg = JSON.stringify({ type: "connected" });
    this.getSocket("agent")?.send(msg);
    this.getSocket("viewer")?.send(msg);
  }

  private getSocket(role: string): WebSocket | null {
    for (const ws of this.state.getWebSockets()) {
      const att = ws.deserializeAttachment() as Attachment;
      if (att?.role === role) {
        return ws;
      }
    }
    return null;
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
