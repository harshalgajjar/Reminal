#!/usr/bin/env bash
# Investigation probe: does replayd treat capture clients that share a
# RESPONSIBLE APP (children of an app launched by LaunchServices) as one
# client, cancelling each other's connection in a loop?
#   scripts/replayd-probe/resp.sh <reminal-capture>
set -uo pipefail
SRC=$1
HERE=$(cd "$(dirname "$0")" && pwd)
W=$(mktemp -d /tmp/rp.XXXX)
printf 'import CoreGraphics\nprint(CGMainDisplayID())\n' > "$W/d.swift"
swiftc -o "$W/mainid" "$W/d.swift" || exit 1
D=$("$W/mainid")
echo "macOS $(sw_vers -productVersion), display $D, SIP: $(csrutil status)"
cp "$SRC" "$W/capA"; cp "$SRC" "$W/capC"
codesign -f -s - --identifier sh.reminal.capture "$W/capA" 2>/dev/null
codesign -f -s - --identifier other.probe "$W/capC" 2>/dev/null

# kids <tag> <bin1> <bin2>: two serve clients side by side, 10s each, the
# second 3s after the first. Writes $W/<tag>.{1,2}.{out,err,pid}.
cat > "$W/kids.sh" <<KIDS
#!/bin/bash
W=$W; D=$D
tag=\$1; b1=\$2; b2=\$3
{ echo "start 1 display:\$D 640 45 5 jpeg"; sleep 10; } | "\$b1" serve > "\$W/\$tag.1.out" 2> "\$W/\$tag.1.err" & echo \$! > "\$W/\$tag.1.pid"
sleep 3
{ echo "start 1 display:\$D 640 45 5 jpeg"; sleep 10; } | "\$b2" serve > "\$W/\$tag.2.out" 2> "\$W/\$tag.2.err" & echo \$! > "\$W/\$tag.2.pid"
wait
touch "\$W/\$tag.done"
KIDS
chmod +x "$W/kids.sh"

# An app whose main executable runs kids.sh with the args in $W/args.
APP="$W/RespApp.app"
mkdir -p "$APP/Contents/MacOS"
cat > "$APP/Contents/MacOS/RespApp" <<APPX
#!/bin/bash
exec "$W/kids.sh" \$(cat "$W/args")
APPX
chmod +x "$APP/Contents/MacOS/RespApp"
cat > "$APP/Contents/Info.plist" <<PL
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>test.respapp</string>
<key>CFBundleExecutable</key><string>RespApp</string>
<key>CFBundlePackageType</key><string>APPL</string>
</dict></plist>
PL
codesign -f -s - "$APP" 2>/dev/null

grant() {
  for db in "/Library/Application Support/com.apple.TCC/TCC.db" "$HOME/Library/Application Support/com.apple.TCC/TCC.db"; do
    sudo sqlite3 "$db" "INSERT OR REPLACE INTO access (service,client,client_type,auth_value,auth_reason,auth_version,flags) VALUES ('kTCCServiceScreenCapture','test.respapp',0,2,4,1,0);" 2>&1 | head -2 && echo "granted in $db"
  done
}

run() { # run <tag> <via: app|shell> <bin1> <bin2>
  local tag=$1 via=$2 b1=$3 b2=$4
  log stream --style compact --predicate 'eventMessage CONTAINS "RPDaemonProxy: connection INTERRUPTED"' > "$W/$tag.log" 2>/dev/null &
  local lp=$!
  log stream --style compact --info --predicate 'process == "replayd" AND (eventMessage CONTAINS "uniqueClientIdentifier" OR eventMessage CONTAINS "accepted client" OR eventMessage CONTAINS "xpc_connection_cancel")' > "$W/$tag.rlog" 2>/dev/null &
  local rp=$!
  sleep 2
  if [ "$via" = app ]; then echo "$tag $b1 $b2" > "$W/args"; open -n "$APP"; elif [ "$via" = shellr ]; then "$W/disclaim" /bin/bash "$W/kids.sh" "$tag" "$b1" "$b2" & else "$W/kids.sh" "$tag" "$b1" "$b2" & fi
  sleep 6
  for i in 1 2; do p=$(cat "$W/$tag.$i.pid" 2>/dev/null); echo "  $tag.$i pid $p resp: $(sudo launchctl procinfo "$p" 2>/dev/null | grep -i 'responsible pid' | head -1)"; done
  for _ in $(seq 30); do [ -f "$W/$tag.done" ] && break; sleep 1; done
  sleep 1; kill $lp $rp 2>/dev/null; wait $lp $rp 2>/dev/null
  echo "== $tag (via $via: $(basename "$b1") + $(basename "$b2"))"
  for i in 1 2; do p=$(cat "$W/$tag.$i.pid" 2>/dev/null)
    echo "  client $i: $(perl "$HERE/../capture-stall-test/frames.pl" < "$W/$tag.$i.out")  INTERRUPTED=$(grep -c "\[$p:" "$W/$tag.log")"
    head -2 "$W/$tag.$i.err" | sed 's/^/    err: /'; done
  echo "  replayd: $(grep -c 'accepted client' "$W/$tag.rlog") accepts, $(grep -c 'xpc_connection_cancel' "$W/$tag.rlog") cancels"
  grep -o 'uniqueClientIdentifier[^]]*\].*' "$W/$tag.rlog" | sed -E 's/[0-9]{3,}/N/g' | sort | uniq -c | head -6 | sed 's/^/    /'
  sleep 2
}

cc -o "$W/disclaim" "$HERE/disclaim.c" || exit 1
cp "$SRC" "$W/capB"; codesign -f -s - --identifier sh.reminal.capture "$W/capB" 2>/dev/null
for n in A B C; do printf '#!/bin/bash\nexec "%s" "%s" "$@"\n' "$W/disclaim" "$W/cap$n" > "$W/d$n"; chmod +x "$W/d$n"; done
grantpath() {
  sudo sqlite3 "/Library/Application Support/com.apple.TCC/TCC.db" "INSERT OR REPLACE INTO access (service,client,client_type,auth_value,auth_reason,auth_version,flags) VALUES ('kTCCServiceScreenCapture','$1',1,2,4,1,0);" && echo "granted $1"
}
# shellr <tag> <bin1> <bin2>: kids.sh run as its own responsible process, so
# both clients share that one responsible process.
# multi <tag> <secs> <offset>:<bin> ...: clients started <offset>s apart in
# one log window, each streaming <secs>s. The shell-started ones are granted
# (responsible = the runner); the d* ones run as their own responsible
# process and are refused.
APPB="$W/reminal.app"; mkdir -p "$APPB/Contents/MacOS"
cp "$W/mainid" "$APPB/Contents/MacOS/reminal"; cp "$SRC" "$APPB/Contents/MacOS/reminal-capture"
printf '<?xml version="1.0" encoding="UTF-8"?>\n<plist version="1.0"><dict><key>CFBundleIdentifier</key><string>sh.reminal</string><key>CFBundleExecutable</key><string>reminal</string><key>CFBundlePackageType</key><string>APPL</string></dict></plist>\n' > "$APPB/Contents/Info.plist"
codesign -f -s - --identifier sh.reminal.capture "$APPB/Contents/MacOS/reminal-capture" 2>/dev/null; codesign -f -s - "$APPB" 2>/dev/null
BH="$APPB/Contents/MacOS/reminal-capture"
multi() {
  local tag=$1 secs=$2; shift 2
  log stream --style compact --predicate 'eventMessage CONTAINS "RPDaemonProxy: connection INTERRUPTED"' > "$W/$tag.log" 2>/dev/null &
  local lp=$! pids=() names=() i=0
  sleep 2
  for spec in "$@"; do
    sleep "${spec%%:*}"
    local bin=${spec#*:}
    { echo "start 1 display:$D 640 45 5 jpeg"; sleep "$secs"; } | "$bin" serve > "$W/$tag.$i.out" 2> "$W/$tag.$i.err" &
    pids+=($!); names+=("$(basename "$bin")"); i=$((i+1))
  done
  for p in "${pids[@]}"; do wait "$p" 2>/dev/null; done
  sleep 1; kill $lp 2>/dev/null; wait $lp 2>/dev/null
  echo "== $tag"
  for i in "${!pids[@]}"; do
    echo "  ${names[$i]} (pid ${pids[$i]}): $(perl "$HERE/../capture-stall-test/frames.pl" < "$W/$tag.$i.out" | cut -c1-60)  INTERRUPTED=$(grep -c "\[${pids[$i]}:" "$W/$tag.log")"
  done
  sleep 2
}
multi M1-granted-alone 10 0:"$W/capC"
multi M2-ring-first-then-granted-other-path 14 0:"$W/dA" 0:"$W/dA" 4:"$W/capC"
multi M3-granted-first-then-ring 14 0:"$W/capC" 4:"$W/dA" 0:"$W/dA"
multi M4-ring-then-bundled-helper 14 0:"$W/dA" 0:"$W/dA" 4:"$BH"
multi M5-one-refused-then-granted-same-path 14 0:"$W/dA" 4:"$W/capA"
multi M6-granted-then-refused-same-path 14 0:"$W/capA" 4:"$W/dA"
multi M7-ring-then-granted-same-path 14 0:"$W/dA" 0:"$W/dA" 4:"$W/capA"
multi M8-granted-alone-after 10 0:"$W/capC"
