#!/bin/sh
# reminal installer — downloads the latest release and installs to ~/.local/bin.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/harshalgajjar/Reminal/main/install.sh | sh
#
# Env overrides:
#   REMINAL_VERSION       Install a specific version (default: latest)
#   REMINAL_INSTALL_DIR   Install location (default: ~/.local/bin)

set -e

REPO="harshalgajjar/Reminal"
INSTALL_DIR="${REMINAL_INSTALL_DIR:-$HOME/.local/bin}"

# Detect OS.
case "$(uname -s)" in
    Darwin) OS="darwin" ;;
    Linux)  OS="linux" ;;
    *) echo "reminal: unsupported OS: $(uname -s)" >&2; exit 1 ;;
esac

# Detect architecture.
case "$(uname -m)" in
    arm64|aarch64)  ARCH="arm64" ;;
    x86_64|amd64)   ARCH="amd64" ;;
    *) echo "reminal: unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

# curl is required; check upfront for a clearer error.
if ! command -v curl >/dev/null 2>&1; then
    echo "reminal: curl is required to install. Install curl and try again." >&2
    exit 1
fi

# Resolve the latest version from the redirect of /releases/latest so we don't
# need a GitHub API token. The effective URL ends in /releases/tag/vX.Y.Z.
VERSION="$REMINAL_VERSION"
if [ -z "$VERSION" ]; then
    EFFECTIVE=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest")
    VERSION="${EFFECTIVE##*/v}"
fi
if [ -z "$VERSION" ]; then
    echo "reminal: failed to resolve latest version" >&2
    exit 1
fi

TARBALL="reminal_${VERSION}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/$REPO/releases/download/v${VERSION}/${TARBALL}"

echo "Installing reminal v${VERSION} (${OS}/${ARCH})..."

# Stage in a temp dir; cleaned up on exit.
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

curl -fsSL -o "$TMPDIR/$TARBALL" "$URL"
tar -xzf "$TMPDIR/$TARBALL" -C "$TMPDIR"

mkdir -p "$INSTALL_DIR"
mv "$TMPDIR/reminal" "$INSTALL_DIR/reminal"
chmod +x "$INSTALL_DIR/reminal"

# macOS: a binary downloaded via curl is not quarantined by Gatekeeper (only
# downloads from browsers/Mail/etc. get the com.apple.quarantine xattr), but
# strip it defensively in case a future install method re-introduces it.
if [ "$OS" = "darwin" ]; then
    xattr -d com.apple.quarantine "$INSTALL_DIR/reminal" 2>/dev/null || true
fi

echo
echo "Installed reminal v${VERSION} to ${INSTALL_DIR}/reminal"

# Heads-up if a different reminal is going to win on PATH.
EXISTING="$(command -v reminal 2>/dev/null || true)"
if [ -n "$EXISTING" ] && [ "$EXISTING" != "$INSTALL_DIR/reminal" ]; then
    echo
    echo "Note: another reminal is already on your PATH at: $EXISTING"
    echo "      It will take precedence. To remove a brew install: brew uninstall reminal"
fi

# Tell the user how to actually run it.
case ":$PATH:" in
    *":$INSTALL_DIR:"*)
        echo
        echo "Run: reminal"
        ;;
    *)
        echo
        echo "$INSTALL_DIR is not on your PATH. Add this to your shell rc:"
        echo "  export PATH=\"\$HOME/.local/bin:\$PATH\""
        echo
        echo "Or run directly:"
        echo "  $INSTALL_DIR/reminal"
        ;;
esac
