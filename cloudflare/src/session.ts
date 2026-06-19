import type { Attachment } from "./types";

const MAX_ATTEMPTS = 5;
const LOCKOUT_MS = 5 * 60 * 1000;

// How long a room is kept alive after the agent disconnects, giving the
// same agent a chance to reattach across a network blip.
const ORPHAN_TTL_MS = 10 * 60 * 1000;

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
      // Allow viewer to attach as long as the room was set up by an
      // authenticated agent — even if the agent is briefly offline.
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

    // Cancel any pending orphan-cleanup alarm now that someone is here.
    if (role === "agent") {
      await this.state.storage.deleteAlarm();
    }

    return new Response(null, { status: 101, webSocket: client });
  }

  async webSocketMessage(ws: WebSocket, message: string | ArrayBuffer) {
    const attachment = ws.deserializeAttachment() as Attachment;

    if (typeof message === "string") {
      let parsed: { type?: string; pin?: string; pin_hash?: string } | null = null;
      try {
        parsed = JSON.parse(message);
      } catch {
        // encrypted payload — fall through to forward
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
        // data / resize / resume / unknown control — forward to the other side
      }
    }

    if (!attachment.authed) {
      ws.close(4001, "authentication required");
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
      // Notify the viewer (if any) that the agent is temporarily gone,
      // but keep the room and credentials so the agent can reattach.
      const viewer = this.getSocket("viewer");
      if (viewer?.readyState === WebSocket.OPEN) {
        viewer.send(JSON.stringify({ type: "agent_offline" }));
      }
      // Schedule cleanup if the agent never returns.
      await this.state.storage.setAlarm(Date.now() + ORPHAN_TTL_MS);
    } else if (attachment.role === "viewer") {
      await this.state.storage.put("viewerAuthed", false);
      const agent = this.getSocket("agent");
      if (agent?.readyState === WebSocket.OPEN) {
        agent.send(JSON.stringify({ type: "closed", error: "viewer disconnected" }));
      }
    }
  }

  async webSocketError(ws: WebSocket) {
    // Treat errors like a clean close for cleanup purposes.
    return this.webSocketClose(ws);
  }

  async alarm() {
    // Orphan-room TTL: if the agent never reattached, kick the viewer and
    // wipe storage. If the agent is back, we leave everything alone.
    if (this.getSocket("agent")) {
      return;
    }
    const viewer = this.getSocket("viewer");
    if (viewer?.readyState === WebSocket.OPEN) {
      viewer.send(JSON.stringify({ type: "closed", error: "agent session expired" }));
      viewer.close(1000, "expired");
    }
    await this.state.storage.deleteAll();
  }

  private async handleAuth(
    ws: WebSocket,
    attachment: Attachment,
    msg: { type?: string; pin?: string; pin_hash?: string },
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
      // For a reattach, the agent must present the same pin_hash that
      // originally created the room. This prevents hijacking.
      const storedPinHash = (await this.state.storage.get<string>("pinHash")) ?? "";
      if (storedPinHash && storedPinHash !== msg.pin_hash) {
        return "session credentials mismatch";
      }
      await this.state.storage.put("pinHash", msg.pin_hash);
      await this.state.storage.put("agentAuthed", true);
      await this.resetFailures();
      ws.serializeAttachment({ role: "agent", authed: true } satisfies Attachment);
      ws.send(JSON.stringify({ type: "auth_ok" }));

      // If a viewer is already authed and waiting, tell them the agent is back.
      const viewer = this.getSocket("viewer");
      if (viewer?.readyState === WebSocket.OPEN) {
        const att = viewer.deserializeAttachment() as Attachment;
        if (att?.authed) {
          viewer.send(JSON.stringify({ type: "agent_online" }));
        }
      }
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
    ws.serializeAttachment({ role: "viewer", authed: true } satisfies Attachment);
    ws.send(JSON.stringify({ type: "auth_ok" }));

    // Tell the viewer the current presence of the agent.
    const agent = this.getSocket("agent");
    if (agent?.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: "connected" }));
      const att = agent.deserializeAttachment() as Attachment;
      if (att?.authed) {
        agent.send(JSON.stringify({ type: "connected" }));
      }
    } else {
      ws.send(JSON.stringify({ type: "agent_offline" }));
    }
    return null;
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
