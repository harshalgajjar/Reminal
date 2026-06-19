# reminal

Remote terminal access from any browser or terminal — no SSH keys, no IP addresses, no port forwarding.

Run `reminal` on your laptop, open the link on any device, and control your terminal. A free Cloudflare relay handles the connection — nothing to expose on your network.

```
  Your laptop                 Cloudflare relay              Any device
  ┌─────────────┐            ┌─────────────┐              ┌─────────────┐
  │  reminal    │◄──WSS─────►│  Workers +  │◄────WSS─────►│  browser or │
  │  (PTY/shell)│            │  Durable Obj│              │  reminal -c │
  └─────────────┘            └─────────────┘              └─────────────┘
```

## Install

```bash
brew tap harshalgajjar/reminal
brew install reminal
```

Or build from source (Go 1.22+):

```bash
./scripts/build.sh
sudo cp dist/reminal /usr/local/bin/
```

## Usage — zero config

Just run it. If you're online, it connects to the hosted relay automatically.

```bash
reminal
```

```
  reminal — remote terminal

  Session:  K7M2NP
  Open:     https://reminal-relay.workers.dev/?s=K7M2NP
  Connect:  reminal --connect K7M2NP

  Waiting for connection... (Ctrl+C to stop)
```

From any other device:

- **Browser:** open the URL above
- **Terminal:** `reminal --connect K7M2NP`

That's it. No env vars, no relay setup, no ports.

## Deploy the relay (one time, free)

The hosted relay lives in `cloudflare/` — Cloudflare Workers + Durable Objects (free tier).

```bash
cd cloudflare
npm install
npx wrangler login
npm run deploy
```

Update `DefaultCloudRelay` / `DefaultCloudWeb` in `internal/config/config.go` with your workers.dev URL, then rebuild. See [cloudflare/README.md](cloudflare/README.md).

## Local development

```bash
# Terminal 1
reminal relay

# Terminal 2
REMINAL_LOCAL=1 reminal

# Terminal 3 or browser
REMINAL_LOCAL=1 reminal --connect <session_id>
# or http://localhost:8080/?s=<session_id>
```

## Commands

| Command | Description |
|---------|-------------|
| `reminal` | Share this terminal session |
| `reminal --connect <id>` | Connect to a remote session from your terminal |
| `reminal relay [port]` | Start local relay (dev only) |
| `reminal version` | Print version |

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `REMINAL_RELAY` | Cloudflare relay URL | Override relay WebSocket base URL |
| `REMINAL_WEB` | Cloudflare web URL | Override web UI URL shown to user |
| `REMINAL_LOCAL` | — | Set to `1` to use localhost relay |
| `SHELL` | `$SHELL` or `/bin/zsh` | Shell to spawn |

## Security

reminal uses defense in depth — you trust Cloudflare to route packets, not to read your terminal:

| Layer | Protection |
|-------|------------|
| **Session ID** | 8 random characters (~1 trillion combinations) |
| **PIN** | 6-digit code required to connect — useless without it |
| **Rate limiting** | 5 wrong PINs → 5-minute lockout per session |
| **E2E encryption** | AES-256-GCM; key derived from PIN + session ID via HKDF |
| **Relay visibility** | Cloudflare only sees encrypted blobs, not keystrokes or output |
| **Transport** | WSS/TLS in production |

**What you still control:**
- Stop `reminal` when done (Ctrl+C) — session dies immediately
- Never share your PIN in screenshots or chat — share it separately from the session ID
- Anyone with **both** session ID and PIN gets full shell access while the session is active

**Not a replacement for SSH** on production servers with sensitive data, but safe for personal remote access to your own machine.

## License

MIT
