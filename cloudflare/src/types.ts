export type Attachment = {
  role: "agent" | "viewer";
  authed: boolean;
  // rejected sockets exist only to deliver a structured error message before
  // closing; webSocketClose must skip the normal presence-cleanup path for
  // them so they don't disturb the legitimate peer.
  rejected?: boolean;
};
