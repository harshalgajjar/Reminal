// Phone alerts: the relay's part is deliberately small. A machine seals the
// alert end-to-end to the phone's own push keys (RFC 8291) before it ever
// reaches us, so all this does is add the VAPID signature every push service
// requires and forward the opaque blob. Nothing is stored: no list of devices,
// no alert text, no log line — the Worker cannot read what it forwards and
// keeps no record that it did.
//
// Why the signature has to happen here: a browser holds ONE push subscription
// per site, bound to one VAPID key, so every machine must push under the same
// key. That key belongs to the site, so it lives in the Worker as a secret.

export interface PushEnv {
  // Base64url of the uncompressed P-256 public key (65 bytes). Handed to the
  // page as applicationServerKey when it subscribes.
  VAPID_PUBLIC?: string;
  // The matching private key as a JWK (JSON string). A secret, never a var.
  VAPID_PRIVATE_JWK?: string;
}

// Only the real push services. Without this the endpoint would make the
// Worker an open HTTP proxy for anyone willing to POST to it.
const PUSH_HOSTS = [
  /^fcm\.googleapis\.com$/,
  /^([a-z0-9-]+\.)*push\.apple\.com$/,
  /^([a-z0-9-]+\.)*push\.services\.mozilla\.com$/,
  /^([a-z0-9-]+\.)*notify\.windows\.com$/,
];

function allowedEndpoint(raw: unknown): URL | null {
  if (typeof raw !== "string" || raw.length > 1024) return null;
  let u: URL;
  try {
    u = new URL(raw);
  } catch {
    return null;
  }
  if (u.protocol !== "https:" || u.port !== "") return null;
  return PUSH_HOSTS.some((re) => re.test(u.hostname)) ? u : null;
}

// Per-isolate rate limit, per subscription. Isolates are many and short-lived,
// so this is a brake on a runaway loop, not a quota — the push services
// enforce their own. A machine's watcher sends a handful of alerts a day.
const RATE_WINDOW_MS = 60_000;
const RATE_MAX = 20;
const recent = new Map<string, number[]>();

function rateOK(key: string, now: number): boolean {
  const hits = (recent.get(key) || []).filter((t) => now - t < RATE_WINDOW_MS);
  if (hits.length >= RATE_MAX) {
    recent.set(key, hits);
    return false;
  }
  hits.push(now);
  recent.set(key, hits);
  if (recent.size > 5000) recent.clear(); // never grow without bound
  return true;
}

function b64url(bytes: ArrayBuffer | Uint8Array): string {
  const u8 = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
  let s = "";
  for (const b of u8) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function b64ToBytes(s: string): Uint8Array<ArrayBuffer> {
  const bin = atob(s.replace(/-/g, "+").replace(/_/g, "/"));
  const out = new Uint8Array(new ArrayBuffer(bin.length));
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

let signingKey: CryptoKey | null = null;
const jwtCache = new Map<string, { jwt: string; exp: number }>();

async function vapidJWT(env: PushEnv, audience: string, nowSec: number): Promise<string> {
  const hit = jwtCache.get(audience);
  if (hit && hit.exp - nowSec > 3600) return hit.jwt;
  if (!signingKey) {
    signingKey = await crypto.subtle.importKey(
      "jwk",
      JSON.parse(env.VAPID_PRIVATE_JWK as string),
      { name: "ECDSA", namedCurve: "P-256" },
      false,
      ["sign"],
    );
  }
  const exp = nowSec + 12 * 3600;
  const enc = new TextEncoder();
  const head = b64url(enc.encode(JSON.stringify({ typ: "JWT", alg: "ES256" })));
  const body = b64url(enc.encode(JSON.stringify({ aud: audience, exp, sub: "https://reminal.app" })));
  // WebCrypto's ECDSA signature is already the raw r||s form JWS wants.
  const sig = await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, signingKey, enc.encode(head + "." + body));
  const jwt = head + "." + body + "." + b64url(sig);
  jwtCache.set(audience, { jwt, exp });
  return jwt;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json", "cache-control": "no-store" },
  });
}

// GET /push/key — the public half, for the page's subscribe call.
export function handlePushKey(env: PushEnv): Response {
  if (!env.VAPID_PUBLIC || !env.VAPID_PRIVATE_JWK) return json({ error: "alerts are not set up on this relay" }, 501);
  return json({ key: env.VAPID_PUBLIC });
}

// POST /push {endpoint, payload (base64 aes128gcm), ttl?, urgency?}
// Answers {status} — the push service's own status — so the machine can tell
// "delivered" from "this phone is gone, forget it" (404/410).
export async function handlePush(request: Request, env: PushEnv): Promise<Response> {
  if (request.method !== "POST") return json({ error: "POST only" }, 405);
  if (!env.VAPID_PUBLIC || !env.VAPID_PRIVATE_JWK) return json({ error: "alerts are not set up on this relay" }, 501);
  let req: { endpoint?: unknown; payload?: unknown; ttl?: unknown; urgency?: unknown };
  try {
    req = await request.json();
  } catch {
    return json({ error: "bad request" }, 400);
  }
  const ep = allowedEndpoint(req.endpoint);
  if (!ep) return json({ error: "not a known push service" }, 400);
  if (typeof req.payload !== "string") return json({ error: "missing payload" }, 400);
  let payload: Uint8Array<ArrayBuffer>;
  try {
    payload = b64ToBytes(req.payload);
  } catch {
    return json({ error: "payload is not base64" }, 400);
  }
  // 4096 is the limit every push service shares. 103 is the aes128gcm header
  // (86) plus the GCM tag (16) plus the record delimiter (1): anything shorter
  // cannot be a sealed message.
  if (payload.length > 4096 || payload.length < 103) return json({ error: "payload size" }, 400);
  const now = Date.now();
  if (!rateOK(ep.href, now)) return json({ error: "too many alerts; slow down" }, 429);

  const ttl = typeof req.ttl === "number" && req.ttl >= 0 && req.ttl <= 86400 ? Math.floor(req.ttl) : 3600;
  const urgency = ["very-low", "low", "normal", "high"].includes(req.urgency as string) ? (req.urgency as string) : "normal";
  const jwt = await vapidJWT(env, ep.origin, Math.floor(now / 1000));
  const res = await fetch(ep.href, {
    method: "POST",
    headers: {
      TTL: String(ttl),
      Urgency: urgency,
      "Content-Encoding": "aes128gcm",
      "Content-Type": "application/octet-stream",
      Authorization: `vapid t=${jwt}, k=${env.VAPID_PUBLIC}`,
    },
    body: payload,
  });
  // Drain without reading into memory we keep; the body is the service's
  // diagnostic text, which we do not relay.
  await res.body?.cancel();
  return json({ status: res.status });
}
