#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# Against the box run.sh starts: each way of pressing Return with send_keys
# runs the line at a zsh prompt. The command prints a number its own text
# does not hold (echo A$((40+N)) prints A4N), so the number on the screen
# means it ran, not that it was typed.
fails=0
ok()   { printf "ok   %s\n" "$*"; }
fail() { printf "FAIL %s\n" "$*"; fails=$((fails+1)); }
box()  { docker exec "$BOX" sh -c "$1"; }
# mcp <tool> <arguments as JSON>: one MCP call, its answer.
mcp() {
    box "( printf '%s\n%s\n%s\n' '{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2024-11-05\",\"capabilities\":{},\"clientInfo\":{\"name\":\"t\",\"version\":\"1\"}}}' '{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}' '{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"$1\",\"arguments\":$2}}'; sleep 3 ) | reminal mcp 2>/dev/null | tail -1"
}
for i in $(seq 1 20); do box "reminal list >/dev/null 2>&1" && break; sleep 1; done
out=$(box "cd /root && reminal new zt 2>&1")
ID=$(echo "$out" | sed -n 's/^ *Session: *\([A-Z0-9]*\).*/\1/p' | head -1)
[ -n "$ID" ] && ok "zsh session $ID" || { fail "no session: $out"; box 'cat /root/daemon.log'; exit 1; }
sleep 2
mcp read_transcript "{\"session\":\"$ID\"}" | grep -q '# ' && ok "zsh is at its prompt" || fail "no zsh prompt: $(mcp read_transcript "{\"session\":\"$ID\"}")"

# n, then the send_keys arguments that should run echo A$((40+n)).
try() {
    n=$1 args=$2 what=$3
    mcp send_keys "{\"session\":\"$ID\",$args}" >/dev/null
    sleep 1
    if mcp read_transcript "{\"session\":\"$ID\"}" | grep -q "A4$n"; then ok "$what ran the line"; else fail "$what left the line unsubmitted"; fi
}
try 1 '"keys":"echo A$((40+1))","enter":true' 'text with enter=true'
try 2 '"keys":"echo A$((40+2))\r"' 'one call ending in a CR'
try 3 '"keys":"echo A$((40+3))\n"' 'one call ending in a newline'
try 4 '"keys":"echo A$((40+4))\n","enter":true' 'one call ending in a newline, with enter=true'
mcp send_keys "{\"session\":\"$ID\",\"keys\":\"echo A\$((40+5))\"}" >/dev/null
try 5 '"keys":"","enter":true' 'the text, then a bare Return'

[ $fails -eq 0 ] && echo "all passed" || { echo "$fails failed"; mcp read_transcript "{\"session\":\"$ID\"}" | tail -c 1500; exit 1; }
