#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# `reminal restore` across a "reboot", in Docker: a relay, and a box whose
# home is a volume — restarting the box kills every process (no signal
# reaches them, as in a power cut) and keeps the disk. Nothing on the host
# is touched.
#
#   scripts/restore-test/run.sh          # build, then run check.sh
#   scripts/restore-test/run.sh shared   # build, then run check-shared.sh
#   scripts/restore-test/run.sh down     # remove it all
set -e
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT=$(CDPATH= cd -- "$DIR/../.." && pwd)
NET=reminal-restore-net IMG=reminal-restore-test

down() {
    for c in reminal-restore-box reminal-restore-relay; do
        if docker container inspect "$c" >/dev/null 2>&1; then docker rm -f "$c" >/dev/null; fi
    done
    if docker volume inspect reminal-restore-home >/dev/null 2>&1; then docker volume rm reminal-restore-home >/dev/null; fi
    if docker network inspect "$NET" >/dev/null 2>&1; then docker network rm "$NET" >/dev/null; fi
}
if [ "$1" = down ]; then down; echo stopped; exit 0; fi
if ! docker info >/dev/null 2>&1; then echo "Docker isn't running." >&2; exit 1; fi

OUT=$(mktemp -d)
trap 'rm -rf "$OUT"' EXIT
cp "$DIR/Dockerfile" "$DIR/mcp.sh" "$OUT/"
docker run --rm -v "$ROOT":/src:ro -v "$OUT":/out \
    -v reminal-restore-gocache:/root/.cache/go-build -v reminal-restore-gomod:/go/pkg/mod \
    -e GOFLAGS=-buildvcs=false -e CGO_ENABLED=0 -w /src \
    golang:1.26-bookworm sh -c 'go build -o /out/reminal ./cmd/reminal && go build -o /out/claude ./scripts/restore-test/fakeclaude'
docker build -q -t "$IMG" "$OUT" >/dev/null

down
docker network create "$NET" >/dev/null
docker volume create reminal-restore-home >/dev/null
docker run -d --name reminal-restore-relay --network "$NET" "$IMG" reminal relay 8080 >/dev/null
docker run -d --name reminal-restore-box --init --network "$NET" -v reminal-restore-home:/root \
    -e REMINAL_RELAY=ws://reminal-restore-relay:8080/ws -e REMINAL_WEB=http://reminal-restore-relay:8080 \
    "$IMG" >/dev/null
if [ "$1" = shared ]; then exec "$DIR/check-shared.sh"; fi
exec "$DIR/check.sh"
