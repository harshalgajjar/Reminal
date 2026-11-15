#!/usr/bin/env bash
# Assemble reminal.app from built binaries + the app icon, and sign it.
# Shared by CI (release.yml) and local dev. macOS only.
#
# Why a bundle: macOS keys a Screen Recording (TCC) grant to a code identity and
# only shows a custom icon for an .app. Packaging reminal + reminal-capture in
# one signed bundle gives ONE identity (sh.reminal) that the user grants once —
# and because the background daemon runs as sh.reminal and spawns reminal-capture,
# TCC attributes the helper's capture to that one granted identity. It also makes
# Settings show the "re" logo.
#
# Usage:
#   scripts/build-app.sh <bin-dir> <out-dir> [identity]
# Env:
#   VERSION    version string for Info.plist   (default 0.0.0)
#   KEYCHAIN   keychain holding the signing identity (optional)
# <bin-dir> must contain `reminal` and `reminal-capture`; `reminal-overlay` is
# included when present.
# [identity] omitted or "-" => ad-hoc signature (dev only; can't hold a TCC grant).
set -euo pipefail

BINDIR="${1:?usage: build-app.sh <bin-dir> <out-dir> [identity]}"
OUTDIR="${2:?usage: build-app.sh <bin-dir> <out-dir> [identity]}"
IDENTITY="${3:--}"
VERSION="${VERSION:-0.0.0}"
KEYCHAIN="${KEYCHAIN:-}"

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/.." && pwd)"

APP="$OUTDIR/reminal.app"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$BINDIR/reminal"           "$APP/Contents/MacOS/reminal"
cp "$BINDIR/reminal-capture"   "$APP/Contents/MacOS/reminal-capture"
[ -f "$BINDIR/reminal-overlay" ] && cp "$BINDIR/reminal-overlay" "$APP/Contents/MacOS/reminal-overlay"
cp "$root/assets/reminal.icns" "$APP/Contents/Resources/reminal.icns"

/usr/libexec/PlistBuddy \
  -c "Add :CFBundleName string reminal" \
  -c "Add :CFBundleDisplayName string reminal" \
  -c "Add :CFBundleIdentifier string sh.reminal" \
  -c "Add :CFBundleExecutable string reminal" \
  -c "Add :CFBundleIconFile string reminal" \
  -c "Add :CFBundlePackageType string APPL" \
  -c "Add :CFBundleShortVersionString string $VERSION" \
  -c "Add :CFBundleVersion string $VERSION" \
  -c "Add :LSMinimumSystemVersion string 12.3" \
  -c "Add :LSUIElement bool true" \
  "$APP/Contents/Info.plist" >/dev/null

kc=(); [ -n "$KEYCHAIN" ] && kc=(--keychain "$KEYCHAIN")

# Sign the nested helper (its own identifier), then seal the bundle — the main
# `reminal` inherits CFBundleIdentifier sh.reminal from Info.plist.
#
# Every binary is signed with the hardened runtime (--options runtime): the
# dynamic loader ignores DYLD_* variables for them and they load only
# libraries signed by Apple or by this same certificate. These processes hold
# the app's Screen Recording and Accessibility grants, so nothing else gets to
# run code inside them. The flag is not part of the Designated Requirement
# (identifier + certificate), so grants made to an earlier build still match.
#
# The helpers carry no entitlements. The main reminal carries one
# (reminal.entitlements): its terminal engine is compiled to machine code in
# memory, which the hardened runtime otherwise kills it for. Library
# validation and the DYLD_* rule stay as they are.
rt=(--options runtime)
codesign --force --timestamp=none "${rt[@]}" ${kc[@]+"${kc[@]}"} --identifier sh.reminal.capture --sign "$IDENTITY" "$APP/Contents/MacOS/reminal-capture"
[ -f "$APP/Contents/MacOS/reminal-overlay" ] && codesign --force --timestamp=none "${rt[@]}" ${kc[@]+"${kc[@]}"} --identifier sh.reminal.overlay --sign "$IDENTITY" "$APP/Contents/MacOS/reminal-overlay"
codesign --force --timestamp=none "${rt[@]}" ${kc[@]+"${kc[@]}"} --entitlements "$here/reminal.entitlements" --sign "$IDENTITY" "$APP"

codesign --verify --deep --strict "$APP"

# The signed app starts its terminal engine, as every session does: a build
# the hardened runtime would kill (2.8.0 / 3.20.0 shipped one) fails here.
# Only where this Mac can run the binary's architecture.
if [ "$(lipo -archs "$APP/Contents/MacOS/reminal" 2>/dev/null)" = "$(uname -m)" ]; then
  "$APP/Contents/MacOS/reminal" __engine-check || { echo "build-app: the signed reminal cannot start its terminal engine" >&2; exit 1; }
else
  echo "build-app: engine check skipped (binary is $(lipo -archs "$APP/Contents/MacOS/reminal" 2>/dev/null), this Mac is $(uname -m))"
fi
echo "built: $APP  (identity: ${IDENTITY}, version: ${VERSION})"
