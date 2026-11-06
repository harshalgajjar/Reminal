#!/usr/bin/env bash
# Investigation probe (not a test): when does `reminal-capture serve` get the
# ReplayKit "RPDaemonProxy: connection INTERRUPTED" loop? Pairs of capture
# clients on one display, varying signing identifier and TCC responsible
# process. Prints frames and INTERRUPTED counts per process.
#   scripts/replayd-probe/run.sh <reminal-capture>
set -uo pipefail
SRC=$1
HERE=$(cd "$(dirname "$0")" && pwd)
W=$(mktemp -d)
cc -o "$W/disclaim" "$HERE/disclaim.c" || exit 1
swiftc -O -o "$W/sclist" "$HERE/sclist.swift" || exit 1
printf 'import CoreGraphics\nprint(CGMainDisplayID())\n' > "$W/d.swift"
swiftc -o "$W/mainid" "$W/d.swift" || exit 1
D=$("$W/mainid")
echo "main display $D; macOS $(sw_vers -productVersion)"
mkdir -p "$W/a" "$W/b" "$W/c"
cp "$SRC" "$W/a/capA"; cp "$SRC" "$W/b/capB"; cp "$SRC" "$W/c/capC"
codesign -f -s - --identifier sh.reminal.capture "$W/a/capA"
codesign -f -s - --identifier sh.reminal.capture "$W/b/capB"
codesign -f -s - --identifier other.probe "$W/c/capC"
for f in a/capA b/capB c/capC; do codesign -dv "$W/$f" 2>&1 | grep -E '^Identifier|CDHash='; done
echo "same identifier, different cdhash? $(cmp -s "$W/a/capA" "$W/b/capB" && echo identical-bytes || echo bytes-differ)"

resp() { sudo launchctl procinfo "$1" 2>/dev/null | grep -iE 'responsible' | head -2 | tr '\n' ' '; }
intr() { grep -c "\[$1:" "$W/log" 2>/dev/null || echo 0; }

# scenario <name> <A argv...> -- <B argv...|none|sclist argv...>
scenario() {
  local name=$1; shift
  local a=() b=()
  while [ "$1" != -- ]; do a+=("$1"); shift; done; shift
  b=("$@")
  log stream --style compact --predicate 'eventMessage CONTAINS "RPDaemonProxy: connection INTERRUPTED"' > "$W/log" 2>/dev/null &
  local lp=$!
  log stream --style compact --predicate 'process == "replayd"' > "$W/rlog" 2>/dev/null &
  local rp=$!
  sleep 2
  { echo "start 1 display:$D 640 45 5 jpeg"; sleep 14; } | "${a[@]}" serve > "$W/A.out" 2> "$W/A.err" &
  local ap=$!
  sleep 4
  cp "$W/A.out" "$W/A.t4"
  local bp=none
  if [ "${b[0]}" = sclist ]; then
    for _ in 1 2 3; do "${b[@]:1}" > "$W/B.txt" 2>&1 & bp=$!; sleep 1; done
  elif [ "${b[0]}" != none ]; then
    { echo "start 1 display:$D 640 45 5 jpeg"; sleep 6; } | "${b[@]}" serve > "$W/B.out" 2> "$W/B.err" &
    bp=$!
  fi
  sleep 2
  echo "  A pid $ap resp: $(resp $ap)"
  [ "$bp" != none ] && echo "  B pid $bp resp: $(resp $bp)"
  wait $ap 2>/dev/null
  wait 2>/dev/null
  sleep 1
  kill $lp $rp 2>/dev/null; wait $lp $rp 2>/dev/null
  echo "== $name"
  echo "  A first 4s: $(perl "$HERE/../capture-stall-test/frames.pl" < "$W/A.t4")"
  echo "  A whole 14s: $(perl "$HERE/../capture-stall-test/frames.pl" < "$W/A.out")  INTERRUPTED=$(intr $ap)"
  if [ "${b[0]}" = sclist ]; then echo "  B: $(cat "$W/B.txt")"
  elif [ "$bp" != none ]; then echo "  B 6s: $(perl "$HERE/../capture-stall-test/frames.pl" < "$W/B.out")  INTERRUPTED=$(intr $bp)"; fi
  echo "  INTERRUPTED by process: $(awk '{for(i=1;i<=NF;i++) if($i ~ /\[[0-9]+:/){split($i,x,":"); print x[1]; break}}' "$W/log" | sort | uniq -c | tr '\n' ' ')"
  sed 's/^/  A.err: /' "$W/A.err" | head -3
  [ -f "$W/B.err" ] && sed 's/^/  B.err: /' "$W/B.err" | head -3
  echo "  replayd lines: $(wc -l < "$W/rlog")"
  sed -E 's/^[0-9-]+ [0-9:.]+ //; s/0x[0-9a-f]+/0x/g; s/[0-9]{3,}/N/g' "$W/rlog" | sort | uniq -c | sort -rn | head -12 | sed 's/^/    /'
  rm -f "$W/A.out" "$W/A.t4" "$W/A.err" "$W/B.out" "$W/B.err" "$W/B.txt"
  sleep 2
}

scenario S1-solo "$W/a/capA" -- none
scenario S2-same-id-same-resp "$W/a/capA" -- "$W/b/capB"
scenario S8-same-binary-twice "$W/a/capA" -- "$W/a/capA"
scenario S3-other-id-same-resp "$W/a/capA" -- "$W/c/capC"
scenario S4-other-id-own-resp "$W/a/capA" -- "$W/disclaim" "$W/c/capC"
scenario S5-same-id-own-resp "$W/a/capA" -- "$W/disclaim" "$W/b/capB"
scenario S7-A-own-resp-B-same-id "$W/disclaim" "$W/a/capA" -- "$W/b/capB"
scenario S6-brief-lister-same-resp "$W/a/capA" -- sclist "$W/sclist"
scenario S9-brief-lister-own-resp "$W/a/capA" -- sclist "$W/disclaim" "$W/sclist"
scenario S1b-solo-again "$W/a/capA" -- none
