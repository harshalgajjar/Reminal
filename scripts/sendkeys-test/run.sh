#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# send_keys in front of zsh, in Docker: one call whose text ends in a Return
# has to run the line, as the text and then a Return in two calls does.
# Nothing on the host is touched.
#
#   scripts/sendkeys-test/run.sh
set -e
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT=$(CDPATH= cd -- "$DIR/../.." && pwd)
NAME=reminal-sendkeys-$$-$(date +%s)
if ! docker info >/dev/null 2>&1; then echo "Docker isn't running." >&2; exit 1; fi
OUT=$(mktemp -d)
trap 'docker rm -f "$NAME" >/dev/null 2>&1 || true; docker rmi -f "$NAME" >/dev/null 2>&1 || true; "$ROOT/scripts/rm-tmp.sh" "$OUT"' EXIT
cp "$DIR/Dockerfile" "$OUT/"
docker run --rm -v "$ROOT":/src:ro -v "$OUT":/out \
    -v reminal-restore-gocache:/root/.cache/go-build -v reminal-restore-gomod:/go/pkg/mod \
    -e GOFLAGS=-buildvcs=false -e CGO_ENABLED=0 -w /src \
    golang:1.26-bookworm go build -o /out/reminal ./cmd/reminal
docker build -q -t "$NAME" "$OUT" >/dev/null
docker run -d --name "$NAME" --init "$NAME" >/dev/null
BOX=$NAME "$DIR/check.sh"
