# macOS code signing (self-signed, free)

The release pipeline signs the macOS binaries (`reminal` + `reminal-capture`)
with a **self-signed** code-signing certificate. This needs **no Apple Developer
account and costs nothing**.

## Why

macOS anchors a TCC permission (Screen Recording, Accessibility, …) to a binary's
**Designated Requirement** — for a signed binary that's a stable *identifier +
certificate*, not the per-build hash. So once the release is signed with a fixed
cert, a user grants the permission **once** and it survives every future upgrade.

This matters most for **Screen Recording**: sessions spawned by the background
host (the "+" in the Machines panel) run under launchd, *outside* your terminal's
permission grant, so they need reminal itself to hold the grant. Without stable
signing that grant resets on every version bump.

Not notarized — that's fine for how reminal is actually installed. `install.sh`
fetches the release tarball with `curl`, which does not attach the
`com.apple.quarantine` attribute the way a browser download does, so Gatekeeper
never gates it. (A `.tar.gz` downloaded through a browser *would* carry
quarantine and show one "unidentified developer" click-through; clearing that
needs notarization, which needs the paid account.)

## One-time setup

### 1. Create the certificate

Keychain Access → **Certificate Assistant ▸ Create a Certificate…**
- **Name:** `reminal-signing`  (this becomes the signing identity)
- **Identity Type:** Self Signed Root
- **Certificate Type:** Code Signing
- Check **"Let me override defaults"** and set a long **validity** (e.g. 3650+
  days) so the cert — and therefore the grant — doesn't expire out from under you.

Create it (it lands in the *login* keychain under *My Certificates*).

### 2. Export it

In Keychain Access, select the `reminal-signing` cert (with its private key) →
right-click → **Export…** → format **Personal Information Exchange (.p12)** →
set an export password. That password becomes `MACOS_CERT_PASSWORD`.

### 3. Add three GitHub Actions secrets

Repo → Settings → Secrets and variables → Actions → New repository secret:

| Secret | Value |
|---|---|
| `MACOS_CERT_P12` | `base64 -i reminal-signing.p12 \| pbcopy` — the base64 of the .p12 |
| `MACOS_CERT_PASSWORD` | the export password from step 2 |
| `MACOS_CERT_IDENTITY` | `reminal-signing` — the cert's Common Name |

That's it. The next tagged release signs with this cert. If the secrets are
absent, or the signed app's Designated Requirement differs from the one every
release has shipped, the macOS build fails and nothing is published for it:
see the "Assemble & sign reminal.app (macOS)" step in
`.github/workflows/release.yml`.

## Keeping it stable

Don't regenerate the cert. The whole benefit is that the identifier
(`sh.reminal` / `sh.reminal.capture`) + certificate stay constant across
releases. If you ever replace the cert, every user re-grants their permissions
once.
