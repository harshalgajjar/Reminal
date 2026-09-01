# Subprocessors and Data Handling

**Status:** current as of the version in this repository.

This document lists every third party that any reminal data passes through, and
states what data that is. It exists because "who else touches this" is the first
question in every vendor security review, and because the honest answer here is
unusually short.

---

## Summary

reminal is **not a hosted service and does not operate a user account system.**
There is no sign-up, no user database, no billing system, and no profile. The
software runs on the user's own machine and connects to a relay that stores no
session content.

Concretely, reminal holds **no user account data of any kind**: no names, no email
addresses, no passwords, no payment details, no IP address logs of its own.

## Subprocessors

| Subprocessor | Role | Data it handles | Retention | Avoidable? |
|---|---|---|---|---|
| **Cloudflare, Inc.** | Relay — Workers + Durable Objects, at `live.reminal.app` | Session routing metadata and ciphertext in transit. Plaintext for `reminal expose` only (see below) | Routing metadata ≤10 min past agent disconnect; content never stored | **Yes** — self-host the relay |
| **GitHub, Inc. (Microsoft)** | Release distribution, source hosting, security advisory intake | Client IP and User-Agent on update checks and downloads. Vulnerability reports you choose to submit | Per GitHub's policy | Partly — install from source and disable update checks |
| **STUN/TURN provider** | WebRTC NAT traversal, when a direct path is negotiated | IP addresses of both peers. TURN relays DTLS-encrypted media it cannot read | Per provider | Yes — sessions fall back to the relay path |
| **Loops** | Waitlist email for the announced hosted tier, at `reminal.app` only | Email address, and whether the signup identified as an individual or a team with a size range. Nothing else, and only if the form is submitted | Until unsubscribe or deletion on request | **Yes** — the software never contacts it; do not submit the form |

There are **no other subprocessors**. No analytics vendor, no error-reporting
service, no CDN for client assets, no CRM, no payment processor.

Loops is listed above for completeness and is **not in the software's path at all**: it
receives a waitlist address only when someone submits a form on the marketing site, and no
reminal client or relay ever contacts it. Running reminal touches none of it.
The absence of a client-side telemetry SDK is verifiable:

```bash
grep -rIn "analytics\|telemetry\|posthog\|sentry\|mixpanel\|amplitude" internal/ cmd/
# no output
```

## What Cloudflare can and cannot see

**Cannot see:** terminal output, keystrokes, screen or window contents, filenames,
clipboard contents, file transfers, or the session key. All of it is sealed with
AES-256-GCM under a key that never reaches the relay in usable form. See
[architecture §5](architecture.md#5-what-the-relay-can-observe).

**Can see:** session IDs, connection timing, viewer counts, ciphertext frame sizes,
and — as with any network intermediary — the source IP addresses of connecting
clients, recorded in Cloudflare's own edge logs under Cloudflare's retention policy,
not reminal's.

**One exception: `reminal expose`.** Port-forwarded HTTP traffic is *not*
end-to-end encrypted, because the visitor is an ordinary browser holding no reminal
key material. Request and response bodies transit the relay as plaintext. Do not
use `reminal expose` for sensitive data on a relay you do not operate.

## Data stored by the relay

Per active session, the Durable Object stores only routing and lockout state:

| Key | Purpose |
|---|---|
| `token` | High-entropy reattach credential |
| `pinHash` | Legacy bcrypt credential; also gates `expose` |
| `agentAuthed`, `viewerAuthed` | Whether a live agent holds the room |
| `failedAttempts`, `lockedUntil` | Lockout state for the `expose` PIN gate |
| `tunnelMeta` | Port, gate mode, cookie signing key for an active port-forward |

**No session content is stored at any point** — no scrollback, no frame buffer, no
recording, no message log. The Worker emits no application logs
(`grep -rn "console\." cloudflare/src` returns nothing).

**Retention is enforced by the runtime, not by policy.** Ten minutes after the
agent disconnects, an alarm fires, connected viewers are closed, and the Durable
Object calls `deleteAll()`. Copy/paste rendezvous rooms are capped at one hour.
There is no backup, export, or archival of any of it, and no mechanism by which
this data could be retained longer.

## Data stored on the user's machine

Under `~/.reminal/` (mode 0600): user settings, this device's Ed25519 owner key,
pinned machine identities, and revocation tombstones. Under `/etc/reminal/`
(root-owned): the machine's authorised owner list.

Session IDs, PINs, and session keys exist **in memory only** and are destroyed when
the agent exits. No session content is ever written to disk.

## Data residency

Cloudflare Workers execute at the edge location nearest the connecting client, and
Durable Objects are placed near first use. reminal does not currently pin execution
to a jurisdiction.

**For organizations with data residency requirements:** because no session content
ever reaches the relay, the residency question applies only to routing metadata
with a ten-minute lifetime. Organizations that need a stronger guarantee should
self-host the relay in their own Cloudflare account or infrastructure, which places
all of it under their own control and removes Cloudflare as a reminal subprocessor
entirely.

## Self-hosting

The relay is in this repository (`cloudflare/`) under the same AGPL-3.0 license as
the client. Deploying it to your own Cloudflare account removes the public relay
from the trust chain and puts the metadata above — and, critically, any
`reminal expose` plaintext — on infrastructure you control. This is the recommended
configuration for regulated environments.

## GDPR and data protection posture

Stated plainly rather than aspirationally.

**Personal data processed by reminal itself:** none. There is no account system and
no user database. IP addresses observed by Cloudflare's edge are processed by
Cloudflare as its own controller under its published policies.

**Session content:** end-to-end encrypted and never accessible to the relay. Where
session content contains personal data, reminal is architecturally incapable of
processing it — a design property, not a policy commitment.

**Contractual vehicles:** no legal entity currently stands behind reminal, so
vendor-side instruments — a Data Processing Agreement, Standard Contractual
Clauses, a breach-notification undertaking, cyber liability insurance — are not
available for signature. This would change if a commercial entity is established.

In practice this is rarely the blocker it first appears, because the framing that
fits reminal also removes the requirement: an organization deploying reminal is
running **self-operated open-source software**, making it its own controller and
processor, with no vendor DPA required. Self-hosting the relay completes that
picture by removing the last third party from the chain.

## Changes

Material changes to this list will be reflected here and noted in release notes.
Adding a subprocessor that could observe session content would be a breaking change
to reminal's security model and would be announced as such.
