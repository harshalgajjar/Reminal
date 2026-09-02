# A closed MacBook isn't a server — unless you lie to it twice

*How reminal keeps a lid-shut Mac awake **and** rendering, in software, with
no dummy HDMI plug.*

Coding agents can run for an hour or more now if you scope the work right —
Claude Code chewing on a refactor, Codex working a queue overnight. Run them
that way and the pattern changes: you kick it off and walk away. A
long-running process needs a machine that stays up. What most of us actually
own is a laptop engineered to do the opposite: close the lid and it's gone.

There's an $8 piece of plastic called a dummy HDMI plug whose entire job is to
fix this by lying to your Mac about having a monitor. People who run MacBooks
as home servers buy them in multi-packs. I wanted the lie in software — and it
turned out to be two separate lies you have to tell, not one.

## What closing the lid does

Close a MacBook's lid and two distinct things happen.

**It sleeps.** macOS puts a lid-closed MacBook to sleep unless it's on power
with an external display *and* keyboard attached. The build stops, the agent
freezes mid-task, your SSH session drops.

**It loses its display.** This is the half almost everyone misses. Suppose you
defeat sleep — `sudo pmset -a disablesleep 1`, or Amphetamine. The machine
hums along, and a terminal-only agent even keeps working. But with the lid
shut and no monitor, there is no display attached at all, and macOS quietly
dismantles the display coordinate space. Windows migrate to a phantom
arrangement. Screen capture APIs have nothing to attach to — ScreenCaptureKit
returns black. Injected clicks land in a space that no longer exists. The
browser your agent is driving, the test runner scrolling in the next window,
the diff you'd want to eyeball from your phone — all of it unrenderable. The
process runs on, but you've lost every way of seeing it, and it has lost the
GUI half of its hands.

**Awake isn't the same as observable.**

The dummy plug solves both halves at once by faking EDID at the hardware
level: the Mac believes a monitor is present, so it neither sleeps (given
power and keyboard) nor tears down the display space. To replace it in
software you need both halves separately.

## Half one: sleep

The boring half. `pmset disablesleep` needs root, so a userspace agent
shouldn't — and can't quietly — do it. reminal flips it from the settings flow
where you grant that intent explicitly, and undoes it when you toggle closed-lid
mode off. Nothing novel here; `caffeinate` and Amphetamine live in this half.

## Half two: a display that isn't there

macOS has had virtual display machinery for years — it's how Sidecar and
AirPlay-as-display work. The relevant classes are `CGVirtualDisplay`,
`CGVirtualDisplayDescriptor`, `CGVirtualDisplayMode` and
`CGVirtualDisplaySettings`. They're private, but tools like DeskPad and
BetterDisplay have used them stably across many macOS releases.

Creating one is almost anticlimactic:

```swift
let desc = CGVirtualDisplayDescriptor()
desc.name = "reminal"
desc.maxPixelsWide = 1920
desc.maxPixelsHigh = 1080
desc.sizeInMillimeters = ...   // physical size drives the assumed DPI
let display = CGVirtualDisplay(descriptor: desc)
let mode = CGVirtualDisplayMode(width: 1920, height: 1080, refreshRate: 60)
let settings = CGVirtualDisplaySettings()
settings.modes = [mode]
display.apply(settings)
CFRunLoopRun()   // the display lives exactly as long as this process
```

The moment `apply` succeeds, the machine has a display again. Windows snap back
into a real coordinate space, ScreenCaptureKit gets frames, synthetic input
lands where it should.

Because the API is private, reminal never links against it. Everything goes
through the Objective-C runtime — `NSClassFromString`, existence-checked
selectors, KVC behind a `responds(to:)` guard — so if a macOS release renames
anything, the helper exits with a message instead of crashing, and you simply
don't get a virtual display that day. **Private API as progressive
enhancement, not a load-bearing wall.**

Two details I enjoyed more than I should have:

- The descriptor takes a physical size in millimetres, and *that* is what
  drives the assumed DPI. reminal reports ~530 mm wide — a sane 24-inch
  monitor — so text renders at a sensible size.
- A display needs a vendor and product ID. reminal's virtual display is vendor
  `0x6d69`, product `0x7265`: `mi`, `re` in ASCII.

## Knowing *when* to lie

The display should exist only while the Mac is actually headless. A monitor
plugged back in should win. So the agent runs a census every 12 seconds — a
one-line JXA script enumerating `NSScreen.screens`, skipping any display named
`reminal` — and:

- real displays present → remember the first one's size, so a later virtual
  display can match the layout the windows were living in, then tear ours down;
- zero real displays and closed-lid mode enabled → spawn the helper.

Several reminal agents can run on one machine and exactly one virtual display
should exist, so a pidfile at `~/.reminal/vdisplay.pid` holds the helper's PID;
a signal-0 probe tells the other agents the display is already covered.

The helper is disposable by design: it inherits a stdin pipe from the agent and
treats EOF as an exit order. If the agent dies — SIGKILL, crash, upgrade — the
pipe closes and the display goes with it. No orphaned lies about monitors that
outlive the thing that needed them.

## Why bother

Once a lid-shut Mac keeps rendering, the rest follows. reminal streams every
window and terminal on it into any browser — phone, iPad, a borrowed laptop —
with real input, end-to-end encrypted, no open ports, no account. Kick off the
agent, shut the lid, put the machine in a bag. From a browser you can watch the
terminal it's typing into and the browser window it's driving, and when it
stops to ask "may I run this migration?", you tap the real window and answer.

## Check my work

- [`internal/client/vdisplay_darwin.go`](../internal/client/vdisplay_darwin.go)
  — the census, the pidfile, the helper's lifecycle
- [`native/reminal-capture/main.swift`](../native/reminal-capture/main.swift)
  — the runtime-resolved `CGVirtualDisplay` calls

**One honest caveat:** this rides on private API. It's the same machinery
DeskPad and BetterDisplay use and it has been stable for years, but any macOS
release could change it — which is why the failure mode is "no virtual display
today", never a crash.
