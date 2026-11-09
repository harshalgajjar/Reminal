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
- **Changelog format is stricter than GitHub markdown** (the in-app "What's new" panel renders it): NO `**bold**`/`*italic*`/`[links]` (they show literal `**`/`[]` — only `` `code` `` works), and every non-bullet line is an UPPERCASED subheading, so use short labels (`Improved`/`Fixed`/`Added`) + plain bullets, never prose sentences or an intro paragraph. See `changelog/README.md`.

## Project-specific gotchas
- **One built file is committed: `internal/vtwasm/ghostty-vt.wasm`**, libghostty-vt for wasm32, because `go build` cannot run Zig. It is built only by `scripts/ghostty-vt/build.sh` (a pinned ghostty commit and Zig, in Docker), which records its sha256 beside it; `vtwasm`'s test checks the sum, and the `ghostty-vt` workflow rebuilds it on a PR touching it and weekly. Never edit or rebuild it any other way; a new ghostty commit is a change to the script's pin, then one run of it.
- **The viewer has one source: `internal/client/web`.** Edit `app.js`, `header.js`, `index.html` (a template: its `<script data-build>` markers are stamped) and the rest there. Nothing built from it is committed: the binary renders the page from what it embeds when its relay's mux is built and serves exactly what `build.json` copies, and `cloudflare/public` (all but `_headers`) is emptied and built by `cloudflare/scripts/build-viewer.mjs` before every `wrangler dev` / `wrangler deploy`. Both follow `internal/client/web/build.json` and refuse a key or version they don't know; a file the page loads at a fixed path goes in its `copy` and in `relay.go`'s embed list (a test checks both); `cloudflare/viewer_build_test.go` holds them byte-identical. A rig that serves the page from a directory gets one from `scripts/viewer-page.sh`.
- **Dropdowns are `<ui-pick>` (`internal/client/web/pick.js`), never a native `<select>`:** each option carries what it means (`sub`), shown under its label in the list and under the chosen one on the control. Build one with `Pick.html` / `Pick.make`, or write `<option data-sub>` children; read it as a select (`value`, `options`, `change`). Tests choose by setting `value` and dispatching `change`, not `selectOption`.
- **New agent↔viewer message types** must be added in three places or they silently drop: the dispatch in `internal/client/agent.go`, `machineAccepts` in `machineagent.go`, and `forwardableTypes` in `internal/relay/server.go`.
- The web viewer deploys separately from the binary: `wrangler deploy` from `cloudflare/`. A binary release does **not** update the hosted viewer. To keep pages already open working, every deploy runs `node scripts/build-viewer.mjs --previous https://live.reminal.app` immediately before `wrangler deploy`, in the same script, every time: it keeps the live Worker's newest builds in `cloudflare/public.kept` for this tree's build (each line checked as one build of `build.json`'s scripts, each file against its name), and fails if it cannot read them unless `--first-deploy`. The deploy's own `[build]` fails without such a run for this tree and this origin, so nothing a `wrangler dev` of another branch, or a run against another Worker, left is ever deployed. Another Worker's deploy names its origin with `--other-origin <origin>` both in its `--previous` run and in its own `[build]` command. `wrangler dev` builds the page alone (wrangler runs `[build]` with `WRANGLER_COMMAND=dev`); outside wrangler, `--dev`.

## Signing (do not improvise)
- Canonical macOS cert: **`reminal-signing`** (self-signed, CN `reminal-signing`). Releases are signed by CI from the `MACOS_CERT_*` GitHub secrets. Backup `.p12` + password live in the owner's OneDrive / password manager, not here.
- **Do NOT regenerate the cert** — a new cert changes the Designated Requirement and forces every user to re-grant Screen Recording. Full notes: `docs/macos-signing.md`.
