<div align="center">

# reminal

### Your machines — every terminal, window, and desktop — in any browser.

**One command. Scan a QR. Your phone is a live terminal on your machine** — then reach in and control any app window, mirror the whole screen, and drive your whole fleet from one browser.
No open ports, no keys on disk, no client to install.

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE) [![Release](https://img.shields.io/github/v/release/harshalgajjar/Reminal?color=success&label=release)](https://github.com/harshalgajjar/Reminal/releases) [![Go](https://img.shields.io/badge/go-1.25%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/) [![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux-lightgrey)](https://github.com/harshalgajjar/Reminal/releases) [![Encryption](https://img.shields.io/badge/encryption-AES--256--GCM-success)](#security) [![Relay](https://img.shields.io/badge/relay-Cloudflare%20free%20tier-F38020?logo=cloudflare&logoColor=white)](cloudflare/README.md)

</div>

## Set it up in one line

```bash
curl -fsSL https://raw.githubusercontent.com/harshalgajjar/Reminal/main/install.sh | sh
reminal
```

On Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/harshalgajjar/Reminal/main/install.ps1 | iex
reminal
```

`reminal` prints a session ID, a PIN, and a QR code. Scan it, and your phone is a **full terminal on your machine** — real color, touch text-selection, on-screen modifier keys. No port forwarding. No keys to manage. Nothing to install on the phone; the browser is the client.

<div align="center">
<img src="docs/setup.gif" alt="A terminal runs reminal and prints a QR code; a phone scans it and joins the same session — end-to-end encrypted" width="900">

<sub>That's the whole setup. The fastest SSH you'll ever configure — because there's nothing to configure.</sub>
</div>

---

## …but reminal isn't just a terminal

Once you're in, any app window streams live into your browser — and you don't just watch it, you **drive** it: cursor, click and right-click, type, scroll, drag, pinch-zoom. Not open yet? **Launch any installed app** on the host from the **Apps** menu, then drive it. Or tap **Host → View full desktop** to run the whole machine at once. On a phone the screen becomes a trackpad — your Mac, fully hands-on, from your pocket.

<div align="center">
<img src="docs/hero.gif" alt="Real capture: a phone opens the Mac's TextEdit window and types 'Hello from my phone!' — the words land in the real app on the Mac, live — then the phone mirrors the entire desktop" width="880">

<sub><b>Real capture, unedited.</b> The phone picks a window, types into it — the words appear in the real app on the Mac — then mirrors the whole desktop. Native capture, streamed peer-to-peer.</sub>
</div>

---

## Close the lid and walk away

Close a MacBook's lid and macOS puts it to sleep — unless it's on power with a monitor *and* keyboard attached. The usual fix is a hardware "dummy" HDMI plug that fakes a display, or just leaving the lid propped open on your desk.

**Closed-lid mode does it in software.** Flip it on and leave — no monitor, no dongle, in any order. reminal disables clamshell sleep and, because GUI apps need a screen to draw on, spins up a virtual display the moment the Mac goes headless — so window mirroring keeps working with nothing plugged in. Toggle it off and everything is undone.

<div align="center">
<img src="docs/lid.gif" alt="Animation: a MacBook desktop with VS Code, Keynote and Mail windows folds its lid shut — then the same windows appear live as panes in the reminal web viewer, still controllable, with nothing plugged in" width="900">
</div>

---

## Interact with your machines like they're right here

Pop any remote window out onto your desktop and it sits there like a native app — except it's running on another machine entirely. Line up an editor from your MacBook, a terminal on a cloud box, and a dashboard from the Mac mini at home, and work across all of them as if they were local.

<div align="center">
<img src="docs/machines.gif" alt="Three native windows in front — an editor, a terminal, and a dashboard — each linked by a dotted line to the machine behind it: a MacBook (studio-mac), a cloud VM (aws-eu-1), and a Mac mini (mini-01)" width="900">
</div>

---

## 60 fps, from a machine that isn't here

And nothing about those windows says *stream*. Drag it, scrub a video inside it, watch a build scroll past — it stays fluid the whole way, on hotel Wi-Fi or a phone on cellular. **Sixty frames a second, 17 ms from its screen to yours.**

<div align="center">
<img src="docs/video.gif" alt="A Mac Studio named mac-studio, with one of its windows streaming live into a browser below it at 60 fps — the motion inside sweeping a smooth trail across the canvas — beside a large '60 fps' and the line 'of a live window, from a machine that isn't here'" width="900">

<sub>H.264 down the session's own DataChannel — peer-to-peer, about 2.6 Mbps. Relay-only viewers get the same video at 30 fps.</sub>
</div>

---

## Point an AI agent at your fleet

Hand the session ID and PIN to an agent and it connects like any other viewer — to every machine you've shared. One credential, and it can triage an incident, run scans, and kick off or delegate long-running jobs across all your hardware at once, while you watch it happen in the same browser.

<div align="center">
<img src="docs/agent.gif" alt="An AI agent given the session key dispatches jobs in parallel to three machines — run test suite on studio-mac, scan and patch on aws-eu-1, rotate backups on mini-01" width="900">
</div>

---

## Own your machines. Watch them all.

The moment your work spans more than one machine — a rack of servers, or agents let loose on several boxes at once — the hard part isn't starting it, it's *seeing* it. `reminal machines` is one live view of everything you own: every machine, every terminal on it, what's running right now, who's watching, how long it's been idle. An agent running a test suite on your laptop, patching a CVE on a cloud VM, and rotating backups on the Mac mini — or just your own sessions — all at a glance. It's on the CLI and in the web **Machines panel**, where you can jump into any session, rename it, spawn a new one, or kill it on any box.

That single pane works because you *own* the machines. Enroll a device once — `reminal own` prints its id, you paste `sudo reminal add owner <id>` on each machine — and from then on it reaches every session with **no PIN**. The trust is a per-device key: revocable one at a time, `sudo`-gated to grant, and the relay still only ever sees ciphertext.

<div align="center">
<img src="docs/own.gif" alt="A live 'reminal machines' view of a fleet you own — studio-mac, aws-eu-1 and mini-01, all online — each showing its sessions: agents running a test suite, patching a CVE and rotating backups, alongside ordinary sessions like api-prod, worker and grafana, with live indicators and viewer counts" width="900">
</div>

---

## Share a local port with the world

`reminal expose 3000` turns whatever's running on `localhost` into a **public HTTPS URL** — a dev server, a webhook target, a build to show a client. PIN-gated by default (or `--public` to open it up), so you can share the link without deploying anything. It's a built-in ngrok, on the tool you already have running.

<div align="center">
<img src="docs/expose.gif" alt="A terminal runs a dev server on localhost:3000, then reminal expose 3000 prints a public https URL — which loads the same app live on a phone, PIN-gated over TLS" width="900">
</div>

---

## Move a file between any two machines

`reminal copy report.pdf` on one machine prints a **one-time code**; `reminal paste <code>` on another pulls the file down — Mac to Linux, laptop to server, anywhere to anywhere. End-to-end encrypted, no cloud drive, no account. AirDrop, for every machine you own.

<div align="center">
<img src="docs/copypaste.gif" alt="One terminal runs reminal copy report.pdf and gets a one-time code; another machine runs reminal paste with that code and the file transfers, end-to-end encrypted" width="900">
</div>

---

## The last thing you'll install standing at your computer

Set it up once, in person — then you never have to sit at that machine again. From any browser you get its terminal, any window, the whole desktop, a public link to a local port, files to and from it, even a live session shared with someone else. One tool, every remote job.

<div align="center">
<img src="docs/toolkit.gif" alt="reminal at the center, wired to the five things it does: a terminal in any browser, control any window or desktop, a public URL for a local port, sending files between machines, and pairing with anyone live — no accounts, no subscriptions, nothing for the other side to install" width="900">
</div>

---

## How it works

<div align="center">
<img src="docs/howitworks.gif" alt="Architecture: your machine (reminal) and any device both dial out to a Cloudflare relay over encrypted WSS — the relay only routes ciphertext it can't read — while window and desktop frames go directly peer-to-peer over WebRTC, off the relay" width="900">
</div>

Your machine dials **out** to a relay over WSS; viewers dial out to the same relay. Nothing ever listens on your machine. Everything through the relay is encrypted end-to-end — it routes ciphertext it cannot read. Window and desktop frames don't even take that path: they ride a direct WebRTC connection between browser and host.

> You trust Cloudflare to deliver packets — the same way you trust your ISP with SSH traffic. Neither can read what you send. The difference: **reminal never opens your machine to the internet.**

---

## Everything you get

Join a session from anywhere — phone (scan the QR), any browser (open the URL, type the PIN), or another terminal (`reminal --connect <id> --pin <pin>`). Then:

<table>
<tr>
<td width="50%" valign="top">

#### Persistent, resilient shell

Close the laptop, switch to your phone, reconnect from a different city — your shell is right where you left it, and the current screen paints **instantly** (a snapshot, no slow fast-forward). Wi-Fi drop, tunnel, elevator? Auto-reconnect with backoff, 2 MiB of scrollback intact.

</td>
<td width="50%" valign="top">

#### Pair with anyone

Send a session ID and PIN to a teammate over any channel and they join the same live shell — or a window mirror — from a browser. No account, no install, multiple viewers at once. Ctrl+C ends it; there's nothing to revoke.

</td>
</tr>
<tr>
<td width="50%" valign="top">

#### Sessions that outlive your terminal

Kick off a long job and close the lid — it keeps running. `reminal new deploy` spawns a named session; `list` · `attach` · `rename` · `kill` · `prune` manage the whole fleet by name, id, or fuzzy match. The Host panel shows live CPU/memory and spawns one in a tap.

</td>
<td width="50%" valign="top">

#### Zero-install web terminal

A full xterm.js terminal is built into the relay. Any browser is the client — phone, iPad, locked-down work laptop, hotel-lobby PC. Pinch-zoom, text selection with draggable handles, on-screen modifier keys, voice dictation, find-in-scrollback.

</td>
</tr>
<tr>
<td width="50%" valign="top">

#### Files, ports & pings

`reminal copy` / `paste` move a file between any two machines with a one-time code. `reminal expose 3000` puts a local port on a public, PIN-gated URL. `reminal send` pushes a file to **every viewer at once**, and `reminal notify` fires a browser notification on all of them.

</td>
<td width="50%" valign="top">

#### Secure by construction

No open ports, ephemeral session ID + PIN, AES-256-GCM end-to-end with a PIN-authenticated X25519 handshake the relay can't crack offline. Ctrl+C and the credentials are gone. [Details below](#security).

</td>
</tr>
<tr>
<td width="50%" valign="top">

#### Own a machine, skip the PIN

Enroll a device as an **owner** — `reminal own`, then `sudo reminal add owner <id>` once — and it connects to any of that machine's sessions with no PIN. Per-device trust, revocable one at a time (`reminal owners revoke`, or self-revoke from the browser). The relay stays blind either way.

</td>
<td width="50%" valign="top">

#### Every machine, one list

`reminal machines` shows every box you own and each live session on it — what's running, viewers, idle time. Same view in the web **Machines panel**: attach, rename, spawn, or kill a session on any machine from the browser.

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
| **Owner devices, revocable** | A device you enroll as an owner connects without the PIN using its own key — a separate trust path from the ephemeral PIN, gated behind `sudo` to enroll and revocable per-device (or self-revoked from any browser). The relay still only routes ciphertext. |
| **Dual-factor by design** | An attacker needs both the session ID (~1 trillion combinations) and the 6-digit PIN. Knowing one is useless. |
| **Rate-limited by the agent** | Every PIN guess costs a full online handshake with your machine, and the agent answers at most ~6 per minute (burst of 8, one token per 10s). Exhausting a 6-digit PIN at that rate takes months — far longer than a session lives. |
| **End-to-end encryption** | AES-256-GCM with a fresh random 256-bit session key per agent run. Distributed to each viewer via a PIN-authenticated X25519 handshake (EKE-style) — the relay never sees the key or anything offline-brute-forceable from it. |
| **Forward-secret handshake** | Each WebSocket connection runs its own ephemeral X25519 exchange. Even if a future attacker recovers the PIN, recorded ciphertext stays unreadable. |
| **Relay-blind** | Cloudflare Workers route ciphertext. A relay that records traffic cannot recover the session key offline — wrong PIN guesses are detectable only by attempting a full handshake online (one shot each, bounded by the agent's kex throttle). |
| **P2P you can trust** | WebRTC signaling (SDP, ICE) rides inside the already-encrypted session channel, so the relay can't tamper with DTLS fingerprints — no man-in-the-middle window. Frames on the DataChannel are DTLS-protected end-to-end. |
| **TLS in transit** | WSS / TLS on every hop in production. |

**One deliberate exception:** `reminal expose` port-forwards are **not** end-to-end encrypted — the visitor is an ordinary browser with no reminal key, so that traffic passes through the relay in plaintext (PIN-gated, but readable by the relay). Everything else above is E2E. Self-host the relay if that matters to you.

**Best practices:** share the session ID and PIN over different channels (email the ID, text the PIN) · Ctrl+C when done — credentials die instantly · keep the client current with `reminal upgrade`.

**Digging deeper:** [Security architecture](docs/security/architecture.md) · [Threat model](docs/security/threat-model.md) · [Subprocessors & data handling](docs/security/subprocessors.md) · [Self-assessment](docs/security/self-assessment.md) · [Report a vulnerability](SECURITY.md)

---

## reminal vs SSH, at a glance

SSH was designed in 1995 — it assumes a static IP, a router you can configure, and keys you keep rotated. reminal assumes none of that, so the trade-offs line up differently:

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
| **GUI apps** | Mirror & control any window — or the whole desktop | X11 forwarding, if you dare |
| **Laptop lid shut, no monitor** | Closed-lid mode keeps serving on a virtual display | Terminal only |
| **If laptop is stolen** | Sessions already dead | Old keys still grant access |
| **Encryption** | End-to-end through relay | End-to-end direct (if configured right) |

---

## Run your own relay (free, one time)

The relay runs on **Cloudflare Workers + Durable Objects**. The free tier handles thousands of sessions a month — and window frames go peer-to-peer, so the heavy traffic never touches it.

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

### Platform support

The mirroring you see above isn't macOS-only — window capture **and** full control (click, type, scroll, drag) work on Linux/X11 and Windows as well.

| Capability | macOS | Linux | Windows |
|---|---|---|---|
| Terminal sharing · sessions · files · port forwarding | ✅ | ✅ | ✅ ConPTY |
| Owner connect (PIN-free) · `reminal machines` | ✅ | ✅ | ✅ |
| Window & desktop mirroring + control | ✅ ScreenCaptureKit — H.264 up to 60 fps | ✅ X11 — `wmctrl` · `xdotool` · ImageMagick | ✅ Win32 — PrintWindow · SendInput |
| Closed-lid mode (auto virtual display) | ✅ | — | — |
| Hot restart (`reminal restart`) | ✅ | ✅ | — (no exec() — start a new session after upgrading) |

<sub>Linux capture needs an **X11** session (or Xwayland) — native Wayland blocks synthetic input, so it isn't supported yet. Apple Silicon, x86_64, and Windows ARM64 all supported. Windows sessions default to PowerShell (pwsh → Windows PowerShell → cmd; set `$env:SHELL` to override).</sub>

### Commands

| Command | What it does |
|---|---|
| `reminal [--name <name>]` | Share this terminal session |
| `reminal new [name]` | Spawn a fresh background session (detached — survives this terminal closing) |
| `reminal list [filter] [-v]` | List sessions, recent-first; filter by id/name/cwd/title (`--idle`, `--viewers`, `--headless`) |
| `reminal attach [id\|name]` | Re-connect to a local session as a viewer (no arg → interactive picker) |
| `reminal connect <id-or-url> [pin]` | Connect to a remote session from your terminal (PIN prompted if omitted) |
| `reminal rename [id\|name] <new-name>` | Rename a running session (inside a session: `reminal rename <new-name>`) |
| `reminal stop [id\|name\|port]` | Stop the reminal layer — kicks viewers, keeps your shell/server running |
| `reminal kill [id\|name]` | Fully terminate a session (kills the shell — irreversible) |
| `reminal prune [dur] [-y]` | Kill idle, unwatched sessions in one go (default idle ≥ 30m) |
| `reminal restart [--all]` | Hot-swap the running agent(s) onto the latest binary — the shell stays alive |
| `reminal expose <port> [--public]` | Forward a local HTTP port to a public URL (PIN-protected by default) |
| `reminal send <file>` | Push a file to every connected viewer (web client auto-downloads) |
| `reminal copy [--ttl <dur>] <file>` | Offer a file for pickup anywhere; prints a one-time code |
| `reminal paste <code> [dest]` | Fetch a file offered by `reminal copy` on another machine |
| `reminal notify <message>` | Push a notification to viewers (browser notification on web) |
| `reminal connections` | List currently attached viewers with connect time |
| `reminal own` | Print this device's owner id + the `add owner` line to paste on machines you want to own |
| `reminal add owner <id> [--label <name>]` | Enroll an owner device on this machine (needs `sudo` / an Administrator terminal on Windows) — lets it connect PIN-free |
| `reminal owners [rename\|revoke\|restore <id\|label> …]` | List / relabel / revoke / restore this machine's owner devices |
| `reminal machines [rename <id\|name> <new-name>]` | List every machine you own and its live sessions (web Machines panel manages them) |
| `reminal info [id\|name] [--all] [--qr] [--json]` | Show connect details — ID / PIN / URL / QR |
| `reminal qr [id\|name]` | Print just the join QR (for a second screen) |
| `reminal settings` | Settings page: keep the Mac unlocked for remote control; **closed-lid mode** (serve with the lid shut and nothing plugged in — disables clamshell sleep, auto-creates a virtual display while headless) |
| `reminal doctor` | Self-diagnostic: version, relay reachability, terminal, shell |
| `reminal permissions` | macOS: grant Screen Recording to reminal once, so background (`+`) sessions can mirror windows |
| `reminal completion <bash\|zsh\|fish\|powershell>` | Print a shell completion script |
| `reminal upgrade` | Upgrade to the latest release |
| `reminal relay [port]` | Start a local relay (development only) |
| `reminal version [--verbose]` | Print version |

Sessions resolve by **exact id, exact name, unique id prefix, or unique substring** of name / cwd / title — `reminal attach deploy` just works.

### Environment variables

| Variable | Default | What it does |
|---|---|---|
| `REMINAL_RELAY` | Cloudflare relay URL | Override the relay WebSocket base URL |
| `REMINAL_WEB` | Cloudflare web URL | Override the web UI URL shown in the banner |
| `REMINAL_LOCAL` | — | Set to `1` to point everything at `localhost` |
| `REMINAL_OWNERS_DIR` | `/etc/reminal` | Where the machine's owner list lives (the sudo-gated trust store) — override for tests or unusual layouts |
| `REMINAL_NO_KEEP_AWAKE` | — | Set to `1` to let the host sleep while reminal runs (defaults to keeping it awake via `caffeinate` / `systemd-inhibit`) |
| `REMINAL_TURN` / `REMINAL_TURN_USER` / `REMINAL_TURN_PASS` | — | Optional TURN server for P2P window mirroring behind hostile NATs (or `REMINAL_TURN_CF_KEY` + `REMINAL_TURN_CF_TOKEN` for Cloudflare TURN). Without one, un-punchable viewers stay on the relay fallback |
| `REMINAL_NO_CAPTURE_HELPER` | — | Set to `1` to force the screenshot capture path (skip the native ScreenCaptureKit helper) |
| `REMINAL_DEBUG` | — | Set to `1` to append the raw error string to status lines, for diagnosing connection problems |
| `SHELL` | `$SHELL`, then probes `/bin/zsh`, `/bin/bash`, `/bin/sh` | Which shell to spawn inside the session |

Installs to `~/.local/bin/reminal` — no sudo. macOS and Linux, Apple Silicon and x86_64. Build from source with `./scripts/build.sh` (Go 1.25+, Swift toolchain on macOS for the native capture helper).

---

## Ready to try it?

```bash
curl -fsSL https://raw.githubusercontent.com/harshalgajjar/Reminal/main/install.sh | sh
reminal
```

On Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/harshalgajjar/Reminal/main/install.ps1 | iex
reminal
```

Scan the QR — you're in. No signup, no port-forwarding, no keys on disk. About **30 seconds** from this page to your own machine, live in a browser.

---

<div align="center">

### License

reminal is **dual-licensed**: [AGPL-3.0](LICENSE) for open-source use, or a
[commercial license](LICENSING.md) for proprietary/closed-source use. See
[`LICENSING.md`](LICENSING.md) for details, and [`CLA.md`](CLA.md) if you'd
like to contribute.

<sub>Built by <a href="https://github.com/harshalgajjar">@harshalgajjar</a>. Stars are appreciated. Issues even more so.</sub>

</div>
