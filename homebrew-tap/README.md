# reminal Homebrew tap

Install reminal:

```bash
brew tap reminal/tap
brew install reminal
```

This repository is published separately from [reminal/reminal](https://github.com/reminal/reminal).

## Maintainer: publish a new version

1. Tag a release on the main repo (`v0.1.0` → GitHub Actions uploads binaries).
2. From the main repo, run:

   ```bash
   ./scripts/update-formula-sha.sh v0.1.0
   ```

3. Copy the printed `sha256` values into `Formula/reminal.rb` and bump `version` / URLs.
4. Commit and push this tap repo.

## Install from source (before first release)

```bash
brew tap reminal/tap
brew install --HEAD reminal
```
