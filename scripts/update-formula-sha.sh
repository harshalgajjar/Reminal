#!/bin/bash
# Print sha256 checksums for GitHub release assets (paste into homebrew-tap/Formula/reminal.rb).
set -euo pipefail

TAG="${1:?Usage: $0 v0.1.0}"
VERSION="${TAG#v}"
BASE="https://github.com/reminal/reminal/releases/download/${TAG}"

for pair in \
  "darwin_arm64:REPLACE_ARM64_MACOS" \
  "darwin_amd64:REPLACE_AMD64_MACOS" \
  "linux_arm64:REPLACE_ARM64_LINUX" \
  "linux_amd64:REPLACE_AMD64_LINUX"
do
  arch="${pair%%:*}"
  label="${pair##*:}"
  file="reminal_${VERSION}_${arch}.tar.gz"
  url="${BASE}/${file}"
  echo "Fetching ${file}..."
  sha=$(curl -fsSL "${url}" | shasum -a 256 | awk '{print $1}')
  echo "${label}: ${sha}"
  echo
done

echo "Update homebrew-tap/Formula/reminal.rb with these sha256 values."
