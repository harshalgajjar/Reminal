#!/bin/bash
# A foreground session, as an editor's terminal tab runs one (reminal is the
# terminal's program; script(1) stands in for the editor holding the
# terminal), ended three ways. Each case has its own HOME.
#   tab closed    SIGHUP, its host carries on        -> forgotten
#   app quit      the host killed, the kernel hangs up -> kept, to come back
#   shutdown      SIGHUP, then SIGTERM 1s later        -> kept, to come back
export SHELL=/bin/bash REMINAL_RELAY=ws://127.0.0.1:1/ws REMINAL_WEB=http://127.0.0.1:1 REMINAL_NO_RESTORE=1
fail=0
start() {
  export HOME=$(mktemp -d)
  (sleep 600 | script -qfec reminal "$HOME/t.log" >/dev/null 2>&1) &
  sleep 6
  HOST=$(pgrep -n -f "script -qfec reminal $HOME/t.log")
  R=$(pgrep -P "$HOST" -x reminal)
  SH=$(pgrep -P "$R" -x bash | head -1)
  ID=$(ls "$HOME/.reminal" | sed -n 's/^attach-\(.*\)\.sock$/\1/p')
}
end() { # end NAME WANT(kept|forgotten)
  sleep 6
  local alive=no; kill -0 "$R" 2>/dev/null && alive=yes
  local shell=no; kill -0 "$SH" 2>/dev/null && shell=yes
  local got=forgotten; reminal restore 2>/dev/null | grep -q "$ID" && got=kept
  local ok=PASS
  [ "$alive$shell" = nono ] && [ "$got" = "$2" ] || { ok=FAIL; fail=$((fail+1)); }
  echo "$ok  $1: want $2, got $got (session $ID; reminal alive $alive, shell alive $shell)"
  pkill -9 -f "$HOME/t.log" 2>/dev/null; true
}
start; kill -HUP "$R"; end "tab closed" forgotten
start; kill -9 "$HOST"; end "app quit" kept
start; kill -HUP "$R"; sleep 1; kill -TERM "$R" 2>/dev/null; end "shutdown" kept
[ $fail = 0 ] && echo "all passed" || echo "$fail failed"
exit $fail
