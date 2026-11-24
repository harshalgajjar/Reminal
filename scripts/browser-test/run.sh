#!/usr/bin/env bash
# The browser feature end to end, in Docker only (never the host's Chrome or
# profile): reminal's extension loaded in Playwright's Chromium on Xvfb, with
# a throwaway HOME, reminal's daemon and a session whose stand-in agent uses
# the browser_* tools. check.mjs answers the consent window as the person
# would (Deny first: the control; then Allow) and checks each tool and rule.
#   GIF=1  record the run to $SHOTS/browser.gif (SHOTS defaults to ./out)
#   POLICY=1  only the checks of a managed browser: an administrator's policy
#             starts three switches on (the full run is its control: no policy)
#   MODE=upgrade  reminal upgraded in place (upgrade.sh): the release before
#             is used in a profile, then the browser starts again on this
#             tree's files at the same path. release.yml runs it before
#             anything is built to publish. PREV_REF names the release before
#             (default: the newest release tag before this commit); NEW_REF
#             builds that ref in place of this tree (to see the check fail on
#             a release it would have stopped).
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd ../.. && pwd)
OUT=$(mktemp -d)
trap 'rm -rf "${OUT:?}"' EXIT
SHOTS="${SHOTS:-$PWD/out}"; mkdir -p "$SHOTS"
docker image inspect reminal-browser-rig:2 >/dev/null 2>&1 || docker build -q -t reminal-browser-rig:2 . >/dev/null
# build <source folder> <name>: that tree's reminal, as $OUT/<name>.
build() {
  docker run --rm --cpus 2 -v "$1":/src:ro -v "$OUT":/out -v reminal-browser-rig-gomod:/go/pkg/mod -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false -e NAME="$2" golang:1.26-bookworm \
    sh -c 'mkdir -p /w && cp -r /src/. /w/ && cd /w && go build -o /out/$NAME ./cmd/reminal'
}
# tree <ref>: that commit's files, in a folder of $OUT.
tree() { mkdir -p "$OUT/src-$1" && git -C "$ROOT" archive "$1" | tar -x -C "$OUT/src-$1" && echo "$OUT/src-$1"; }
if [ "${MODE:-full}" = upgrade ]; then
  PREV_REF="${PREV_REF:-$(git -C "$ROOT" describe --tags --abbrev=0 --match 'v[0-9]*' --exclude '*-rc*' HEAD^)}"
  echo "== upgrade in place: from $PREV_REF to ${NEW_REF:-this tree}"
  build "$(tree "$PREV_REF")" reminal-prev
  if [ -n "${NEW_REF:-}" ]; then build "$(tree "$NEW_REF")" reminal; else build "$ROOT" reminal; fi
  docker run --rm --cpus 2 --shm-size=1g -v "$ROOT":/r:ro -v "$OUT":/out:ro reminal-browser-rig:2 bash /r/scripts/browser-test/upgrade.sh
  exit
fi
build "$ROOT" reminal
docker run --rm --cpus 2 --shm-size=1g -v "$ROOT":/r:ro -v "$OUT":/out:ro -v "$SHOTS":/s -e GIF="${GIF:-}" -e GIF_SPEED="${GIF_SPEED:-1.6}" -e MODE="${MODE:-full}" -e RATIO="${RATIO:-}" -e POLICY="${POLICY:-}" \
  reminal-browser-rig:2 bash /r/scripts/browser-test/rig.sh
