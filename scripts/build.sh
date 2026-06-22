#!/bin/bash
set -euo pipefail

VERSION="${VERSION:-0.3.5}"
OUTPUT="${OUTPUT:-dist/reminal}"

mkdir -p dist

RELAY_LDFLAGS="-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.reminal.workers.dev/ws -X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.reminal.workers.dev"

echo "Building reminal ${VERSION}..."
CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION} ${RELAY_LDFLAGS}" -o "${OUTPUT}" ./cmd/reminal

echo "Built ${OUTPUT}"
"${OUTPUT}" version
