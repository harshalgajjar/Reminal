#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# Verify reminal's pi extension end to end, inside the container.
#
# Four steps, each one a thing that has broken or could:
#   1. the Go side that installs the extension,
#   2. the extension's own behaviour against a real `reminal mcp`,
#   3. `reminal integrate pi` putting it where pi looks,
#   4. a real pi loading it and offering its tools to a model.
#
# Anything passed on the command line runs instead of the checks, so
# `run.sh bash` gets you a shell in the same box.
set -euo pipefail

if [ "$#" -gt 0 ]; then
    exec "$@"
fi

say() { printf '\n\033[1m== %s\033[0m\n' "$1"; }

say "versions"
go version
node --version
echo "pi $(pi --version)"

say "1. the installer (go test)"
go test ./internal/piext/

say "2. build reminal"
# Not in /src: the mounted repo belongs to the host, and a Linux binary dropped
# in it would confuse whatever is building there.
go build -o /tmp/reminal ./cmd/reminal
/tmp/reminal --version || true

say "3. the extension against a real reminal mcp"
REMINAL_BIN=/tmp/reminal node --experimental-strip-types \
    internal/piext/extension/test/harness.ts

say "4. reminal integrate pi"
# A home of its own, so nothing here can touch a real pi config.
export HOME=/tmp/pihome
rm -rf "$HOME"
mkdir -p "$HOME"
/tmp/reminal integrate pi -y
echo
find "$HOME/.pi" -type f | sort

say "5. a real pi loads it"
# --approve trusts the working directory. The probe exits during startup, so the
# prompt is never sent and no credentials are needed or used.
cd /tmp
OUT=$(ANTHROPIC_API_KEY=never-used timeout 120 pi \
    --approve \
    --provider anthropic \
    -e /opt/pi-test/probe.ts \
    -p "never sent: the probe exits first" 2>&1 || true)

echo "$OUT" | grep -a '^PROBE' || { echo "$OUT" | tail -40; echo; echo "FAIL: pi never reported its tools"; exit 1; }

# The extension is worth nothing if the tools are registered but not active.
ACTIVE=$(echo "$OUT" | grep -a '^PROBE active:' | sed 's/^PROBE active: //')
for tool in list_sessions read_transcript send_keys add_note; do
    case ",$ACTIVE," in
        *",$tool,"*) ;;
        *) echo "FAIL: $tool is not active in pi"; exit 1 ;;
    esac
done

# And pi's own tools must still be there: shadowing one would be a silent
# downgrade of the agent.
for tool in read bash edit write; do
    case ",$ACTIVE," in
        *",$tool,"*) ;;
        *) echo "FAIL: pi's own $tool went missing"; exit 1 ;;
    esac
done

printf '\n\033[32mall good\033[0m — reminal'"'"'s tools are live in pi, and pi kept its own.\n'
