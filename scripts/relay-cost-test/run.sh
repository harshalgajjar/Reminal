#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# Runs relay.test.ts against the Worker's own source in the real Workers
# runtime (workerd, through @cloudflare/vitest-pool-workers), in Docker.
# Nothing is deployed and nothing on the host is touched.
#
#   scripts/relay-cost-test/run.sh
set -e
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT=$(CDPATH= cd -- "$DIR/../.." && pwd)
docker run --rm -v "$ROOT/cloudflare":/src:ro -v "$DIR":/t:ro node:24-bookworm sh -c '
  set -e
  mkdir -p /w && cd /w && cp -R /src/src /src/public /src/wrangler.toml /src/tsconfig.json . && npm init -y >/dev/null
  mkdir test && cp /t/relay.test.ts test/ && cp /t/vitest.config.mts .
  # The deploy routes and account are not the test'"'"'s; workerd runs locally.
  sed -i "/^account_id/d; /^routes = \[/,/^\]/d" wrangler.toml
  # The runtime dependency of the Worker, and the test pool; not the dev
  # dependencies of the Worker, whose pinned types the pool does not accept.
  npm i --no-audit --no-fund "bcryptjs@^3.0.2" -D @cloudflare/vitest-pool-workers@0.23.0 "vitest@^4.1.0" >/tmp/npm.log 2>&1 || { tail -20 /tmp/npm.log; exit 1; }
  npm install-scripts approve esbuild workerd >/dev/null 2>&1 || true
  npm rebuild esbuild workerd >/dev/null 2>&1 || true
  npx vitest run --reporter=verbose 2>&1
'
