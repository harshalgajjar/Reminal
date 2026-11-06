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
  log stream --style compact --predicate 'eventMessage CONTAINS "RPDaemonProxy: connection INTERRUPTED"' > "$W/$tag.log" 2>/dev/null &
  local lp=$!
  log stream --style compact --predicate 'process == "replayd" AND (eventMessage CONTAINS "accepted client" OR eventMessage CONTAINS "xpc_connection_cancel")' > "$W/$tag.rlog" 2>/dev/null &
  local rp=$!
  sleep 2
  case $idle in
    exec) "$CHROME" --remote-debugging-port=9333 --user-data-dir="$W/idle-$tag" --no-first-run about:blank > /dev/null 2>&1 & ;;
    open) open -na "Google Chrome" --args --remote-debugging-port=9333 --user-data-dir="$W/idle-$tag" --no-first-run about:blank ;;
  esac
  local sp=()
  for i in $(seq 1 "$shooters"); do
    if [ "$how" = disclaim ]; then "$W/disclaim" "$(command -v node)" "$HERE/shot.js" 30 > "$W/$tag.shot$i" 2>&1 &
    else node "$HERE/shot.js" 30 > "$W/$tag.shot$i" 2>&1 & fi
    sp+=($!)
  done
  sleep 8
  { echo "start 1 display:$D 640 45 5 jpeg"; sleep 15; } | "$BH" serve > "$W/$tag.out" 2> "$W/$tag.err" &
  local cp=$!
  wait $cp 2>/dev/null
  for p in "${sp[@]}"; do wait "$p" 2>/dev/null; done
  pkill -f "user-data-dir=$W/idle-$tag" 2>/dev/null
  sleep 2; kill $lp $rp 2>/dev/null; wait $lp $rp 2>/dev/null
  echo "== $tag (idle chrome: $idle, playwright chromes: $shooters $how)"
  echo "  serve (pid $cp): $(perl "$HERE/../capture-stall-test/frames.pl" < "$W/$tag.out" | cut -c1-80)  INTERRUPTED=$(grep -c "\[$cp:" "$W/$tag.log")"
  for i in $(seq 1 "$shooters"); do echo "  shooter $i: $(tail -1 "$W/$tag.shot$i")"; done
  echo "  INTERRUPTED by process: $(sed -nE 's/.* ([^ ]+\[[0-9]+):.*/\1/p' "$W/$tag.log" | sort | uniq -c | sort -rn | head -8 | tr '\n' ' ')"
  echo "  replayd: $(grep -c 'accepted client' "$W/$tag.rlog") accepts, $(grep -c 'xpc_connection_cancel' "$W/$tag.rlog") cancels"
  head -2 "$W/$tag.err" | sed 's/^/  serve err: /'
  sleep 3
}
case_ C0-serve-alone none 0 plain
case_ C1-idle-exec exec 0 plain
case_ C2-idle-open open 0 plain
case_ C3-two-shooters none 2 plain
case_ C4-all-three exec 2 plain
case_ C5-all-three-open-disclaimed open 2 disclaim
case_ C6-serve-alone-after none 0 plain
