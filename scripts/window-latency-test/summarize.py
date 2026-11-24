# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# run.sh: one result from viewer.json (the viewer's side), probe.log (when
# each input reached the window) and procstat.jsonl (CPU and memory by
# phase). Each latency trial is matched to the first probe event after it
# was sent, within 3 s: glass-to-glass = send -> the changed pixel drawn;
# input -> host = send -> the window got it; host -> paint = the rest.
import json, sys
from statistics import median
S = "/shared/"
v = json.load(open(S + "viewer.json"))
if v.get("error"):
    print(json.dumps({"error": v["error"]})); sys.exit(1)
events = []
for line in open(S + "probe.log"):
    k, t, _ = line.split(); events.append((k, float(t)))
def pct(xs, p):
    xs = sorted(x for x in xs if x is not None)
    return None if not xs else round(xs[min(len(xs) - 1, int(len(xs) * p))], 1)
res = {k: v.get(k) for k in ("p2p", "p2pAfterMs", "rtcState", "inputChannel", "codec", "h264Decode")}
res["latency"] = {}
used = set()
for tag, trials in v["trials"].items():
    g2g, in2h, h2p, lost = [], [], [], 0
    for tr in trials:
        ev = next((i for i, (k, t) in enumerate(events) if i not in used and tr["w0"] - 5 <= t <= tr["w0"] + 3000), None)
        if ev is not None: used.add(ev)
        if tr["g2g"] is None: lost += 1; continue
        g2g.append(tr["g2g"])
        if ev is not None:
            in2h.append(events[ev][1] - tr["w0"])
            if tr["paintWall"]: h2p.append(tr["paintWall"] - events[ev][1])
    res["latency"][tag] = {
        "trials": len(trials), "lost": lost,
        "g2g_ms": {"p50": pct(g2g, .5), "p90": pct(g2g, .9), "p95": pct(g2g, .95), "max": pct(g2g, 1), "min": pct(g2g, 0)},
        # How many taps took a relay pacing interval or more (wsFrameMinInterval).
        "g2g_200ms_or_more": sum(1 for x in g2g if x >= 200),
        "frames_drawn_per_trial": (v.get("framesPerTrial") or {}).get(tag),
        "g2g_all_ms": sorted(round(x) for x in g2g),
        "input_to_host_ms": {"p50": pct(in2h, .5), "p95": pct(in2h, .95)},
        "host_to_paint_ms": {"p50": pct(h2p, .5), "p95": pct(h2p, .95)},
        "by_kind_p50": {k: pct([t["g2g"] for t in trials if t["kind"] == k], .5) for k in ("key", "click")},
    }
if v.get("passive"):
    # A change the window made on its own after being still: its flip -> the pane showing it.
    pv, lostp = [], 0
    for tr in v["passive"]:
        ev = next((t for k, t in events if k == "self" and tr["w0"] - 5 <= t <= tr["w0"] + 1000), None)
        if ev is None or tr["paintWall"] is None:
            lostp += 1; continue
        pv.append(tr["paintWall"] - ev)
    res["passive_change_to_paint_ms"] = {"trials": len(v["passive"]), "lost": lostp, "p50": pct(pv, .5), "p95": pct(pv, .95), "max": pct(pv, 1)}
if v.get("rapid"):
    # Inputs close together: send -> the last of them reaching the window.
    def last(trials, kind, want):
        xs, short = [], 0
        for tr in trials:
            got = [t for k, t in events if k == kind and tr["w0"] - 5 <= t <= tr["w0"] + 2400]
            if len(got) < want: short += 1
            if got: xs.append(got[-1] - tr["w0"])
        return {"trials": len(trials), "short": short, "p50": pct(xs, .5), "p95": pct(xs, .95), "max": pct(xs, 1)}
    res["rapid_scroll_5_notches_last_ms"] = last(v["rapid"]["scroll"], "wheel", 5)
    res["rapid_3_clicks_last_ms"] = last(v["rapid"]["clicks"], "click", 3)
res["viewer"] = v["phases"]
cpu = {}
for line in open(S + "procstat.jsonl"):
    s = json.loads(line)
    if not s["phase"]: continue
    ph = cpu.setdefault(s["phase"], {})
    for r, a in s["by"].items():
        x = ph.setdefault(r, {"cpu": [], "rss": 0})
        x["cpu"].append(a["cpu"]); x["rss"] = max(x["rss"], a["rss"])
res["host"] = {ph: {r: {"cpu_pct": round(sum(x["cpu"][1:]) / max(1, len(x["cpu"]) - 1), 1), "rss_mb": round(x["rss"] / 1048576, 1)} for r, x in rs.items()} for ph, rs in cpu.items()}
print(json.dumps(res, indent=1))
