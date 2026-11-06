#!/usr/bin/env bash
# Investigation probe: a `reminal-capture serve` next to real Chromes (two
# Playwright Chromes screenshotting over CDP, one idle Chrome with a debugging
# port), counting INTERRUPTED per process; and the same serve without them.
#   scripts/replayd-probe/chrome.sh <reminal-capture>   (needs playwright in node_modules)
set -uo pipefail
SRC=$1
HERE=$(cd "$(dirname "$0")" && pwd)
W=$(mktemp -d /tmp/rc.XXXX)
cc -o "$W/disclaim" "$HERE/disclaim.c" || exit 1
printf 'import CoreGraphics\nprint(CGMainDisplayID())\n' > "$W/d.swift"; swiftc -o "$W/mainid" "$W/d.swift" || exit 1
D=$("$W/mainid")
APPB="$W/reminal.app"; mkdir -p "$APPB/Contents/MacOS"
cp "$W/mainid" "$APPB/Contents/MacOS/reminal"; cp "$SRC" "$APPB/Contents/MacOS/reminal-capture"
printf '<?xml version="1.0" encoding="UTF-8"?>\n<plist version="1.0"><dict><key>CFBundleIdentifier</key><string>sh.reminal</string><key>CFBundleExecutable</key><string>reminal</string><key>CFBundlePackageType</key><string>APPL</string></dict></plist>\n' > "$APPB/Contents/Info.plist"
codesign -f -s - --identifier sh.reminal.capture "$APPB/Contents/MacOS/reminal-capture" 2>/dev/null; codesign -f -s - "$APPB" 2>/dev/null
BH="$APPB/Contents/MacOS/reminal-capture"
CHROME="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
echo "macOS $(sw_vers -productVersion); $("$CHROME" --version)"

# case <tag> <idle: none|exec|open> <shooters: 0|2> <shooter launch: plain|disclaim>
case_() {
  local tag=$1 idle=$2 shooters=$3 how=$4
  echo ">> $tag starting"
  log stream --style compact --predicate 'eventMessage CONTAINS "RPDaemonProxy: connection INTERRUPTED"' > "$W/$tag.log" 2>/dev/null &
  local lp=$!
  log stream --style compact --predicate 'process == "replayd" AND (eventMessage CONTAINS "accepted client" OR eventMessage CONTAINS "xpc_connection_cancel")' > "$W/$tag.rlog" 2>/dev/null &
  local rp=$!
  sleep 2
  case $idle in
    exec) perl -e "alarm 45; exec @ARGV" "$CHROME" --remote-debugging-port=9333 --user-data-dir="$W/idle-$tag" --no-first-run about:blank > /dev/null 2>&1 & ;;
    open) open -na "Google Chrome" --args --remote-debugging-port=9333 --user-data-dir="$W/idle-$tag" --no-first-run about:blank ;;
  esac
  local sp=()
  for i in $(seq 1 "$shooters"); do
    if [ "$how" = disclaim ]; then perl -e "alarm 45; exec @ARGV" "$W/disclaim" "$(command -v node)" "$HERE/shot.js" 30 > "$W/$tag.shot$i" 2>&1 &
    else perl -e "alarm 45; exec @ARGV" node "$HERE/shot.js" 30 > "$W/$tag.shot$i" 2>&1 & fi
    sp+=($!)
  done
  sleep 8
  { echo "start 1 display:$D 640 45 5 jpeg"; sleep 15; } | "$BH" serve > "$W/$tag.out" 2> "$W/$tag.err" &
  local cp=$!
  wait $cp 2>/dev/null
  sleep 3; pkill -f "$HERE/shot.js" 2>/dev/null; pkill -f "Google Chrome.*remote-debugging-port=9333" 2>/dev/null
  pkill -f "user-data-dir=$W/idle-$tag" 2>/dev/null
  sleep 2; kill $lp $rp 2>/dev/null; wait $lp $rp 2>/dev/null
  echo "== $tag (idle chrome: $idle, playwright chromes: $shooters $how)"
  echo "  serve (pid $cp): $(perl "$HERE/../capture-stall-test/frames.pl" < "$W/$tag.out" | cut -c1-80)  INTERRUPTED=$(grep -c "\[$cp:" "$W/$tag.log")"
  for i in $(seq 1 "$shooters"); do echo "  shooter $i:"; grep -E "SHOT|shots=" "$W/$tag.shot$i" | head -2 | sed "s/^/    /"; done
  echo "  INTERRUPTED by process: $(sed -nE 's/.* ([^ ]+\[[0-9]+):.*/\1/p' "$W/$tag.log" | sort | uniq -c | sort -rn | head -8 | tr '\n' ' ')"
  echo "  replayd: $(grep -c 'accepted client' "$W/$tag.rlog") accepts, $(grep -c 'xpc_connection_cancel' "$W/$tag.rlog") cancels"
  head -2 "$W/$tag.err" | sed 's/^/  serve err: /'
  sleep 3
}
# pair <tag> <bin1> <bin2>: two granted serves side by side (shell-launched).
pair() {
  echo ">> $1 starting"
  log stream --style compact --predicate 'eventMessage CONTAINS "RPDaemonProxy: connection INTERRUPTED"' > "$W/$1.log" 2>/dev/null &
  local lp=$!; sleep 2
  { echo "start 1 display:$D 640 45 5 jpeg"; sleep 12; } | "$2" serve > "$W/$1.a" 2> "$W/$1.ae" & local a=$!
  sleep 3
  { echo "start 1 display:$D 640 45 5 jpeg"; sleep 8; } | "$3" serve > "$W/$1.b" 2> "$W/$1.be" & local b=$!
  wait $a $b 2>/dev/null; sleep 1; kill $lp; wait $lp 2>/dev/null
  echo "== $1"
  echo "  first  $(perl "$HERE/../capture-stall-test/frames.pl" < "$W/$1.a" | cut -c1-70) INTERRUPTED=$(grep -c "\[$a:" "$W/$1.log")"
  echo "  second $(perl "$HERE/../capture-stall-test/frames.pl" < "$W/$1.b" | cut -c1-70) INTERRUPTED=$(grep -c "\[$b:" "$W/$1.log")"
}
mkbundle() { # mkbundle <dir> <bundle id>
  mkdir -p "$1/Contents/MacOS"; cp "$W/mainid" "$1/Contents/MacOS/main"; cp "$SRC" "$1/Contents/MacOS/helper"
  printf '<?xml version="1.0" encoding="UTF-8"?>\n<plist version="1.0"><dict><key>CFBundleIdentifier</key><string>%s</string><key>CFBundleExecutable</key><string>main</string><key>CFBundlePackageType</key><string>APPL</string></dict></plist>\n' "$2" > "$1/Contents/Info.plist"
  codesign -f -s - --identifier "$2.helper" "$1/Contents/MacOS/helper" 2>/dev/null; codesign -f -s - "$1" 2>/dev/null
}
mkbundle "$W/One.app" test.one; mkbundle "$W/Two.app" test.two
cp "$SRC" "$W/plainA"; cp "$SRC" "$W/plainB"
# Run one case: chrome.sh <helper> <case args...>, killed after 100s.
shift
( case_ "$@" ) &
CP=$!
( sleep 100; echo "!! $1 hit the 100s limit"; pkill -P $CP; kill -9 $CP; pkill -f "$HERE/shot.js"; pkill -f "remote-debugging-port=9333"; pkill -x log ) &
WD=$!
wait $CP
kill $WD 2>/dev/null
exit 0
