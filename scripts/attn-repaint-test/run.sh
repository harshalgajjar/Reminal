#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# Does output with no turn make a resting agent read "working", then "done"?
# An idle claude stand-in (fakeidle.go) whose hook last said "done"; then it
# repaints (SIGWINCH, as a viewer connecting or resizing makes it do — past
# the resize grace, as a second resize or a debounced redraw is) and later
# changes only its footer. The attention samples (REMINAL_ATTENTION_PROBE)
# are printed as they change. PASS = it never leaves "done".
# Run in Docker, never on a real HOME:
#   docker run --rm -v "$PWD/scripts/attn-repaint-test":/t:ro -v <dir with reminal, claude>:/b:ro golang:1.26-bookworm bash /t/run.sh
set -u
cp /b/reminal /b/claude /usr/local/bin/; chmod +x /usr/local/bin/reminal /usr/local/bin/claude
export HOME=$(mktemp -d) REMINAL_RELAY=ws://127.0.0.1:1 REMINAL_WEB=http://127.0.0.1:1 REMINAL_KEYSTORE=file
export REMINAL_ATTENTION_PROBE=/tmp/probe.jsonl SHELL=/usr/local/bin/claude
id=$(reminal new idle 2>&1 | grep -o "[A-Z0-9]\{8\}" | head -1)
sleep 3
REMINAL_SESSION=$id reminal hook done </dev/null   # its last turn finished
sleep 8
pid=$(pgrep -x claude | head -1)
kill -WINCH $pid; sleep 6     # a viewer connects: a repaint
kill -WINCH $pid; sleep 6     # and resizes again
sleep 35                      # the footer changes by itself
python3 - <<'PY'
import json
last=None
for l in open('/tmp/probe.jsonl'):
    e=json.loads(l)
    if e['fg']!='claude': continue
    k=(e['state'],e['source'])
    if k!=last:
        print(e['ts']%1000000, e['state'], e['source'], repr(e['tail'][-50:])); last=k
PY
python3 - <<'PY'
import json
s=[json.loads(l) for l in open('/tmp/probe.jsonl')]
s=[e for e in s if e['fg']=='claude']
i=next((i for i,e in enumerate(s) if e['state']=='done' and e['source'].startswith('hook')), None)
bad=[e for e in s[i:]] if i is not None else []
bad=[e for e in bad if e['state']!='done']
print("FAIL: left done with no turn (%d samples, from %s)" % (len(bad), sorted({e['source'] for e in bad})) if bad else "PASS: stayed done through the repaints and the footer change")
PY
