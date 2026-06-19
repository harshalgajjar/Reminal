# Publish reminal to Homebrew

Homebrew core is for widely-used tools with strict review. For reminal, use a **custom tap** — one command for users, full control for you.

## User install (after publish)

```bash
brew tap reminal/tap
brew install reminal
reminal
```

---

## One-time setup

### 1. Push the main repo to GitHub

```bash
cd /Users/harshal/Desktop/server/GitHub/reminal
git init
git add .
git commit -m "Initial release"
git branch -M main
git remote add origin https://github.com/reminal/reminal.git
git push -u origin main
```

(Create the `reminal/reminal` repo on GitHub first if it doesn't exist.)

### 2. Create a GitHub release

```bash
git tag v0.1.0
git push origin v0.1.0
```

GitHub Actions builds and uploads:

- `reminal_0.1.0_darwin_arm64.tar.gz`
- `reminal_0.1.0_darwin_amd64.tar.gz`
- `reminal_0.1.0_linux_arm64.tar.gz`
- `reminal_0.1.0_linux_amd64.tar.gz`

Wait for the [Release workflow](https://github.com/reminal/reminal/actions) to finish.

### 3. Update the formula checksums

```bash
chmod +x scripts/update-formula-sha.sh
./scripts/update-formula-sha.sh v0.1.0
```

Paste the four `sha256` values into `homebrew-tap/Formula/reminal.rb` (replace `REPLACE_AFTER_RELEASE`).

### 4. Publish the tap repo

Create a **new** GitHub repo: `reminal/homebrew-tap`

```bash
cd homebrew-tap
git init
git add Formula/reminal.rb README.md
git commit -m "Add reminal formula v0.1.0"
git branch -M main
git remote add origin https://github.com/reminal/homebrew-tap.git
git push -u origin main
```

The repo **must** be named `homebrew-tap` under the `reminal` org so users run `brew tap reminal/tap`.

### 5. Test install

```bash
brew untap reminal/tap 2>/dev/null || true
brew tap reminal/tap
brew install reminal
reminal version
```

---

## Before the first release (dev)

Install straight from source:

```bash
brew tap reminal/tap
brew install --HEAD reminal
```

---

## Future releases

1. Bump version in `homebrew-tap/Formula/reminal.rb`
2. Tag `v0.2.0` on main repo → wait for CI
3. `./scripts/update-formula-sha.sh v0.2.0` → update formula
4. Push tap repo

---

## Getting into homebrew-core (later)

Not required for launch. Core needs ~75 GitHub stars, tests, stable usage, and an accepted PR. Start with the tap; migrate when there's demand.
