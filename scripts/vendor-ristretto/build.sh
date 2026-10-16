#!/usr/bin/env bash
# Rebuilds the viewer's vendored ristretto255 (internal/client/web/vendor/; the
# Worker's copy is built from it) from a pinned @noble/curves.
#
# The viewer needs ristretto255 for its CPace handshake and WebCrypto has none,
# so a small, audited library is bundled and served from our own origin with
# SRI rather than fetched from a third party at handshake time. Run this to
# reproduce or update it, then put the printed integrity value in app.js (RISTRETTO_SRI).
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd ../.. && pwd)
NOBLE=1.9.7
ESBUILD=0.28.2

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp entry.mjs "$work/"
(cd "$work" && npm init -y >/dev/null && npm install --silent --no-audit --no-fund \
  "@noble/curves@$NOBLE" "esbuild@$ESBUILD" >/dev/null)
(cd "$work" && npx esbuild entry.mjs --bundle --format=iife --minify \
  --legal-comments=inline --outfile=ristretto255.js >/dev/null 2>&1)

mkdir -p "$ROOT/internal/client/web/vendor"
cp "$work/ristretto255.js" "$ROOT/internal/client/web/vendor/ristretto255.js"
echo "sha384-$(openssl dgst -sha384 -binary "$work/ristretto255.js" | openssl base64 -A)"
