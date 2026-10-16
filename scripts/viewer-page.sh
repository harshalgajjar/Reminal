#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# The viewer page as it is served, built into a new directory whose path is
# printed: for a rig that serves the page from a directory. It is built
# (cloudflare/scripts/build-viewer.mjs, in a container), never committed —
# only internal/client/web is source. The caller removes the directory.
#
#   PAGE=$(scripts/viewer-page.sh)
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
OUT=$(mktemp -d)
docker run --rm -v "$ROOT":/r:ro -v "$OUT":/o "${VIEWER_PAGE_IMAGE:-node:22-bookworm-slim}" \
  node /r/cloudflare/scripts/build-viewer.mjs --web /r/internal/client/web --out /o >&2
echo "$OUT"
