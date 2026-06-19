#!/bin/bash
# Print sha256 checksums for GitHub release assets (paste into homebrew-tap/Formula/reminal.rb).
set -euo pipefail

TAG="${1:?Usage: $0 v0.1.0}"
VERSION="${TAG#v}"

# Auto-detect owner/repo from git remote, or override:
#   REMINAL_GITHUB_REPO=reminal/reminal ./scripts/update-formula-sha.sh v0.1.0
if [[ -z "${REMINAL_GITHUB_REPO:-}" ]]; then
  REMINAL_GITHUB_REPO=$(git remote get-url origin 2>/dev/null | sed -E 's#(git@github.com:|https://github.com/)([^/.]+)/([^/.]+)(\.git)?#\2/\3#')
  REMINAL_GITHUB_REPO="${REMINAL_GITHUB_REPO:-harshalgajjar/Reminal}"
fi

if [[ -z "$REMINAL_GITHUB_REPO" ]]; then
  echo "Set REMINAL_GITHUB_REPO=owner/repo or run from a git repo with origin remote" >&2
  exit 1
fi

BASE="https://github.com/${REMINAL_GITHUB_REPO}/releases/download/${TAG}"
echo "Release: ${REMINAL_GITHUB_REPO} ${TAG}"
echo

for pair in \
  "darwin_arm64:macOS Apple Silicon" \
  "darwin_amd64:macOS Intel" \
  "linux_arm64:Linux ARM64" \
  "linux_amd64:Linux x86_64"
do
  arch="${pair%%:*}"
  label="${pair##*:}"
  file="reminal_${VERSION}_${arch}.tar.gz"
  url="${BASE}/${file}"
  echo "Fetching ${file}..."
  if sha=$(curl -fsSL "${url}" | shasum -a 256 | awk '{print $1}'); then
    echo "  ${label}: sha256 \"${sha}\""
  else
    echo "  ${label}: MISSING (404 or download failed)" >&2
  fi
  echo
done

echo "Update homebrew-tap/Formula/reminal.rb with the sha256 values above."
