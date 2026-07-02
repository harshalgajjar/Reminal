<div align="center">

# reminal

### Your terminal. Anywhere. In one command.

**A modern, zero-config alternative to SSH for reaching your own machine.**
No open ports. No long-lived keys. No router gymnastics.
Run `reminal`, scan a QR code, you're in.

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/harshalgajjar/Reminal?color=success&label=release)](https://github.com/harshalgajjar/Reminal/releases)
[![Go](https://img.shields.io/badge/go-1.25%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux-lightgrey)](https://github.com/harshalgajjar/Reminal/releases)
[![Encryption](https://img.shields.io/badge/encryption-AES--256--GCM-success)](#security)
[![Relay](https://img.shields.io/badge/relay-Cloudflare%20free%20tier-F38020?logo=cloudflare&logoColor=white)](cloudflare/README.md)

</div>

---

```
  Your laptop                 Cloudflare relay              Any device
  ┌─────────────┐            ┌─────────────┐              ┌─────────────┐
  │  reminal    │◄──WSS─────►│  Workers +  │◄────WSS─────►│  browser or │
  │  (PTY/shell)│            │  Durable Obj│              │  reminal -c │
  └─────────────┘            └─────────────┘              └─────────────┘
              end-to-end encrypted — the relay sees ciphertext only
```

---

## The 30-second pitch

SSH was designed in 1995. It assumes you own a static IP, a router you can configure, and a security team to keep keys rotated.

**reminal assumes none of that.** It is built for laptops, hotel Wi-Fi, locked-down café guest networks, and the phone in your pocket — without compromising on security.

| | **reminal** | SSH |
|---|---|---|
| **Setup time** | One command | Keys, configs, port-forwarding, firewalls |
| **Listening port** | None | TCP 22 exposed to the internet |
| **Credentials** | Ephemeral session ID + PIN | Permanent keys on disk |
| **Behind NAT / hotel Wi-Fi** | Just works | VPN or jump host required |
| **Client required on viewer** | None — a browser is the client | `ssh` + a configured key per device |
| **Phone friendly** | Scan QR → in | No native client |
| **Session survives disconnect** | Shell keeps running, hop between devices | Drop the connection, lose your work (unless you wrapped it in `tmux`) |
| **Network blips** | Auto-reconnect, scrollback replay | `Write failed: Broken pipe` |
| **If laptop is stolen** | Sessions already dead | Old keys still grant access |
| **Encryption** | End-to-end through relay | End-to-end direct (if configured right) |

> You trust Cloudflare to deliver packets — the same way you trust your ISP with SSH traffic. Neither can read what you send. The difference: **reminal never opens your machine to the internet.**

---

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/harshalgajjar/Reminal/main/install.sh | sh
```

Installs to `~/.local/bin/reminal`. No sudo. macOS and Linux, Apple Silicon and x86_64.

`reminal` checks for updates on launch and offers to upgrade in place.

<sub>Other options: `reminal upgrade` to force an immediate upgrade · build from source with `./scripts/build.sh` (Go 1.25+).</sub>

---

## Use it

```bash
reminal
```

That's the whole tutorial. Here's what you'll see:

```
  reminal — remote terminal

  Session:  K7M2NP4Q
  PIN:      482916
  Open:     https://reminal-relay.reminal.workers.dev/?s=K7M2NP4Q
  Connect:  reminal --connect K7M2NP4Q --pin 482916

  Scan to join from your phone:

  ██▀▀▀▀▀▀▀██▀▀██▀▀█▀▀▀▀▀▀▀██
  █ █████ █ █ █  ██ █████ █
  █ █   █ █▀ ▀▄█▀█ █   █ █
  █ █████ █ ▄██ ▀█ █████ █
  ▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀

  Waiting for connection... (Ctrl+C to stop)
```

Pick your portal — they all work:

- **Phone.** Scan the QR. URL fragment carries the PIN. You're auto-joined.
- **Browser.** Built-in web terminal lives at the relay URL. Any device — laptop, iPad, kiosk PC, a friend's Chromebook. **Nothing to install, no app to download, no client to configure.**
- **Terminal.** `reminal --connect K7M2NP4Q --pin 482916` — full TTY, full color, full speed.

No env vars. No relay setup. No ports.

---

## What you get

<table>
<tr>
<td width="33%" valign="top">

#### Persistent shell

Close the laptop, switch to your phone, reconnect from a different city — your shell is still right where you left it. The PTY lives on your machine; viewers come and go without disturbing it.

<sub>SSH drops a connection? You lose the job. reminal doesn't — no `tmux`, no `nohup`, no thinking about it.</sub>

</td>
<td width="33%" valign="top">

#### Zero-install web terminal

A full xterm.js terminal is built into the relay. **Any browser is the client.** Phone, iPad, locked-down work laptop, hotel-lobby PC. Open the URL, type the PIN, you're in. Nothing to install. Nothing to configure.

</td>
<td width="33%" valign="top">

#### Resilient by default

Wi-Fi drop, tunnel switch, walk into the elevator — reminal auto-reconnects with exponential backoff and replays what you missed from a 2 MiB scrollback buffer. The connection layer is the part you should never have to think about.

</td>
</tr>
</table>

---

## Why people use it

<table>
<tr>
<td width="33%" valign="top">

#### Forgot something at home

Laptop sleeping on the desk. Phone in your pocket on the train. Scan, run the command, lock it back up.

</td>
<td width="33%" valign="top">

#### Hostile networks

Hotel Wi-Fi, café Wi-Fi, conference NAT — all block inbound. They all allow outbound HTTPS. reminal only needs outbound HTTPS.

</td>
<td width="33%" valign="top">

#### Pair from anywhere

Send a session ID and PIN to a teammate. They scan or paste. Live shared terminal. Hang up when done — no keys to revoke.

</td>
</tr>
</table>

---

## Security

> Built to be **as secure as a properly configured SSH — and safer by default.**

SSH leaves port 22 open, stores long-lived keys on disk, and trusts you to configure everything correctly. reminal takes the opposite approach: **nothing to expose, nothing permanent to steal, encryption end-to-end.**

| Layer | What it does |
|---|---|
| **No open ports** | Your machine only initiates outbound connections. There is nothing on the network to scan, brute-force, or zero-day. |
| **Ephemeral credentials** | Session ID and PIN exist only while `reminal` is running. Ctrl+C and they are gone forever. |
| **Dual-factor by design** | An attacker needs both the session ID (~1 trillion combinations) and the 6-digit PIN. Knowing one is useless. |
| **Lockout on abuse** | Five wrong PINs trigger a 5-minute lockout. PIN guessing is not viable. |
| **End-to-end encryption** | AES-256-GCM with a fresh random 256-bit session key per agent run. Distributed to each viewer via a PIN-authenticated X25519 handshake (EKE-style) — the relay never sees the key or anything offline-brute-forceable from it. |
| **Forward-secret handshake** | Each WebSocket connection runs its own ephemeral X25519 exchange. Even if a future attacker recovers the PIN, recorded ciphertext stays unreadable. |
| **Relay-blind** | Cloudflare Workers route ciphertext. A relay that records traffic cannot recover the session key offline — wrong PIN guesses are detectable only by attempting a full handshake online (one shot each, bounded by the 5-strike lockout). |
| **TLS in transit** | WSS / TLS on every hop in production. |

### Best practices

- Share the session ID and PIN over **different channels** (e.g. email the ID, text the PIN).
- Stop the session with **Ctrl+C** when you're done. Credentials die instantly.
- Keep your client up to date — `reminal upgrade`.

---

## Self-host the relay (free, one time)

The relay runs on **Cloudflare Workers + Durable Objects**. Free tier handles thousands of sessions a month.

```bash
cd cloudflare
npm install
npx wrangler login
npm run deploy
```

Then point `DefaultCloudRelay` / `DefaultCloudWeb` in `internal/config/config.go` at your `workers.dev` URL and rebuild. Full guide in [cloudflare/README.md](cloudflare/README.md).

---

## Local development

```bash
# Terminal 1 — your own relay on localhost:8080
reminal relay

# Terminal 2 — share a session via the local relay
REMINAL_LOCAL=1 reminal

# Terminal 3 — connect from another shell or the browser
REMINAL_LOCAL=1 reminal --connect <session_id> --pin <pin>
# or http://localhost:8080/?s=<session_id>
```

---

## Reference

### Commands

| Command | What it does |
|---|---|
| `reminal` | Share this terminal session |
| `reminal connect <id-or-url> [pin]` | Connect to a remote session from your terminal (PIN prompted if omitted) |
| `reminal info [--json]` | Reprint the session ID / PIN / URL / QR for the agent running on this machine (or JSON for scripts) |
| `reminal qr` | Print just the join QR for the running agent (for a second screen) |
| `reminal doctor` | Self-diagnostic: version, relay reachability, terminal, shell |
| `reminal completion <bash\|zsh\|fish>` | Print a shell completion script |
| `reminal upgrade` | Upgrade to the latest release |
| `reminal relay [port]` | Start a local relay (development only) |
| `reminal version` | Print version |

### Environment variables

| Variable | Default | What it does |
|---|---|---|
| `REMINAL_RELAY` | Cloudflare relay URL | Override the relay WebSocket base URL |
| `REMINAL_WEB` | Cloudflare web URL | Override the web UI URL shown in the banner |
| `REMINAL_LOCAL` | — | Set to `1` to point everything at `localhost` |
| `REMINAL_NO_KEEP_AWAKE` | — | Set to `1` to let the host sleep while reminal runs (defaults to keeping it awake via `caffeinate` / `systemd-inhibit`) |
| `REMINAL_DEBUG` | — | Set to `1` to append the raw error string to status lines, for diagnosing connection problems |
| `SHELL` | `$SHELL`, then probes `/bin/zsh`, `/bin/bash`, `/bin/sh` | Which shell to spawn inside the session |

---

<div align="center">

### License

reminal is **dual-licensed**: [AGPL-3.0](LICENSE) for open-source use, or a
[commercial license](LICENSING.md) for proprietary/closed-source use. See
[`LICENSING.md`](LICENSING.md) for details, and [`CLA.md`](CLA.md) if you'd
like to contribute.

<sub>Built by <a href="https://github.com/harshalgajjar">@harshalgajjar</a>. Stars are appreciated. Issues even more so.</sub>

</div>
