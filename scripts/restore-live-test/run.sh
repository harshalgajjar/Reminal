#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# A session that is running is never restored again; one that is gone always
# can be. Real binary, its own HOME, no relay:
#   docker run --rm -v "$PWD/scripts/restore-live-test":/t:ro -v <dir with reminal>:/b:ro golang:1.26-bookworm bash /t/run.sh
# (procps is installed if missing.)
set -u
command -v pgrep >/dev/null || { apt-get -qq update >/dev/null 2>&1; apt-get -qq install -y procps >/dev/null 2>&1; }
cp /b/reminal /usr/local/bin/; chmod +x /usr/local/bin/reminal
export HOME=$(mktemp -d) REMINAL_OWNERS_DIR=/tmp/etc REMINAL_RELAY=ws://127.0.0.1:1 REMINAL_WEB=http://127.0.0.1:1 SHELL=/bin/sh REMINAL_NO_RESTORE=
cd $HOME
fails=0
pass() { echo "PASS  $1"; }
fail() { echo "FAIL  $1"; fails=$((fails+1)); }
newid() { reminal new "$1" 2>&1 | grep -o "[A-Z0-9]\{8\}" | head -1; }
agents_on() { local n=0; for p in $(pgrep -f -- "--headless"); do tr '\0' '\n' < /proc/$p/environ 2>/dev/null | grep -q "^REMINAL_RESTORE=$1\$" && n=$((n+1)); done; echo $n; }
agent_of() { for p in $(pgrep -f -- "--headless"); do grep -q "\"pid\": $p" $HOME/.reminal/active-$1.json 2>/dev/null && echo $p; done | head -1; }

# A session restored after a crash; `reminal new` typed in its shell.
id1=$(newid one); sleep 2
kill -9 $(agent_of $id1); sleep 1
reminal restore $id1 >/dev/null 2>&1; sleep 2
shell=$(pgrep -P $(agent_of $id1) | head -1)
tr '\0' '\n' < /proc/$shell/environ | grep -q REMINAL_RESTORE && fail "the restored session's shell inherits REMINAL_RESTORE" || pass "the restored session's shell has no REMINAL_RESTORE"
got=$(env -i $(tr '\0' '\n' < /proc/$shell/environ | grep -v '^$' | tr '\n' ' ') reminal new two 2>&1 | grep -o '[A-Z0-9]\{8\}' | head -1); sleep 2
[ -n "$got" ] && [ "$got" != "$id1" ] && [ "$(agents_on $id1)" = 1 ] && pass "reminal new in it makes a new session ($got)" || fail "reminal new in it gave $got; agents restoring $id1: $(agents_on $id1)"

# A shell an older agent started still carries the variable.
got=$(REMINAL_RESTORE=$id1 reminal new three 2>&1 | grep -o '[A-Z0-9]\{8\}' | head -1); sleep 2
[ -n "$got" ] && [ "$got" != "$id1" ] && [ "$(agents_on $id1)" = 1 ] && pass "with a stale REMINAL_RESTORE, reminal new still makes a new session" || fail "stale REMINAL_RESTORE: got $got"

# Restoring a live session straight away is refused.
out=$(REMINAL_RESTORE=$id1 timeout 10 reminal --headless 2>&1 | head -1)
echo "$out" | grep -q "already running" && pass "a restore of a running session is refused" || fail "a restore of a running session: $out"
reminal restore 2>&1 | grep -q "Nothing to restore" && pass "with every session running, nothing is restorable" || fail "restorable while running: $(reminal restore 2>&1 | head -3)"

# A power cut: an agent gone with its record cut short mid-write.
id4=$(newid four); sleep 16   # its restore record saved
kill -9 $(agent_of $id4); sleep 1
printf '{"id":"%s","pid":12' $id4 > $HOME/.reminal/active-$id4.json
touch -d '2000-01-01' $HOME/.reminal/active-$id4.json
reminal restore 2>&1 | grep -q $id4 && pass "a record cut short before this boot: its session is restorable" || fail "a record cut short before this boot kept $id4 from coming back"
# The same record written since boot may be a live agent's, mid-write.
touch $HOME/.reminal/active-$id4.json
reminal restore 2>&1 | grep -q $id4 && fail "an unreadable record from this boot was taken as gone" || pass "an unreadable record from this boot is not taken as gone"

echo; [ $fails = 0 ] && echo "all passed" || { echo "$fails failed"; exit 1; }
