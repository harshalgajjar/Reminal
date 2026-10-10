#!/usr/bin/env bash
# End to end: a real agent mirroring an xterm on an Xvfb that keeps its screen
# in a file, through a real relay, to the real viewer (e2e.mjs). Typing in the
# xterm must reach the viewer as changed bands, and the picture they build must
# be the window's. Needs docker; everything runs in containers built from
# scripts/x11-test. Nothing here touches the host's reminal.
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd ../.. && pwd)
NAME=reminal-bands-e2e
IMAGE=reminal-bands-e2e
PW=mcr.microsoft.com/playwright:v1.56.0-noble
BIN=$(mktemp -d)
cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; rm -rf "${BIN:?}"; }
trap cleanup EXIT

GOOS=linux GOARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/') \
  go build -C "$ROOT" -o "$BIN/reminal" ./cmd/reminal
docker build -q -t "$IMAGE" "$ROOT/scripts/x11-test" >/dev/null
docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --name "$NAME" -e XVFB_FBDIR=/run/xvfb \
  -e REMINAL_RELAY=ws://127.0.0.1:18080/ws -e REMINAL_WEB=http://127.0.0.1:18080 \
  "$IMAGE" sleep infinity >/dev/null
docker cp "$BIN/reminal" "$NAME:/usr/local/bin/reminal" >/dev/null
for _ in $(seq 100); do docker exec "$NAME" sh -c 'wmctrl -l 2>/dev/null | grep -q .' && break; sleep 0.1; done
docker exec -d "$NAME" sh -c 'reminal relay 18080 >/tmp/relay.log 2>&1'
for _ in $(seq 50); do docker exec "$NAME" bash -c 'exec 3<>/dev/tcp/127.0.0.1/18080' 2>/dev/null && break; sleep 0.2; done
docker exec "$NAME" sh -c 'cd /root && reminal new alpha >/dev/null 2>&1'
read -r S PIN < <(docker exec "$NAME" sh -c 'reminal info alpha | grep -E "Session:|PIN:" | awk "{print \$2}"' | tr '\n' ' '; echo)
echo "session $S"
docker run --rm --network "container:$NAME" -v "$PWD:/t:ro" -w /tmp "$PW" sh -c \
  'npm i -s playwright@1.56.0 >/dev/null 2>&1 && cp /t/e2e.mjs . && node e2e.mjs "$@"' _ "$S" "$PIN"
