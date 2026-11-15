#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# Reads off a signed reminal.app what the hardened runtime will ask of it:
# the main binary must be signed with the runtime and entitled to run the
# code its terminal engine compiles in memory. Without the entitlement a Mac
# kills reminal the first time a session starts (3.20.0 shipped that way).
#
# Starting the engine is not enough to tell: a Mac with System Integrity
# Protection off, as GitHub's macOS runners are, runs it either way. The
# signature says the same thing on every Mac.
#
#   scripts/check-app-signature.sh <path/to/reminal.app>
set -euo pipefail
APP="${1:?usage: check-app-signature.sh <reminal.app>}"
bin="$APP/Contents/MacOS/reminal"
fail() { echo "check-app-signature: $*" >&2; exit 1; }
[ -f "$bin" ] || fail "no $bin"
info="$(codesign -dvvv "$bin" 2>&1)" || fail "$bin is not signed"
case "$info" in *"flags="*"runtime"*) ;; *) fail "$bin is not signed with the hardened runtime" ;; esac
ents="$(codesign -d --entitlements - "$bin" 2>&1)" || fail "cannot read the entitlements of $bin"
case "$ents" in
  *com.apple.security.cs.allow-unsigned-executable-memory*) ;;
  *) fail "$bin lacks com.apple.security.cs.allow-unsigned-executable-memory: a Mac would kill it as its terminal engine starts" ;;
esac
echo "check-app-signature: $bin has the hardened runtime and may run its terminal engine"
