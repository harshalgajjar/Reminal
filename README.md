# reminal

Remote terminal access from any browser or terminal — more secure than SSH, zero setup.

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

  Session:  K7M2NP4Q
  PIN:      482916
  Open:     https://reminal-relay.reminal.workers.dev/?s=K7M2NP4Q
  Connect:  reminal --connect K7M2NP4Q --pin 482916

  Scan to join from your phone:

  [QR code printed here — encodes the URL + PIN]

  Waiting for connection... (Ctrl+C to stop)
```

From any other device:

- **Phone:** scan the QR — auto-joins with the PIN, no typing
- **Browser:** open the URL above, enter the PIN
- **Terminal:** `reminal --connect K7M2NP4Q --pin 482916`

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
REMINAL_LOCAL=1 reminal --connect <session_id> --pin <pin>
# or http://localhost:8080/?s=<session_id>
```

## Commands

| Command | Description |
|---------|-------------|
| `reminal` | Share this terminal session |
| `reminal --connect <id> --pin <pin>` | Connect to a remote session from your terminal |
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

reminal is built to be **as secure as SSH — and safer by default**.

SSH leaves port 22 open, stores long-lived keys on disk, and depends on you configuring everything correctly. reminal takes a different approach: **nothing to expose, nothing permanent to steal, encryption end-to-end**.

| Layer | Protection |
|-------|------------|
| **No open ports** | Your machine initiates outbound connections only — nothing to scan or brute-force |
| **Ephemeral credentials** | Session ID + PIN expire when you quit — no keys sitting on disk for years |
| **Dual-factor connect** | Attacker needs both session ID (~1 trillion combos) and 6-digit PIN |
| **Rate limiting** | 5 wrong PINs → 5-minute lockout |
| **E2E encryption** | AES-256-GCM; key derived from PIN + session ID via HKDF |
| **Relay-blind** | Cloudflare routes packets but cannot read your terminal — only encrypted blobs |
| **Transport** | WSS/TLS in production |

**Why reminal beats SSH for remote access:**

| | reminal | SSH |
|---|---------|-----|
| Attack surface | No listening port | Port 22 exposed to the internet |
| Credentials | Temporary, per-session | Permanent keys/passwords |
| Stolen laptop | Old SSH keys still work | Sessions already dead |
| Misconfiguration | Works out of the box | Password auth, weak keys, exposed agents |
| NAT / firewall | Just works | Port forwarding, VPN, or jump hosts |
| Encryption | E2E through relay | E2E direct (when configured correctly) |

You trust Cloudflare to **deliver** packets — the same way you trust your ISP with SSH traffic. Neither can read what you send. The difference: reminal never opens your machine to inbound connections.

**Good habits:**
- Share session ID and PIN separately
- Stop `reminal` when done (Ctrl+C)

## License

MIT
