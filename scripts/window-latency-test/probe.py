# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 Harshal Gajjar
#
# The probe window. Every key or click flips it between black and white and
# logs when the event arrived (wall clock ms, the same clock the viewer's
# container reads): input -> host latency is read from that. While
# /shared/anim exists a bar moves along its top edge every 16 ms, so the
# window changes continuously: delivered frame rate and jitter are read from
# that. The flip covers the centre, which the bar never reaches.
import os, time, tkinter as tk
W, H = 480, 320
root = tk.Tk()
root.title("latency-probe")
root.geometry(f"{W}x{H}+120+120")
c = tk.Canvas(root, width=W, height=H, highlightthickness=0, bg="#000000")
c.pack(fill="both", expand=True)
bar = c.create_rectangle(0, 0, 40, 24, fill="#ff0000", width=0)
state = {"v": 0, "x": 0}
log = open("/shared/probe.log", "a", buffering=1)
# PROBE_STEP_MS: the window answers an input in two draws, as an app does (a
# button shows pressed at once, the result comes a moment later): a strip
# along the bottom edge changes at once, and the centre flips that many ms
# after. The strip is a changed frame that does not show the result.
STEP = int(os.environ.get("PROBE_STEP_MS", "0") or 0)
strip = c.create_rectangle(0, H - 24, W, H, fill="#202020", width=0)
def show(kind, t):
    state["v"] ^= 1
    c.configure(bg="#ffffff" if state["v"] else "#000000")
    log.write(f"{kind} {t:.3f} {state['v']}\n")
def flip(kind):
    t = time.time() * 1000
    if STEP <= 0 or kind == "self":
        return show(kind, t)
    state["s"] = state.get("s", 0) ^ 1
    c.itemconfigure(strip, fill="#3060ff" if state["s"] else "#202020")
    root.after(STEP, lambda: show(kind, t))
root.bind("<Key>", lambda e: flip("key"))
root.bind("<Button-1>", lambda e: flip("click"))
# Each wheel notch (X11 buttons 4 and 5) is logged and flips it too.
root.bind("<Button-4>", lambda e: flip("wheel"))
root.bind("<Button-5>", lambda e: flip("wheel"))
def tick():
    # A change the window makes on its own, with no input (a build finishing,
    # a log line): /shared/selfflip asks for one flip, logged as "self".
    if os.path.exists("/shared/selfflip"):
        try:
            os.remove("/shared/selfflip")
        except FileNotFoundError:
            pass
        flip("self")
    if os.path.exists("/shared/anim"):
        state["x"] = (state["x"] + 6) % (W - 40)
        c.coords(bar, state["x"], 0, state["x"] + 40, 24)
    root.after(16, tick)
tick()
root.focus_force()
root.mainloop()
