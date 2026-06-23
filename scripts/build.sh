#!/bin/bash
set -euo pipefail

VERSION="${VERSION:-0.7.1}"
OUTPUT="${OUTPUT:-dist/reminal}"

mkdir -p dist

RELAY_LDFLAGS="-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.futuristic.workers.dev/ws -X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.futuristic.workers.dev"
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
BUILD_LDFLAGS="-X main.buildDate=${BUILD_DATE} -X main.commit=${COMMIT}"

echo "Building reminal ${VERSION}..."
CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION} ${BUILD_LDFLAGS} ${RELAY_LDFLAGS}" -o "${OUTPUT}" ./cmd/reminal

echo "Built ${OUTPUT}"
"${OUTPUT}" version
