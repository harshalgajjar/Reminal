#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# How responsive a mirrored window is, measured end to end in Docker: a
# headless X11 desktop with a probe window (probe.py), a local relay, the
# daemon and a session, and the real viewer in a Playwright container. It
# reports glass-to-glass latency (an input sent from the viewer -> the
# changed pixel drawn in the pane), input -> host and host -> paint, the
# delivered frame rate and its jitter while the window moves, the viewer's
# main-thread time, and the CPU and memory of the agent, the daemon and what
# they spawn, by phase (idle, a viewer with no pane, mirroring still,
# mirroring moving).
#
#   scripts/window-latency-test/run.sh [TREE]
#
# Knobs (environment):
#   CODEC=jpeg|h264        jpeg sets REMINAL_NO_H264 (default jpeg)
#   CAPTURE=fbdir|import   Xvfb's screen file, or ImageMagick per frame (default fbdir)
#   BOX_CPUS=2 BOX_MEM=4g  a weak machine
#   THROTTLE=1,4,6         viewer CPU throttle rates (a slow phone)
#   TRIALS=40 PHASE_SECS=15 IDLE_SECS=20 PASSIVE_TRIALS=8 RAPID_TRIALS=10
#   P2P=0                  do not wait for peer-to-peer (it may still come up and carry input)
#   NORTC=1                no WebRTC in the page: frames and input both over the relay
#   PROBE_STEP_MS=40       the probe answers an input in two draws that far apart
#   REQUIRE_P2P=1          fail if the peer-to-peer path never comes up
#   RESULT=path            also write the summary there
#   GOMOD_VOL=name         a docker volume to keep the Go module cache in
# Needs docker. Nothing here touches the host's reminal.
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd ../.. && pwd)
TREE=${1:-$ROOT}
NAME=${NAME:-reminal-window-latency-$$}
PW=mcr.microsoft.com/playwright:v1.56.0-noble
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
OUT=$(mktemp -d)
cleanup() {
  docker rm -f "$NAME-box" >/dev/null 2>&1 || true
  docker rmi -f "$NAME-img" >/dev/null 2>&1 || true
  rm -f "$PWD/reminal"
  "$ROOT/scripts/rm-tmp.sh" "$OUT" >/dev/null 2>&1 || true
}
trap cleanup EXIT
CACHE=()
[ -n "${GOMOD_VOL:-}" ] && CACHE=(-v "$GOMOD_VOL":/go/pkg/mod)
docker run --rm -v "$TREE":/src:ro -v "$PWD":/out ${CACHE[@]+"${CACHE[@]}"} -e GOFLAGS=-buildvcs=false golang:1.26-bookworm \
  sh -c "cp -r /src /w && cd /w && CGO_ENABLED=0 GOOS=linux GOARCH=$ARCH go build -o /out/reminal ./cmd/reminal"
docker build -q -t "$NAME-img" . >/dev/null
mkdir -p "$OUT/shared" && chmod 777 "$OUT/shared"
ENVS=(-e "PROBE_STEP_MS=${PROBE_STEP_MS:-0}" -e "CAPTURE=${CAPTURE:-fbdir}" -e REMINAL_RELAY=ws://127.0.0.1:8080/ws -e REMINAL_WEB=http://127.0.0.1:8080)
[ "${CODEC:-jpeg}" = jpeg ] && ENVS+=(-e REMINAL_NO_H264=1)
LIMITS=()
[ -n "${BOX_CPUS:-}" ] && LIMITS+=(--cpus "$BOX_CPUS")
[ -n "${BOX_MEM:-}" ] && LIMITS+=(--memory "$BOX_MEM" --memory-swap "$BOX_MEM")
docker run -d --init --name "$NAME-box" ${LIMITS[@]+"${LIMITS[@]}"} "${ENVS[@]}" -v "$OUT/shared":/shared "$NAME-img" >/dev/null
for _ in $(seq 100); do docker exec "$NAME-box" test -f /tmp/desktop-ready 2>/dev/null && break; sleep 0.2; done
docker exec "$NAME-box" test -f /tmp/desktop-ready || { docker logs "$NAME-box"; exit 1; }
docker exec -d "$NAME-box" python3 /usr/local/bin/procstat.py
docker exec -d "$NAME-box" sh -c 'reminal relay 8080 >/tmp/relay.log 2>&1'
docker exec "$NAME-box" timeout 30 sh -c 'until grep -q "listening" /tmp/relay.log 2>/dev/null; do sleep 0.2; done'
docker exec -d "$NAME-box" sh -c 'reminal daemon >/tmp/daemon.log 2>&1'
sleep 2
docker exec "$NAME-box" sh -c 'cd /root/h && reminal new perf >/dev/null 2>&1'
creds=$(docker exec "$NAME-box" sh -c 'reminal info perf | grep -E "Session:|PIN:" | awk "{print \$2}"' | tr '\n' ' ')
read -r SID PIN <<<"$creds"
echo "http://127.0.0.1:8080/?s=$SID#p=$PIN" > "$OUT/shared/join.txt"
echo idle > "$OUT/shared/phase"
sleep "${IDLE_SECS:-20}"
docker run --rm --network "container:$NAME-box" -v "$OUT/shared":/shared -v "$PWD":/t:ro -w /tmp \
  -e TRIALS="${TRIALS:-40}" -e PASSIVE_TRIALS="${PASSIVE_TRIALS:-8}" -e RAPID_TRIALS="${RAPID_TRIALS:-10}" -e PHASE_SECS="${PHASE_SECS:-15}" -e THROTTLE="${THROTTLE:-1}" \
  -e NORTC="${NORTC:-}" -e P2P="${P2P:-}" -e P2P_WAIT_MS="${P2P_WAIT_MS:-}" -e REQUIRE_P2P="${REQUIRE_P2P:-}" "$PW" \
  sh -c 'npm i -s playwright@1.56.0 >/dev/null 2>&1 && cp /t/measure.js . && node measure.js' || true
sleep 2
docker exec "$NAME-box" python3 /usr/local/bin/summarize.py | tee "${RESULT:-/dev/null}"
