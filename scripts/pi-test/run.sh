#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# Verify reminal's pi extension against a real pi, in a container.
#
#   scripts/pi-test/run.sh            # the whole check
#   scripts/pi-test/run.sh bash       # a shell in the same box
#   scripts/pi-test/run.sh go test ./internal/piext/
set -e

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT=$(CDPATH= cd -- "$DIR/../.." && pwd)
IMAGE=reminal-pi-test

if ! docker info >/dev/null 2>&1; then
    echo "Docker isn't running — start Docker Desktop (open -a Docker on macOS) and try again." >&2
    exit 1
fi

docker build -t "$IMAGE" "$DIR"

# A terminal only when there is one to attach: asking for a TTY without one is a
# hard error, which would make this unusable from a script or from CI.
TTY=
[ -t 0 ] && TTY=-it

# The repo is mounted read-only: this rig must never be able to write into the
# checkout it is testing. Caches are volumes so a re-run doesn't refetch the world.
# shellcheck disable=SC2086 # $TTY is a flag or nothing, and must not be quoted
exec docker run --rm $TTY \
    -v "$ROOT":/src:ro \
    -v reminal-pi-gocache:/root/.cache/go-build \
    -v reminal-pi-gomod:/go/pkg/mod \
    "$IMAGE" "$@"
