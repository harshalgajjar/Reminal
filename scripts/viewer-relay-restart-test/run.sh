#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# A page attached to a session whose relay restarts, and is back before the
# session's agent: the relay knows no session yet and refuses it ("session
# not found or not ready"). The real viewer, served by a real relay, against
# a real session, in a container; the browser in a Playwright one.
#   1. the page waits for the agent and is live again once it is back, and
#      what is typed then echoes
#   2. a page opened on a session no relay has heard of still says so at once
#   3. a wrong PIN for the live session still ends at once
#
#   scripts/viewer-relay-restart-test/run.sh [TREE]   # TREE: a checkout to test (default: this one)
# Needs docker. Nothing here touches the host's reminal.
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd ../.. && pwd)
TREE=${1:-$ROOT}
NAME=${NAME:-reminal-viewer-relay-restart-test-$$}
PW=mcr.microsoft.com/playwright:v1.56.0-noble
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/;s/arm64/arm64/')
docker run --rm -v "$TREE":/src:ro -v "$PWD":/out -e GOFLAGS=-buildvcs=false golang:1.26-bookworm \
  sh -c "rm -f /out/reminal; cp -r /src /w && cd /w && GOOS=linux GOARCH=$ARCH go build -o /out/reminal ./cmd/reminal"
docker build -q -t "$NAME" . >/dev/null
docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --init --name "$NAME" "$NAME" >/dev/null
IO=$(mktemp -d)
trap 'docker rm -f "$NAME" >/dev/null 2>&1 || true; docker rmi -f "$NAME" >/dev/null 2>&1 || true; rm -f "$PWD/reminal" "$IO/in" "$IO/out"; rmdir "$IO"' EXIT
relay() {
  docker exec -d "$NAME" sh -c 'reminal relay 18080 >/tmp/relay.log 2>&1'
  # Its listen line, not a guessed sleep (timeout: an abort cap only).
  docker exec "$NAME" timeout 30 sh -c 'tail -n +1 -F /tmp/relay.log 2>/dev/null | { grep -q -m1 "reminal relay listening"; pkill -P $$ tail; }'
}
relay
docker exec "$NAME" sh -c 'cd /root && SHELL=/bin/bash reminal new alpha >/dev/null 2>&1'
creds=$(docker exec "$NAME" sh -c 'reminal info alpha | grep -E "Session:|PIN:" | awk "{print \$2}"' | tr '\n' ' ')
echo "session: $creds"
# The page's script asks for the relay to restart with the agent held
# (RELAY), for the agent to go on (AGENT), and waits for a line on its
# stdin: the browser cannot reach docker.
mkfifo "$IO/in" "$IO/out"
docker run --rm -i --network "container:$NAME" -v "$PWD:/t" -w /tmp "$PW" sh -c \
  'npm i -s playwright@1.56.0 >/dev/null 2>&1 && cp /t/checks.mjs . && node checks.mjs "$@"' _ $creds <"$IO/in" >"$IO/out" 2>&1 &
pw=$!
exec 3>"$IO/in"
agentpid() { docker exec "$NAME" sh -c 'grep -o "\"pid\": [0-9]*" /root/.reminal/active-*.json | head -1 | grep -o "[0-9]*$"'; }
while IFS= read -r line; do
  if [ "$line" = RELAY ]; then
    p=$(agentpid)
    docker exec "$NAME" kill -STOP "$p"
    docker exec "$NAME" sh -c 'pkill -x -f "reminal relay 18080"; while pgrep -x -f "reminal relay 18080" >/dev/null; do sleep 0.1; done'
    relay
    echo go >&3
  elif [ "$line" = AGENT ]; then
    docker exec "$NAME" kill -CONT "$(agentpid)"
    echo go >&3
  else
    echo "$line"
  fi
done <"$IO/out"
exec 3>&-
# The checks' own verdict is the rig's (a bare wait is always 0).
wait "$pw"
