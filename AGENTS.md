# Agent guardrails for reminal

Rules for AI coding agents (Claude Code etc.) working on this repo. The point is
that when several agents run at once, a break is always **attributable to a
commit** and never comes from **shared machine state** that isn't in git.

## Isolation — never share a working directory
- **One `git worktree` + one branch per agent.** `git worktree add -b <branch> ../reminal-<topic> origin/main`. Two agents in the same checkout stomp each other's working tree and branch switches.
- **Never commit directly to `main`.** Work on a branch; open a PR; merge after CI is green. (Trivial docs are the only sane exception.)
- Before starting, `reminal list` shows which sessions/agents are live and where — pick a non-overlapping scope.

## Never mutate shared machine state during dev
These are **not in git**, so breakage here is un-attributable. Do NOT, as part of feature work:
- run `install.sh`, `reminal upgrade`, or `reminal daemon --install` (don't touch `~/Applications/reminal.app`, the launchd daemon, or the global CLI symlink);
- change the login keychain, the code-signing certs, or **regenerate the signing cert** (see Signing);
- alter TCC / Screen-Recording / Accessibility grants.

Instead, **build to a throwaway dir and run that in an isolated `$HOME`:**
```sh
go build -o /tmp/rem ./cmd/reminal
H=$(mktemp -d); HOME="$H" REMINAL_OWNERS_DIR="$H/etc" /tmp/rem ...
# offline tests: REMINAL_RELAY=ws://127.0.0.1:1 REMINAL_WEB=http://127.0.0.1:1
```
Global-install / signing / release operations belong to a single, deliberate
release flow — one at a time, never a side effect of feature work.

## Before you merge / tag
- `gofmt -l ./cmd ./internal` must be empty (CI blocks the release otherwise).
- `go vet ./...`, `go test ./...`, and `go test -race ./...` must pass.
- Cross-compile every release target: `for p in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do GOOS=${p%/*} GOARCH=${p#*/} go build ./...; done`
- Releases are gated: `release.yml`'s `test` job (same suite) must pass before the build/publish job runs. Tag `vX.Y.Z` triggers it; `changelog/<version>.md` is required and reads for non-technical users.

## Project-specific gotchas
- **Two viewer copies must stay byte-identical:** `cloudflare/public/index.html` and `internal/client/web/index.html`. Edit one, copy to the other (a test enforces it).
- **New agent↔viewer message types** must be added in three places or they silently drop: the dispatch in `internal/client/agent.go`, `machineAccepts` in `machineagent.go`, and `forwardableTypes` in `internal/relay/server.go`.
- The web viewer deploys separately from the binary: `wrangler deploy` from `cloudflare/`. A binary release does **not** update the hosted viewer.

## Signing (do not improvise)
- Canonical macOS cert: **`reminal-signing`** (self-signed, CN `reminal-signing`). Releases are signed by CI from the `MACOS_CERT_*` GitHub secrets. Backup `.p12` + password live in the owner's OneDrive / password manager, not here.
- **Do NOT regenerate the cert** — a new cert changes the Designated Requirement and forces every user to re-grant Screen Recording. Full notes: `docs/macos-signing.md`.
