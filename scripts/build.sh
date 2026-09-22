#!/bin/bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# Personal/self-hosted relay defaults live outside version control. Copy
# reminal.build.env.example to reminal.build.env, or point this at another file.
BUILD_CONFIG="${REMINAL_BUILD_CONFIG:-${ROOT}/reminal.build.env}"
ENV_RELAY_SET="${REMINAL_DEFAULT_RELAY+x}"
ENV_WEB_SET="${REMINAL_DEFAULT_WEB+x}"
ENV_RELAY="${REMINAL_DEFAULT_RELAY:-}"
ENV_WEB="${REMINAL_DEFAULT_WEB:-}"
if [[ -f "$BUILD_CONFIG" ]]; then
  # Parse inert KEY=VALUE data rather than sourcing shell code. Keep the
  # allowlist deliberately small so a typo cannot silently change the build.
  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%$'\r'}"
    [[ "$line" =~ ^[[:space:]]*$ || "$line" =~ ^[[:space:]]*# ]] && continue
    if [[ "$line" != *=* ]]; then
      echo "Invalid build config line in ${BUILD_CONFIG}: ${line}" >&2
      exit 2
    fi
    key="${line%%=*}"
    value="${line#*=}"
    case "$key" in
      REMINAL_DEFAULT_RELAY) REMINAL_DEFAULT_RELAY="$value" ;;
      REMINAL_DEFAULT_WEB) REMINAL_DEFAULT_WEB="$value" ;;
      *)
        echo "Unsupported build config key in ${BUILD_CONFIG}: ${key}" >&2
        exit 2
        ;;
    esac
  done < "$BUILD_CONFIG"
fi
# An explicit command environment has higher precedence than the convenience
# file (including an explicitly empty value used to disable one file entry).
if [[ -n "$ENV_RELAY_SET" ]]; then
  REMINAL_DEFAULT_RELAY="$ENV_RELAY"
  [[ -z "$ENV_WEB_SET" ]] && REMINAL_DEFAULT_WEB=""
fi
if [[ -n "$ENV_WEB_SET" ]]; then
  REMINAL_DEFAULT_WEB="$ENV_WEB"
  [[ -z "$ENV_RELAY_SET" ]] && REMINAL_DEFAULT_RELAY=""
fi

# Local/fork builds must not masquerade as an old upstream release: doing so
# makes the startup updater offer to replace the customized binary. Release CI
# passes VERSION explicitly, while an ordinary local build stays "dev" and
# intentionally skips upstream self-update checks.
VERSION="${VERSION:-dev}"
OUTPUT="${OUTPUT:-dist/reminal}"

mkdir -p dist

DEFAULT_RELAY="${REMINAL_DEFAULT_RELAY:-}"
DEFAULT_WEB="${REMINAL_DEFAULT_WEB:-}"
if [[ -n "$DEFAULT_RELAY" && ! "$DEFAULT_RELAY" =~ ^wss?:// ]]; then
  echo "REMINAL_DEFAULT_RELAY must start with ws:// or wss://" >&2
  exit 2
fi
if [[ -n "$DEFAULT_WEB" && ! "$DEFAULT_WEB" =~ ^https?:// ]]; then
  echo "REMINAL_DEFAULT_WEB must start with http:// or https://" >&2
  exit 2
fi
# Build defaults replace the upstream pair together. When only one is supplied,
# derive the other so a fork never opens sockets on one relay while printing
# links for another.
if [[ -n "$DEFAULT_RELAY" && -z "$DEFAULT_WEB" ]]; then
  DEFAULT_WEB="${DEFAULT_RELAY%/}"
  DEFAULT_WEB="${DEFAULT_WEB%/ws}"
  DEFAULT_WEB="${DEFAULT_WEB/#wss:\/\//https://}"
  DEFAULT_WEB="${DEFAULT_WEB/#ws:\/\//http://}"
elif [[ -n "$DEFAULT_WEB" && -z "$DEFAULT_RELAY" ]]; then
  DEFAULT_RELAY="${DEFAULT_WEB%/}"
  DEFAULT_RELAY="${DEFAULT_RELAY/#https:\/\//wss://}"
  DEFAULT_RELAY="${DEFAULT_RELAY/#http:\/\//ws://}/ws"
fi
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS=(-s -w -X "main.version=${VERSION}" -X "main.buildDate=${BUILD_DATE}" -X "main.commit=${COMMIT}")
[[ -n "$DEFAULT_RELAY" ]] && LDFLAGS+=(-X "reminal/internal/config.DefaultCloudRelay=${DEFAULT_RELAY%/}")
[[ -n "$DEFAULT_WEB" ]] && LDFLAGS+=(-X "reminal/internal/config.DefaultCloudWeb=${DEFAULT_WEB%/}")

echo "Building reminal ${VERSION}..."
if [[ -n "$DEFAULT_RELAY" || -n "$DEFAULT_WEB" ]]; then
  echo "Embedding configured relay defaults from ${BUILD_CONFIG} / environment."
else
  echo "Using the upstream relay defaults from internal/config (override with reminal.build.env)."
fi
CGO_ENABLED=0 go build -trimpath -ldflags "${LDFLAGS[*]}" -o "${OUTPUT}" ./cmd/reminal

echo "Built ${OUTPUT}"
"${OUTPUT}" version

# macOS: compile the ScreenCaptureKit capture helper alongside the binary. The
# agent auto-discovers reminal-capture next to itself and uses it for a native,
# ~30fps window mirror (falling back to screencapture if it's absent). Skipped on
# non-macOS and when swiftc isn't installed — the mirror still works, just slower.
if [[ "$(uname)" == "Darwin" ]]; then
  if command -v swiftc >/dev/null 2>&1; then
    HELPER="$(dirname "${OUTPUT}")/reminal-capture"
    echo "Building capture helper ${HELPER}..."
    # Same 12.3 deployment target as CI (the ScreenCaptureKit floor), so a
    # locally-built helper doesn't silently require the build machine's OS.
    swiftc -O -target "$(uname -m)-apple-macos12.3" -o "${HELPER}" native/reminal-capture/main.swift
    codesign --force --sign - "${HELPER}" >/dev/null 2>&1 || true
    echo "Built ${HELPER}"
    # Window-annotation helper: `reminal mcp` shells out to it, so it has to sit
    # next to the CLI or the MCP tools report "helper not found".
    OVERLAY="$(dirname "${OUTPUT}")/reminal-overlay"
    swiftc -O -target "$(uname -m)-apple-macos12.3" -o "${OVERLAY}" native/reminal-overlay/main.swift
    codesign --force --sign - "${OVERLAY}" >/dev/null 2>&1 || true
    echo "Built ${OVERLAY}"
    # Assemble reminal.app so local builds match the release layout (one signed
    # bundle: CLI + helper + icon). Ad-hoc signed here — an ad-hoc bundle can't
    # persist a Screen Recording grant (releases use the stable cert), but it's
    # enough to exercise the bundle path locally.
    OUT_DIR="$(dirname "${OUTPUT}")"
    if [ -f assets/reminal.icns ]; then
      VERSION="${VERSION}" "$(dirname "$0")/build-app.sh" "${OUT_DIR}" "${OUT_DIR}" - >/dev/null && echo "Assembled ${OUT_DIR}/reminal.app"
    fi
  else
    echo "swiftc not found — skipping capture helper (window mirror will use screencapture)"
  fi
fi
