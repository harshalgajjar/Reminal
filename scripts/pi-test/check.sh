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

say "6. pi running inside an actual reminal session"
# The steps above test the pieces. This one is the thing itself: a real session,
# with a real pi in it, read the way another machine would read it. It is the
# only step that exercises the parts nothing else can reach — that reminal knows
# a pi session on sight and reads its SCREEN rather than the cursor moves it was
# painted with, and that the extension can find reminal without it being on PATH.
export REMINAL_LOCAL=1  # a test box must never register on the public relay

/tmp/reminal new pitest >/dev/null 2>&1
sleep 2
ID=$(/tmp/reminal list 2>/dev/null | sed -n 's/^  pitest  \([A-Z0-9]*\).*/\1/p' | head -1)
[ -n "$ID" ] || { echo "FAIL: no session started"; exit 1; }
echo "session $ID"

# One MCP request, start to finish: a fresh server, the handshake, one call.
mcp_call() {
    printf '%s\n%s\n%s\n' \
        '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"check","version":"1"}}}' \
        '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
        "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"$1\",\"arguments\":$2}}" \
    | timeout 30 /tmp/reminal mcp 2>/dev/null | python3 -c '
import json,sys
for line in sys.stdin:
    try: m = json.loads(line)
    except Exception: continue
    if m.get("id") == 2:
        for c in (m.get("result") or {}).get("content", []):
            print(c.get("text",""))
'
}

mcp_call send_keys "{\"session\":\"$ID\",\"keys\":\"pi --approve\",\"enter\":true}" >/dev/null
sleep 14

# Reading a session is half of it; driving one is the other half. pi's `!` prefix
# runs a shell command and shows the output, with no credentials and no network,
# so the marker coming back proves the Return actually submitted rather than
# sitting in the input box — the failure send_keys' own description warns about.
mcp_call send_keys "{\"session\":\"$ID\",\"keys\":\"!echo LANDED-IN-PI\",\"enter\":true}" >/dev/null
sleep 5

mcp_call read_transcript "{\"session\":\"$ID\"}" > /tmp/transcript.txt
FG=$(python3 -c "import json;print(json.load(open('$HOME/.reminal/active-$ID.json')).get('fg') or '')")
/tmp/reminal kill "$ID" -y >/dev/null 2>&1

echo "foreground reminal sees: ${FG:-<none>}"
[ "$FG" = "pi" ] || { echo "FAIL: reminal sees the foreground as '${FG:-<none>}', not pi"; exit 1; }

# pi draws on the main screen, so nothing but being a known agent makes reminal
# read it. Without that, read_transcript answers with the raw stream — fragments
# run together — and an agent on another machine cannot see what pi is showing.
grep -q -- "--- the screen now ---" /tmp/transcript.txt || {
    echo "FAIL: read_transcript returned no screen for a pi session"; exit 1; }
echo "read_transcript: includes the screen"

# The extension found reminal and got its tools. Without bin.json it falls back
# to PATH, which on a fresh install is exactly where reminal is not.
if grep -q "reminal tools unavailable" /tmp/transcript.txt; then
    grep -o "reminal tools unavailable[^\\\\]*" /tmp/transcript.txt | head -1
    echo "FAIL: the extension could not reach reminal from inside pi"; exit 1
fi
echo "the extension reached reminal without it being on PATH"

grep -q "LANDED-IN-PI" /tmp/transcript.txt || {
    echo "FAIL: send_keys did not submit into pi — the text never ran"; exit 1; }
echo "send_keys: the Return landed and pi ran it"

say "7. the attention state comes from pi, not from the screen"
# The states moving is not proof the hooks did it: while pi streams, the screen
# moves too, and the screen detector would report the same thing. So give pi a
# model that says NOTHING for 25 seconds. The screen freezes, the screen detector
# calls a still screen settled within a couple of seconds, and anything still
# reporting "working" can only have come from the agent's own hook.
FAKE_MODEL_DELAY_MS=25000 FAKE_MODEL_STREAM_MS=1000 \
    node /src/scripts/pi-test/fake-model.mjs >/dev/null 2>&1 &
MODEL_PID=$!
sleep 1

/tmp/reminal new attntest >/dev/null 2>&1
sleep 2
AID=$(/tmp/reminal list 2>/dev/null | sed -n 's/^  attntest  \([A-Z0-9]*\).*/\1/p' | head -1)
[ -n "$AID" ] || { echo "FAIL: no session started"; exit 1; }

PI_ARGS="--approve --provider fake --model fake-1 -e /src/scripts/pi-test/fake-provider.ts"
mcp_call send_keys "{\"session\":\"$AID\",\"keys\":\"pi $PI_ARGS\",\"enter\":true}" >/dev/null
sleep 14

# What the record says, and what the extension actually wrote.
attn_of() { python3 -c "
import json,os
print(json.load(open('$HOME/.reminal/active-$AID.json')).get('attn') or '-')
"; }
hook_of() { python3 -c "
import json,os
p='$HOME/.reminal/hook-$AID.state'
print(json.load(open(p)).get('state') if os.path.exists(p) else '(none)')
"; }

echo "at an idle prompt:   attn=$(attn_of) hook=$(hook_of)"

mcp_call send_keys "{\"session\":\"$AID\",\"keys\":\"say something\",\"enter\":true}" >/dev/null
sleep 10   # well past the ~1.5s a still screen needs to read as settled
A=$(attn_of); H=$(hook_of)
echo "10s into a frozen turn: attn=$A hook=$H"
[ "$H" = "working" ] || { echo "FAIL: the extension did not report working during a turn"; kill $MODEL_PID 2>/dev/null; exit 1; }
[ "$A" = "working" ] || { echo "FAIL: attn is '$A' — the hook said working and was not believed"; kill $MODEL_PID 2>/dev/null; exit 1; }
echo "the hook held 'working' through a screen that never moved"

sleep 22
A=$(attn_of); H=$(hook_of)
echo "after the turn:      attn=$A hook=$H"
[ "$H" = "done" ] || { echo "FAIL: the extension did not report done after the turn"; kill $MODEL_PID 2>/dev/null; exit 1; }
[ "$A" = "done" ] || { echo "FAIL: attn is '$A' after the turn, not done"; kill $MODEL_PID 2>/dev/null; exit 1; }
/tmp/reminal kill "$AID" -y >/dev/null 2>&1
kill $MODEL_PID 2>/dev/null
sleep 1

say "8. a model actually calls one of reminal's tools"
# Registration is not use. This is the last link: a model choosing a reminal
# tool, pi executing it, and the extension proxying it through to reminal.
FAKE_MODEL_TOOL=list_sessions FAKE_MODEL_STREAM_MS=1000 \
    node /src/scripts/pi-test/fake-model.mjs >/dev/null 2>&1 &
MODEL_PID=$!
sleep 1

/tmp/reminal new tooltest >/dev/null 2>&1
sleep 2
TID=$(/tmp/reminal list 2>/dev/null | sed -n 's/^  tooltest  \([A-Z0-9]*\).*/\1/p' | head -1)
[ -n "$TID" ] || { echo "FAIL: no session started"; kill $MODEL_PID 2>/dev/null; exit 1; }

mcp_call send_keys "{\"session\":\"$TID\",\"keys\":\"pi $PI_ARGS\",\"enter\":true}" >/dev/null
sleep 14
mcp_call send_keys "{\"session\":\"$TID\",\"keys\":\"which reminals am I running\",\"enter\":true}" >/dev/null
sleep 12
mcp_call read_transcript "{\"session\":\"$TID\"}" > /tmp/tooltranscript.txt
/tmp/reminal kill "$TID" -y >/dev/null 2>&1
kill $MODEL_PID 2>/dev/null

grep -q "list_sessions" /tmp/tooltranscript.txt || {
    echo "FAIL: the model never called list_sessions"; exit 1; }
# The answer has to have come back through the proxy, not just been asked for.
grep -q "machines" /tmp/tooltranscript.txt || {
    echo "FAIL: list_sessions was called but returned nothing through the proxy"; exit 1; }
echo "the model called list_sessions and got reminal's answer back"

printf '\n\033[32mall good\033[0m — reminal'"'"'s tools are live in pi, pi kept its own,\na pi session reads correctly from another machine, and its state on your\nlist comes from pi itself rather than from guessing at the screen.\n'
