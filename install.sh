#!/bin/sh
# reminal installer — downloads the latest release and installs to ~/.local/bin.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/harshalgajjar/Reminal/main/install.sh | sh
#
# Env overrides:
#   REMINAL_VERSION       Install a specific version (default: latest)
#   REMINAL_INSTALL_DIR   Install location (default: ~/.local/bin)
#   REMINAL_ARCHIVE_URL   Install this build archive instead of a release from
#                         GitHub (give REMINAL_VERSION too, for the messages)
#   REMINAL_ARCHIVE_SHA256  The archive's SHA-256; nothing is installed unless
#                         the download matches it

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

# Set up shell integration for the user's shell: put reminal on PATH (so a fresh
# terminal can just run `reminal`) and enable tab-completion for session ids and
# names. Best-effort — never fails the install. Skip with REMINAL_NO_RC=1 (the
# older REMINAL_NO_COMPLETION=1 is still honored). Idempotent and self-repairing:
# each run rewrites a single marker-guarded block, so re-running the installer
# fixes a broken PATH and upgrades never duplicate. The PATH line only prepends
# when missing; the completion line sources `reminal completion ...` at startup,
# so it stays fresh across upgrades.
setup_shell() {
    { [ "${REMINAL_NO_RC:-}" = "1" ] || [ "${REMINAL_NO_COMPLETION:-}" = "1" ]; } && return 0

    _begin="# >>> reminal >>>"
    _end="# <<< reminal <<<"

    # Prefer a $HOME-relative dir so the rc line is portable and readable.
    case "$INSTALL_DIR" in
        "$HOME/"*) _rc_dir="\$HOME/${INSTALL_DIR#"$HOME"/}" ;;
        *)         _rc_dir="$INSTALL_DIR" ;;
    esac
    # PATH snippet for POSIX shells: prepend only when not already present, so
    # it's safe to run on every shell startup.
    _path_snip="case \":\$PATH:\" in *\":${_rc_dir}:\"*) ;; *) export PATH=\"${_rc_dir}:\$PATH\" ;; esac"

    _add_to_rc() { # $1=rcfile  $2=body
        _rc="$1"
        _dir=$(dirname "$_rc")
        [ -d "$_dir" ] || mkdir -p "$_dir" 2>/dev/null || return 0
        if [ ! -e "$_rc" ]; then : >"$_rc" 2>/dev/null || return 0; fi
        # Strip any previously-managed reminal block (this marker or the older
        # "reminal completion" one) so we rewrite exactly one fresh block.
        if grep -qE '^# >>> reminal( completion)? >>>' "$_rc" 2>/dev/null; then
            _tmp=$(mktemp) 2>/dev/null || return 0
            awk '
              /^# >>> reminal( completion)? >>>/ { skip=1 }
              skip==0 { print }
              /^# <<< reminal( completion)? <<</ { skip=0 }
            ' "$_rc" >"$_tmp" 2>/dev/null && cat "$_tmp" >"$_rc" 2>/dev/null
            rm -f "$_tmp" 2>/dev/null
        fi
        printf '\n%s\n%s\n%s\n' "$_begin" "$2" "$_end" >>"$_rc" 2>/dev/null || return 0
        RC_UPDATED=1
        say "  shell  $_rc"
    }

    # OSC 7 cwd announcement: lets reminal's Dir column (and any modern
    # terminal) follow cd's as EVENTS instead of being polled — the shell
    # tells the terminal where it is. Percent-encodes the minimum that breaks
    # a file:// URI. Harmless anywhere: unknown OSC is ignored by terminals.
    _osc7_fn='__reminal_osc7() {
  _p=$(printf "%s" "$PWD" | sed "s/%/%25/g; s/ /%20/g; s/#/%23/g")
  printf "\033]7;file://%s%s\007" "${HOST:-$(hostname 2>/dev/null)}" "$_p"
}'

    case "$(basename "${SHELL:-sh}")" in
        zsh)
            _add_to_rc "${ZDOTDIR:-$HOME}/.zshrc" "$_path_snip
if command -v reminal >/dev/null 2>&1; then
  (( \$+functions[compdef] )) || { autoload -Uz compinit && compinit -u; }
  source <(reminal completion zsh)
fi
$_osc7_fn
autoload -Uz add-zsh-hook 2>/dev/null && { add-zsh-hook chpwd __reminal_osc7; __reminal_osc7; }"
            ;;
        bash)
            _rcfile="$HOME/.bashrc"
            [ "$OS" = "darwin" ] && [ -e "$HOME/.bash_profile" ] && _rcfile="$HOME/.bash_profile"
            _add_to_rc "$_rcfile" "$_path_snip
command -v reminal >/dev/null 2>&1 && source <(reminal completion bash)
$_osc7_fn
case \";\$PROMPT_COMMAND;\" in *\";__reminal_osc7;\"*) ;; *) PROMPT_COMMAND=\"__reminal_osc7\${PROMPT_COMMAND:+;\$PROMPT_COMMAND}\" ;; esac"
            ;;
        fish)
            _fdir="${XDG_CONFIG_HOME:-$HOME/.config}/fish/completions"
            if mkdir -p "$_fdir" 2>/dev/null && printf '%s\n' 'reminal completion fish | source' >"$_fdir/reminal.fish" 2>/dev/null; then
                echo "  + tab-completion installed: $_fdir/reminal.fish"
            fi
            # fish uses its own PATH syntax; add a guarded block to config.fish.
            _add_to_rc "${XDG_CONFIG_HOME:-$HOME/.config}/fish/config.fish" "if not contains \"${_rc_dir}\" \$PATH
    set -gx PATH \"${_rc_dir}\" \$PATH
end"
            ;;
        *)
            echo "  (couldn't detect your shell — add ${INSTALL_DIR} to PATH and run: reminal completion <bash|zsh|fish>)"
            ;;
    esac
}

# Re-run shell setup only (no download): REMINAL_SETUP_COMPLETION_ONLY=1.
if [ "${REMINAL_SETUP_COMPLETION_ONLY:-}" = "1" ]; then
    setup_shell || true
    exit 0
fi

# curl is required; check upfront for a clearer error.
if ! command -v curl >/dev/null 2>&1; then
    echo "reminal: curl is required to install. Install curl and try again." >&2
    exit 1
fi

# Resolve the latest version from the redirect of /releases/latest so we don't
# need a GitHub API token. The effective URL ends in /releases/tag/vX.Y.Z.
VERSION="$REMINAL_VERSION"
if [ -n "${REMINAL_ARCHIVE_URL:-}" ] && [ -z "$VERSION" ]; then
    VERSION="(archive)"
fi
if [ -z "$VERSION" ]; then
    EFFECTIVE=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest")
    VERSION="${EFFECTIVE##*/v}"
fi
if [ -z "$VERSION" ]; then
    echo "reminal: failed to resolve latest version" >&2
    exit 1
fi

# Bold and dim only. Terminals remap the colour palette to suit their own
# background, but a bright/bold colour still washes out on a light one -- and
# the lines that would vanish first are the cost warnings, which are exactly the
# ones that must be read. Weight survives every theme, so structure is carried
# by weight and the separator, never by hue.
if [ -t 1 ]; then
    CB='\033[1m'; CD='\033[2m'; C0='\033[0m'
else
    CB=''; CD=''; C0=''
fi
say() { printf '%b\n' "$1"; }

TARBALL="reminal_${VERSION}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/$REPO/releases/download/v${VERSION}/${TARBALL}"
if [ -n "${REMINAL_ARCHIVE_URL:-}" ]; then
    URL="$REMINAL_ARCHIVE_URL"
    TARBALL="$(basename "$URL")"
fi

say "${CD}Downloading reminal v${VERSION} (${OS}/${ARCH})…${C0}"

# Stage in a temp dir; cleaned up on exit.
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

# A release is built one platform per job, so a single failed job publishes a
# release carrying every other platform's build and none for this one. Left to
# curl that is "error: 404" and nothing else — a first-run failure that reads
# like a broken installer rather than a release still finishing, or one whose
# build for this machine did not make it.
if ! curl -fsSL -o "$TMPDIR/$TARBALL" "$URL"; then
    if [ -n "${REMINAL_ARCHIVE_URL:-}" ]; then
        echo "reminal: could not download $URL." >&2
        exit 1
    fi
    if curl -fsSLI -o /dev/null "https://github.com/$REPO/releases/tag/v${VERSION}" 2>/dev/null; then
        echo "reminal: release v${VERSION} exists but has no ${OS}/${ARCH} build." >&2
        echo "  It may still be publishing — try again in a few minutes." >&2
        echo "  If it persists: https://github.com/$REPO/releases/tag/v${VERSION}" >&2
    else
        echo "reminal: could not download ${TARBALL}." >&2
        echo "  Check your connection, or see https://github.com/$REPO/releases" >&2
    fi
    exit 1
fi
# A digest given is a digest kept: a build that is not the one named is never
# unpacked, let alone installed.
if [ -n "${REMINAL_ARCHIVE_SHA256:-}" ]; then
    if command -v sha256sum >/dev/null 2>&1; then
        GOT=$(sha256sum "$TMPDIR/$TARBALL" | cut -d' ' -f1)
    else
        GOT=$(shasum -a 256 "$TMPDIR/$TARBALL" | cut -d' ' -f1)
    fi
    WANT=$(printf '%s' "$REMINAL_ARCHIVE_SHA256" | tr 'A-F' 'a-f')
    if [ "$GOT" != "$WANT" ]; then
        echo "reminal: the downloaded build is not the one that was published (sha256 $GOT, expected $WANT)." >&2
        echo "  Nothing was installed." >&2
        exit 1
    fi
fi
if ! tar -xzf "$TMPDIR/$TARBALL" -C "$TMPDIR"; then
    echo "reminal: the downloaded archive could not be extracted (truncated download?)." >&2
    exit 1
fi

mkdir -p "$INSTALL_DIR"

# macOS ships reminal.app — a signed bundle holding the CLI, the ScreenCaptureKit
# helper, and the icon under ONE code identity, so a single Screen Recording grant
# also covers the background daemon (the "+" sessions). Install the bundle to
# ~/Applications and symlink the CLI onto PATH so `reminal` works exactly as
# before. Linux (and any older bare-binary release) installs the plain binary.
APP_DIR="${REMINAL_APP_DIR:-$HOME/Applications}"
if [ "$OS" = "darwin" ] && [ -d "$TMPDIR/reminal.app" ]; then
    mkdir -p "$APP_DIR"
    rm -rf "$APP_DIR/reminal.app"
    mv "$TMPDIR/reminal.app" "$APP_DIR/reminal.app"
    xattr -dr com.apple.quarantine "$APP_DIR/reminal.app" 2>/dev/null || true
    ln -sf "$APP_DIR/reminal.app/Contents/MacOS/reminal" "$INSTALL_DIR/reminal"
    # Register with LaunchServices so Finder/Settings show the icon.
    _lsr="/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"
    [ -x "$_lsr" ] && "$_lsr" -f "$APP_DIR/reminal.app" >/dev/null 2>&1 || true
    APP_PATH="${APP_DIR}/reminal.app"
else
    mv "$TMPDIR/reminal" "$INSTALL_DIR/reminal"
    chmod +x "$INSTALL_DIR/reminal"
    # macOS: a binary downloaded via curl is not quarantined by Gatekeeper (only
    # downloads from browsers/Mail/etc. get the com.apple.quarantine xattr), but
    # strip it defensively in case a future install method re-introduces it.
    [ "$OS" = "darwin" ] && xattr -d com.apple.quarantine "$INSTALL_DIR/reminal" 2>/dev/null || true
    APP_PATH=""
fi

echo
say "${CB}reminal v${VERSION} installed${C0}"
# An `if`, not `[ … ] && …`: under `set -e` a false test makes the whole
# compound non-zero and exits the script — which on Linux, where there is no
# app bundle, would end the install right here.
if [ -n "${APP_PATH:-}" ]; then say "  app    ${APP_PATH}"; fi
say "  cli    ${INSTALL_DIR}/reminal"

# macOS bundle post-install:
if [ "$OS" = "darwin" ] && [ -d "$APP_DIR/reminal.app" ]; then
    # Always run the background daemon. It's the single process that performs all
    # screen capture + input injection, so ONE grant to reminal.app (via
    # `reminal permissions`) covers every session — terminal or "+". `daemon
    # --install` writes the LaunchAgent pointing at the bundle binary and starts it
    # (idempotent; on upgrade it re-points + restarts).
    "$INSTALL_DIR/reminal" daemon --install >/dev/null 2>&1 || true
    # Stale loose helper from a previous bare install (the bundle carries its own).
    rm -f "$INSTALL_DIR/reminal-capture" 2>/dev/null || true
    rm -f "$INSTALL_DIR/reminal-overlay" 2>/dev/null || true
fi

# Set up shell integration: PATH + tab-completion (best-effort).
setup_shell || true

# Heads-up if a different reminal is going to win on PATH.
EXISTING="$(command -v reminal 2>/dev/null || true)"
if [ -n "$EXISTING" ] && [ "$EXISTING" != "$INSTALL_DIR/reminal" ]; then
    echo
    echo "Note: another reminal is already on your PATH at: $EXISTING"
    echo "      It will take precedence. To remove a brew install: brew uninstall reminal"
fi

# ---- One-time setup ---------------------------------------------------------
# Walk the user through the steps rather than printing commands to remember
# later. `curl | sh` leaves stdin pointing at the script, so questions are read
# from /dev/tty; with no terminal at all (CI, image builds) we print the same
# list instead of hanging on a read that can never return.
#
# Styling follows `reminal permissions`, which these steps hand off to: a bold
# heading at column 0 and its detail indented two. Matching that grammar is what
# makes the nested output read as hierarchy instead of two margins colliding --
# and the step heading is coloured so the installer's voice stays distinct from
# the command it just launched.
SKIPPED=""
skip() { SKIPPED="$SKIPPED  $1\n"; }

if [ "$OS" = "darwin" ]; then TOTAL=4; else TOTAL=1; fi
STEP=0
# Heading flush left, detail indented two -- the exact shape `reminal
# permissions` uses, so its own Step blocks nest under ours instead of sitting
# two columns off it. A dim rule above each gives the separation that a blank
# line alone did not.
rule() { printf '%b\n' "${CD}────────────────────────────────────────────${C0}"; }
next_step() {
    STEP=$((STEP + 1))
    printf '\n'
    rule
    printf '%b\n' "${CB}Setup $STEP/$TOTAL — $1${C0}"
}
ask() {
    printf '\n  %b [Y/n] ' "${CB}$1${C0}" >/dev/tty
    read -r _ans </dev/tty || _ans=""
    printf '\n' >/dev/tty
    # Close the block only when something follows it. A skipped step prints
    # nothing, so a closing rule there would collide with the next step's
    # opening one and read as a doubled divider.
    case "$_ans" in
        [nN]|[nN][oO]) return 1 ;;
        *) rule; return 0 ;;
    esac
}

# Opening it is the only honest test: /dev/tty exists as a device node even with
# no controlling terminal (`docker run -i`, CI), where -r/-w both pass and every
# read and write then fails with "No such device or address".
#
# In a SUBSHELL, because `:` is a POSIX special builtin and a redirection failure
# on one of those exits a non-interactive shell outright -- which killed the
# whole installer on exactly the piped `curl | sh` path this test exists for.
if ( : >/dev/tty ) 2>/dev/null; then
    say ""
    if [ "$TOTAL" = 1 ]; then
        say "${CB}One quick step${C0} — skip it and run it later."
    else
        say "${CB}A few one-time steps${C0} — skip any of them and run them later."
    fi

    if [ "$OS" = "darwin" ]; then
        next_step "Screen permissions"
        say "  Lets you mirror and control this Mac's windows from anywhere."
        say "  macOS asks for Screen Recording and Accessibility, one at a time."
        if ask "Grant now?"; then
            "$INSTALL_DIR/reminal" permissions </dev/tty || true
        else
            skip "reminal permissions     mirror and control windows"
        fi
    fi

    next_step "Coding agents"
    say "  Shows each session as working, needs you, or done, so you can tell"
    say "  from your phone which one to go back to. And lets the agents here"
    say "  list your sessions, read what is on them, and type into them."
    if ask "Set up now?"; then
        # -y because the question above WAS the consent. Without it the user is
        # asked twice, and the second one defaults to no — so answering yes and
        # pressing Enter installed nothing.
        "$INSTALL_DIR/reminal" integrate -y </dev/tty || true
    else
        skip "reminal integrate       show session states, and let agents drive them"
    fi

    if [ "$OS" = "darwin" ]; then
        next_step "Keep serving with the lid shut"
        say "  Closing the lid normally sleeps this Mac and your sessions stop."
        say "  Uses more battery, and asks for your admin password once."
        if ask "Turn on?"; then
            "$INSTALL_DIR/reminal" settings closed-lid on </dev/tty || true
        else
            skip "reminal settings        keep serving with the lid shut"
        fi

        next_step "Keep it from locking"
        say "  A locked Mac ignores remote clicks and keystrokes, so window"
        say "  control stops while you are away. Terminals work either way."
        say "  Costs a lit screen."
        if ask "Turn on?"; then
            "$INSTALL_DIR/reminal" settings always-unlocked on </dev/tty || true
        else
            skip "reminal settings        stop it locking while you are away"
        fi
    fi
else
    # No terminal to ask on — same steps, printed.
    say ""
    say "One-time setup, when you are at a terminal:"
    if [ "$OS" = "darwin" ]; then
        say "  reminal permissions     mirror and control windows"
    fi
    say "  reminal integrate       show session states, and let agents drive them"
    if [ "$OS" = "darwin" ]; then
        say "  reminal settings        keep serving with the lid shut, and stop it locking"
    fi
fi

# Tell the user how to actually run it.
echo
case ":$PATH:" in
    *":$INSTALL_DIR:"*)
        echo "Run: reminal"
        ;;
    *)
        if [ -n "${RC_UPDATED:-}" ]; then
            # We added INSTALL_DIR to the rc, but the current shell hasn't
            # re-read it — so `reminal` won't resolve here until a new shell.
            echo "Added $INSTALL_DIR to your PATH. Open a new terminal and run:"
            echo "  reminal"
            echo
            echo "Or use it right now in this shell:"
            echo "  export PATH=\"$INSTALL_DIR:\$PATH\" && reminal"
        else
            echo "$INSTALL_DIR is not on your PATH. Add this to your shell rc:"
            echo "  export PATH=\"$INSTALL_DIR:\$PATH\""
            echo
            echo "Or run directly:"
            echo "  $INSTALL_DIR/reminal"
        fi
        ;;
esac

if [ -n "$SKIPPED" ]; then
    printf '\n%b\n' "${CB}Skipped — run these any time:${C0}"
    printf '%b' "$SKIPPED"
fi
