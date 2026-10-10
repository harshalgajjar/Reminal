#!/usr/bin/env bash
# Issue #198: a QR written to a file must survive any shell. Builds reminal,
# then (in a container, local relay) checks every command that prints a QR.
# EXPOSE=1 also checks `reminal expose` against the real relay. Needs docker; nothing here touches the host's reminal.
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd ../.. && pwd)
GOOS=linux GOARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/') \
  go build -C "$ROOT" -o "$PWD/reminal" ./cmd/reminal
trap 'rm -f "$PWD/reminal"' EXIT
docker build -q -t reminal-qr-test . >/dev/null
docker run --rm -e EXPOSE="${EXPOSE:-}" reminal-qr-test
