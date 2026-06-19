# Homebrew install

```bash
brew tap harshalgajjar/reminal
brew install reminal
```

## How it works

GitHub redirects `harshalgajjar/homebrew-reminal` → `harshalgajjar/Reminal` (same repo after rename).

The repo contains source code **and** `Formula/reminal.rb`. Homebrew only reads the formula.

## Update formula after a release

```bash
./scripts/update-formula-sha.sh v0.1.0
# Update Formula/reminal.rb and homebrew-tap/Formula/reminal.rb (keep in sync)
git commit -am "Update formula checksums for v0.1.0"
git push
```

## Optional: separate tap repo later

If you create a standalone `harshalgajjar/homebrew-reminal` repo (no redirect), push only `homebrew-tap/` contents there.
