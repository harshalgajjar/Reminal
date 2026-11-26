export type Attachment = {
  role: "agent" | "viewer" | "tunnel" | "visitor";
  authed: boolean;
  // rejected sockets exist only to deliver a structured error message before
  // closing; webSocketClose must skip the normal presence-cleanup path for
  // them so they don't disturb the legitimate peer.
  rejected?: boolean;
  // streamId is set only on role === "visitor" sockets — a proxied WebSocket
  // through `reminal expose`. It correlates the visitor's socket with the
  // backend connection the agent dialed, so frames route to the right peer.
  streamId?: string;
  // src, on a viewer socket, is a hash of the address it connected from,
  // for counting its PIN handshakes (SessionRoom.allowHandshake).
  src?: string;
  // edge, on an agent socket: the code of the Cloudflare location it came
  // in at, told back to the agent with where the session is served.
  edge?: string;
  // parked, on a viewer socket: kept warm by a viewer that has switched to
  // another session (a "park" frame). It is sent nothing of the agent's and
  // is not counted as a viewer until it unparks.
  parked?: boolean;
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
  // Agent advertised "req_chunk" at registration: it can reassemble a request
  // body split across tunnel_req + tunnel_req_body. Absent on every agent
  // released before that support landed — those silently drop the follow-on
  // chunks, so the relay must refuse an oversized upload instead of chunking it.
  reqChunk?: boolean;
  // Agent advertised "ws_subproto": it reports the backend's chosen WebSocket
  // subprotocol (tunnel_ws_opened), so the relay can hold the visitor's 101 and
  // echo the real pick. Absent on older agents, where the relay falls back to
  // echoing the client's first offer rather than stalling the handshake.
  wsSubproto?: boolean;
  // Until when (ms) the tunnel still takes the single cookie every visitor got
  // before visitor cookies. 0: never (registered since). Absent: registered
  // before, not yet asked; the relay sets it the first time it is.
  legacyUntil?: number;
  // Agent advertised "long_pin": its PIN is long enough that wrong PINs are
  // held per source only (see gateWait). Absent on a short PIN.
  longPin?: boolean;
};
