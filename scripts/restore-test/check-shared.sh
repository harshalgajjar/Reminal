#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# Several claudes in one folder across a reboot (run.sh shared): each comes
# back on its own conversation.
#   A, B  never reported an id through a hook (nothing sent since it started,
#         or no hooks installed): resumed from claude's own record of the
#         process, not by opening the list
#   C     claude quit a moment before the machine went down: still resumed
#   D, E  E was started with --continue and picked up D's conversation: it is
#         resumed once, in D, and E opens the list with a line saying why
BOX=reminal-restore-box
fails=0
ok()   { echo "ok   $*"; }
fail() { echo "FAIL $*"; fails=$((fails+1)); }
box()  { docker exec $BOX sh -c "$1"; }
send() { box "mcp.sh send $1 '$2' >/dev/null"; }
screen() { box "mcp.sh read $1"; }
newsession() { box "mkdir -p $2 && cd $2 && reminal new $1 2>&1"; }
idof()  { echo "$1" | sed -n 's/.*background session · v[^ ]* · \([A-Z0-9]*\).*/\1/p'; }
convof() { screen $1 | sed -nE 's/.*fake-claude: (new|resumed) conversation (conv-[0-9a-f]+).*/\2/p' | tail -1; }
W=/root/shared

A=$(idof "$(newsession a $W)"); B=$(idof "$(newsession b $W)"); C=$(idof "$(newsession c $W)")
D=$(idof "$(newsession d $W)"); E=$(idof "$(newsession e $W)")
[ -n "$A$B$C$D$E" ] && ok "five sessions in $W: A=$A B=$B C=$C D=$D E=$E" || { fail "sessions"; exit 1; }
send $A 'FAKE_CLAUDE_HOOKS=0 claude'; send $B 'FAKE_CLAUDE_HOOKS=0 claude'; send $C 'claude'; send $D 'claude'
sleep 2
send $A 'alpha'; send $B 'bravo'; send $C 'charlie'; send $D 'delta'
sleep 1
CA=$(convof $A) CB=$(convof $B) CC=$(convof $C) CD=$(convof $D)
[ -n "$CA" ] && [ -n "$CB" ] && [ -n "$CC" ] && [ -n "$CD" ] && ok "conversations: A=$CA B=$CB C=$CC D=$CD" || { fail "a claude did not start: A=$CA B=$CB C=$CC D=$CD"; exit 1; }
sleep 17   # D is saved with its conversation before E picks it up
send $D 'delta again'    # D's is now the latest here
sleep 1
send $E 'claude --continue'
sleep 2
CE=$(convof $E)
[ "$CE" = "$CD" ] && ok "E continued the latest here, which is D's ($CE)" || fail "E is on $CE, not D's $CD"
send $C "$(printf '\\u0004')"   # C quits claude...
sleep 17                         # ...and a save sees only its prompt
box 'echo; for f in /root/.reminal/restore/*.conv; do echo "$f: $(cat $f)"; done'

docker restart $BOX >/dev/null
ok "box rebooted"
sleep 12

# Only what came after the restore, in the scrollback: a session's output from
# before the reboot is restored with it, and the reader's "screen now" part
# can still show it.
after() { screen $1 | perl -pe 's/\\r?\\n/\n/g' | awk '/restored this session after its machine restarted/{f=1;next} /--- the screen now ---/{f=0} f'; }
resumed() { [ -n "$2" ] && after $1 | grep -q "fake-claude: resumed conversation $2"; }
resumed $A "$CA" && ok "A resumed its own conversation with no hook report" || fail "A: $(after $A | tail -4)"
resumed $B "$CB" && ok "B resumed its own conversation with no hook report" || fail "B: $(after $B | tail -4)"
resumed $C "$CC" && ok "C, quit just before the shutdown, was resumed" || fail "C: $(after $C | tail -4)"
resumed $D "$CD" && ok "D resumed its conversation" || fail "D: $(after $D | tail -4)"
n=0; for s in $A $B $C $D $E; do resumed $s "$CD" && n=$((n+1)); done
[ $n -eq 1 ] && ok "D's conversation was resumed in exactly one session" || fail "D's conversation resumed in $n sessions"
after $E | grep -q "open in another session" && ok "E says its last conversation is open in another session" || fail "E: $(after $E | tail -4)"
after $E | grep -q "fake-claude: new conversation" && ok "E opened the list (claude --resume), not D's conversation" || fail "E: $(after $E | tail -4)"

[ $fails -eq 0 ] && echo "all passed" || { echo "$fails failed"; exit 1; }
