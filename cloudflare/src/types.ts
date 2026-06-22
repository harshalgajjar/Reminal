export type Attachment = {
  role: "agent" | "viewer" | "tunnel";
  authed: boolean;
  // rejected sockets exist only to deliver a structured error message before
  // closing; webSocketClose must skip the normal presence-cleanup path for
  // them so they don't disturb the legitimate peer.
  rejected?: boolean;
};

// TunnelMeta is persisted in DO storage once the tunnel agent registers
// itself. signingKey is generated server-side and used to HMAC the auth
// cookie so the worker can verify cookies without round-tripping to the
// agent on every request.
export type TunnelMeta = {
  port: number;
  pinHash: string;
  public: boolean;
  signingKey: string; // hex-encoded 32 bytes
};
