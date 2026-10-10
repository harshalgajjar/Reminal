# Runs inside the container. For each command that prints a QR, written to a
# file, through a pipe, and on a terminal: is it plain ASCII off a terminal,
# and does the QR decode to the link printed next to it?
set -u
cd /root
reminal relay 18080 >/tmp/relay.log 2>&1 &
sleep 2
fail=0
pty() { script -qec "$1" "$2" >/dev/null 2>&1; }   # run on a real terminal
# $1 label  $2 file  $3 want style  $4 expected link
verify() {
  got=$(python3 /t/decode.py "$2"); style=${got%% *}; text=${got#* }
  nonascii=$(LC_ALL=C grep -c '[^[:print:][:space:]]' "$2")
  ok=PASS
  [ "$style" = "$3" ] || ok=FAIL
  [ "$text" = "$4" ] || ok=FAIL
  [ "$3" = ascii ] && [ "$nonascii" != 0 ] && ok=FAIL
  [ $ok = FAIL ] && fail=1
  printf '%-34s %-9s non-ascii-lines=%-3s decodes-to-link=%-3s %s\n' "$1" "$style" "$nonascii" \
    "$([ "$text" = "$4" ] && echo yes || echo NO)" $ok
}
link() { grep -a "$1" "$2" | head -1 | tr -d '\r' | awk '{print $NF}'; }

reminal new demo > new.txt 2>/dev/null
J=$(link 'Join:' new.txt)
verify "new > file"            new.txt  ascii "$J"
reminal new piped 2>/dev/null | cat > pipe.txt
verify "new | cat"             pipe.txt ascii "$(link 'Join:' pipe.txt)"
pty "reminal new tty" tty.txt
verify "new on a terminal"     tty.txt  halfblock "$(link 'Join:' tty.txt)"
reminal info demo > info.txt
verify "info <name> > file"    info.txt ascii "$J"
pty "reminal info demo" infotty.txt
verify "info <name> on a terminal" infotty.txt halfblock "$J"
reminal qr demo > qr.txt
verify "qr <name> > file"      qr.txt   ascii "$J"
pty "reminal qr demo" qrtty.txt
verify "qr <name> on a terminal" qrtty.txt halfblock "$J"
# inside a session whose host is elsewhere: the env-driven banner
S=$(link 'Session:' new.txt); P=$(link 'PIN:' new.txt); U=$(link 'Open:' new.txt)
REMINAL_SESSION=ZZZZZZZZ REMINAL_SESSION_PIN=$P REMINAL_SESSION_URL=$U reminal info > env.txt 2>&1
verify "info from env > file"  env.txt  ascii "$U#p=$P"
# expose needs the Worker's tunnel, which the local relay lacks: it runs against
# the real relay (a short PIN-protected tunnel to a dummy local server), and
# only when EXPOSE=1.
if [ "${EXPOSE:-}" = 1 ]; then
  ( python3 -m http.server 8000 >/dev/null 2>&1 & )
  REMINAL_RELAY=wss://live.reminal.app/ws REMINAL_WEB=https://live.reminal.app reminal expose 8000 > expose.txt 2>&1
  verify "expose > file"       expose.txt ascii "$(grep -a 'Quick link:' expose.txt | awk '{print $3}')"
  REMINAL_RELAY=wss://live.reminal.app/ws REMINAL_WEB=https://live.reminal.app script -qec "reminal expose 8001" exptty.txt >/dev/null 2>&1
  verify "expose on a terminal" exptty.txt halfblock "$(grep -a 'Quick link:' exptty.txt | tr -d '\r' | awk '{print $3}')"
  REMINAL_RELAY=wss://live.reminal.app/ws REMINAL_WEB=https://live.reminal.app sh -c 'reminal stop 8000; reminal stop 8001' >/dev/null 2>&1
fi
# foreground session banner on a terminal (always a terminal)
pty "timeout 6 reminal" fg.txt
verify "foreground session banner" fg.txt halfblock "$(grep -a 'Join' fg.txt | grep -o 'http[^ ]*#p=[0-9]*' | head -1)"
echo "--- banner text when redirected:"; sed -n 2p new.txt
exit $fail
