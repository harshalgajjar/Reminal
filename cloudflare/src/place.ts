// Where on Cloudflare's network a session is served, for a diagnostic.
//
// A session's object is made in the Cloudflare location of the first request
// for its id and stays there for the life of the id, while its agent and its
// viewers each come in wherever is nearest to them. A machine that has moved
// since (a laptop, a session of weeks) is then served through a place that is
// near nothing, and every key typed goes by way of it. That was a guess from
// outside. The object tells its AGENT, with the answer to its authentication,
// the code of the location it runs in and of the one the agent came in at
// (three letters each, as Cloudflare names them). Only the agent is told: its
// authentication is the session's own secret, where a viewer's socket is
// answered before the PIN is checked, and where a machine is served from is
// not for whoever knows a session's id. A viewer is told only the location
// its own socket came in at, which it could find out for itself.

// placeCode is v when it is a Cloudflare location's code, or "".
export function placeCode(v: unknown): string {
  return typeof v === "string" && /^[A-Z]{3}$/.test(v) ? v : "";
}

// placeFromTrace reads the location's code out of Cloudflare's own trace
// page (lines of key=value, one of them colo=XXX), or "".
export function placeFromTrace(text: string): string {
  for (const line of String(text).split("\n")) {
    if (line.startsWith("colo=")) return placeCode(line.slice(5).trim());
  }
  return "";
}
