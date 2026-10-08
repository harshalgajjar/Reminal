#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# What `reminal restore` promises, against the rig run.sh starts: after a
# reboot the daemon brings a session back as itself — same id and PIN, its
# scrollback, a shell where it was — with its agent resumed on the SAME
# conversation; and a session ended on purpose stays ended.
BOX=reminal-restore-box RELAY=reminal-restore-relay
fails=0
ok()   { echo "ok   $*"; }
fail() { echo "FAIL $*"; fails=$((fails+1)); }
box()  { docker exec $BOX sh -c "$1"; }
send() { box "mcp.sh send $1 '$2' >/dev/null"; }
screen() { box "mcp.sh read $1"; }
newsession() { box "mkdir -p $2 && cd $2 && reminal new $1 2>&1"; }
idof()  { echo "$1" | sed -n 's/.*background session · v[^ ]* · \([A-Z0-9]*\).*/\1/p'; }
pinof() { echo "$1" | sed -n 's/.*PIN: *\([0-9]*\).*/\1/p'; }

out=$(newsession work /root/project); ID=$(idof "$out"); PIN=$(pinof "$out")
[ -n "$ID" ] && ok "session $ID (PIN $PIN) in /root/project" || { fail "no session: $out"; exit 1; }
send $ID "echo before-the-reboot-marker"
send $ID 'claude --model opus-test an-opening-prompt'
sleep 2
send $ID "remember the number 4242"
sleep 2
CONV=$(box "ls -t /root/.fake-claude/*.conv | head -1 | xargs basename | sed 's/.conv$//'")
screen $ID | grep -q "you said: remember the number 4242" && ok "claude is running, conversation $CONV" || fail "claude not answering"

# Two more: one ended by typing exit, one by reminal kill — neither may come back.
o2=$(newsession quitter /root); Q=$(idof "$o2"); send $Q "exit"
o3=$(newsession killed /root); K=$(idof "$o3"); box "reminal kill $K -y >/dev/null 2>&1"
sleep 17   # past one save (every 15s)
box "test -f /root/.reminal/restore/$ID.sealed" && ok "restore record kept for $ID" || fail "no restore record"
box "test -f /root/.reminal/restore/$Q.sealed" && fail "exited session $Q kept a record" || ok "a shell that exited on its own leaves no record"
box "test -f /root/.reminal/restore/$K.sealed" && fail "killed session $K kept a record" || ok "reminal kill leaves no record"

docker restart $BOX >/dev/null
ok "box rebooted — every process gone, no chance to clean up"
# The daemon restores at boot; the agent is typed in once the new shell is
# quiet, after its --help has been read for the flags to keep.
for i in $(seq 1 30); do box "grep -q -- '--resume' /root/.fake-claude/argv.log" 2>/dev/null && break; sleep 1; done
sleep 2

box "reminal list" | grep -q "$ID" && ok "the daemon brought $ID back at boot, same id" || fail "$ID not back: $(box 'cat /root/daemon.log; reminal restore')"
box "reminal list" | grep -qE "$Q|$K" && fail "a session ended on purpose came back" || ok "the sessions ended on purpose stayed ended"
s=$(screen $ID)
echo "$s" | grep -q "before-the-reboot-marker" && ok "scrollback from before the reboot is there" || fail "scrollback lost"
echo "$s" | grep -q "restored this session after its machine restarted" && ok "a line marks where the restore happened" || fail "no restore banner"
echo "$s" | grep -q "fake-claude: resumed conversation $CONV" && ok "claude resumed the same conversation ($CONV)" || fail "claude not resumed: $(echo "$s" | tail -5)"
echo "$s" | grep -q "(earlier) you said: remember the number 4242" && ok "…with what was said in it" || fail "conversation content missing"
argv=$(box "tail -1 /root/.fake-claude/argv.log")
[ "$argv" = "argv: --model opus-test --resume $CONV" ] && ok "started as: claude $(echo "$argv" | cut -c7-) — flags kept, the opening prompt not sent again" || fail "resumed with: $argv"
send $ID "what number"
sleep 1
screen $ID | grep -q "you said: what number" && ok "the resumed claude takes new input" || fail "resumed claude not answering"
box "grep -q 'you said: what number' /root/.fake-claude/$CONV.conv" && ok "…into the same conversation" || fail "new input went elsewhere"
docker exec -e REMINAL_RELAY=ws://$RELAY:8080/ws $RELAY sh -c "mcp.sh read 'http://$RELAY:8080/?s=$ID#p=$PIN'" | grep -q "what number" \
    && ok "reachable through the relay with its old id and PIN" || fail "not reachable through the relay with the old PIN"
send $ID "$(printf '\\u0004')"   # ctrl-d: leave claude
sleep 1
send $ID "pwd"
sleep 1
screen $ID | grep -q "pwd.\{0,4\}/root/project" && ok "the new shell is where the old one was" || fail "shell not in /root/project"

[ $fails -eq 0 ] && echo "all passed" || { echo "$fails failed"; exit 1; }
