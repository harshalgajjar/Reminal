// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar
//
// reminal-capture — a tiny ScreenCaptureKit helper that streams one window's
// frames as JPEGs on stdout for the reminal agent to forward to viewers.
//
// Why a separate native helper: the Go agent builds with CGO_ENABLED=0 and can't
// call ScreenCaptureKit. Shelling out to `screencapture` + `sips` per frame costs
// ~175ms/frame (two process spawns + a full-res intermediate), capping the window
// mirror at ~5fps. SCStream delivers hardware-composited, pre-scaled frames
// continuously (up to 60fps) and only when the picture actually changed, and
// ImageIO encodes JPEG on the hardware path — so per-frame cost drops to ~5-15ms.
//
// Protocol: argv = <windowID> [maxWidth] [quality 0-100] [fps] [codec].
// codec "jpeg" (default): frames are written to stdout as
// [uint32 big-endian length][JPEG bytes], repeated — unchanged since v1.10, so
// an old agent driving a new helper sees the byte stream it expects.
// codec "h264": frames are [uint32 big-endian length][1 flag byte][payload]
// where length covers flag+payload, flag 1 = delta access unit, flag 2 = key
// access unit (SPS+PPS+IDR inline), payload is Annex-B H.264 from VideoToolbox
// in low-latency mode. A "key\n" line on stdin forces an immediate IDR — for a
// static window the last frame is re-encoded, so a newly attached viewer gets
// a picture without waiting for the window to change. The agent already knows
// the window's logical size, so no per-frame dimensions are sent. Fatal
// problems (permission denied, window gone) print one line to stderr and exit
// non-zero, so the agent falls back to its screencapture path (or JPEG mode).

import ApplicationServices
import CoreGraphics
import CoreImage
import CoreMedia
import CoreServices
import CoreVideo
import Foundation
import ImageIO
import ScreenCaptureKit
import VideoToolbox

// ---- args ----
let args = CommandLine.arguments

// Input-injection subcommand: `reminal-capture scroll <x> <y> <dx> <dy>` moves
// the cursor to screen point (x,y) and posts a pixel scroll-wheel event there.
// Lives here (compiled) because the agent's JXA path builds a scroll event
// whose wheel deltas don't marshal — it posts as a silent no-op, so scrolling a
// mirrored window from a viewer did nothing. dx/dy use CGEvent convention
// (positive = up/left); the agent pre-negates from DOM deltas.
if args.count >= 2, args[1] == "scroll" {
    guard args.count >= 6, let x = Double(args[2]), let y = Double(args[3]),
          let dy = Int32(args[4]), let dx = Int32(args[5]) else {
        FileHandle.standardError.write(Data("usage: reminal-capture scroll <x> <y> <dy> <dx>\n".utf8))
        exit(2)
    }
    let src = CGEventSource(stateID: .hidSystemState)
    if let mv = CGEvent(mouseEventSource: src, mouseType: .mouseMoved,
                        mouseCursorPosition: CGPoint(x: x, y: y), mouseButton: .left) {
        mv.post(tap: .cghidEventTap)
    }
    // Modern macOS apps IGNORE phase-less synthetic scroll events (verified on
    // 14.8: plain wheel events in line or pixel units do nothing) — they only
    // honor trackpad-style gestures: continuous events carrying a scroll-phase
    // sequence. Post each call as a minimal gesture: began → changed → ended.
    func phasedScroll(_ phase: Int64, _ w1: Int32, _ w2: Int32) {
        guard let e = CGEvent(scrollWheelEvent2Source: src, units: .pixel,
                              wheelCount: 2, wheel1: w1, wheel2: w2, wheel3: 0) else { return }
        e.setIntegerValueField(.scrollWheelEventIsContinuous, value: 1)
        e.setIntegerValueField(.scrollWheelEventScrollPhase, value: phase)
        e.post(tap: .cghidEventTap)
    }
    phasedScroll(1, dy / 2, dx / 2) // began
    phasedScroll(2, dy - dy / 2, dx - dx / 2) // changed (remainder — full delta total)
    phasedScroll(4, 0, 0) // ended
    exit(0)
}

// Drag-phase subcommand: `reminal-capture drag <down|move|up> <x> <y>` posts ONE
// step of a live drag. Split into phases because a drag used to be shipped as a
// whole path after the finger lifted and replayed at scripted speed — nothing
// tracked the pointer, and the playback bore no relation to how the user
// actually moved. The mouse button is system state, not process state: a
// `down` posted here stays down after this process exits, so the later `move`
// and `up` invocations continue the same drag. That is also why `reminal
// permissions`/releaseInput exist to unstick a button if a gesture is cut off.
if args.count >= 2, args[1] == "drag" {
    guard args.count >= 5, let x = Double(args[3]), let y = Double(args[4]) else {
        FileHandle.standardError.write(Data("usage: reminal-capture drag <down|move|up> <x> <y>\n".utf8))
        exit(2)
    }
    let type: CGEventType
    switch args[2] {
    case "down": type = .leftMouseDown
    case "move": type = .leftMouseDragged
    case "up":   type = .leftMouseUp
    default:
        FileHandle.standardError.write(Data("drag phase must be down|move|up\n".utf8))
        exit(2)
    }
    let src = CGEventSource(stateID: .hidSystemState)
    if let e = CGEvent(mouseEventSource: src, mouseType: type,
                       mouseCursorPosition: CGPoint(x: x, y: y), mouseButton: .left) {
        e.post(tap: .cghidEventTap)
    }
    exit(0)
}

// Permission subcommand: `reminal-capture request` asks ScreenCaptureKit for
// shareable content purely to surface the Screen Recording (TCC) prompt, then
// reports. `reminal permissions` runs this via `open`ing the reminal.app, so the
// prompt is attributed to the bundle identity (sh.reminal) — the one grant that
// then covers the background daemon — instead of the terminal. Prints
// "granted"/"denied" and exits 0/1.
if args.count >= 2, args[1] == "request" {
    let sem = DispatchSemaphore(value: 0)
    var ok = false
    SCShareableContent.getWithCompletionHandler { content, err in
        ok = err == nil && content != nil
        sem.signal()
    }
    _ = sem.wait(timeout: .now() + 120)
    print(ok ? "granted" : "denied")
    exit(ok ? 0 : 1)
}

// Permission subcommand: `reminal-capture accessibility` surfaces the
// Accessibility (TCC) prompt — needed because injected mouse/scroll/click events
// (CGEvent → .cghidEventTap) are silently dropped without it. AXIsProcessTrusted
// WithOptions(prompt:true) shows the "…would like to control this computer" dialog
// when untrusted. Prints "granted"/"denied".
if args.count >= 2, args[1] == "accessibility" {
    let key = kAXTrustedCheckOptionPrompt.takeUnretainedValue() as String
    var trusted = AXIsProcessTrustedWithOptions([key: true] as CFDictionary)
    // Stay alive so the dialog isn't dropped while it's queued behind another TCC
    // prompt (that's why it used to appear only on a SECOND run), and pick up the
    // grant the moment the user flips it. Poll up to ~30s.
    var waited = 0
    while !trusted && waited < 30 {
        Thread.sleep(forTimeInterval: 1)
        trusted = AXIsProcessTrusted()
        waited += 1
    }
    print(trusted ? "granted" : "denied")
    exit(trusted ? 0 : 1)
}

// Preflight subcommand: `reminal-capture check` reports Screen Recording status
// WITHOUT prompting (CGPreflightScreenCaptureAccess — native; the JXA bridge
// doesn't expose it). Prints "ok"/"no". The agent uses this to warn a viewer to
// run `reminal permissions` instead of streaming silent black frames.
if args.count >= 2, args[1] == "check" {
    print(CGPreflightScreenCaptureAccess() ? "ok" : "no")
    exit(0)
}

// Preflight subcommand: `reminal-capture ax-check` reports Accessibility
// (control-this-computer) status WITHOUT prompting — AXIsProcessTrusted (no
// options) never shows the dialog, unlike the `accessibility` subcommand above.
// The daemon runs this in its granted (sh.reminal) context so `reminal permissions`
// can poll for the grant one step at a time. Prints "ok"/"no".
if args.count >= 2, args[1] == "ax-check" {
    print(AXIsProcessTrusted() ? "ok" : "no")
    exit(0)
}

// Preflight subcommand: `reminal-capture auto-check` reports Automation (Apple
// Events → System Events) status WITHOUT prompting, via
// AEDeterminePermissionToAutomateTarget(askUserIfNeeded: false). noErr means the
// grant is already in place; anything else (denied, undetermined, target not
// running) counts as "not yet". The daemon runs this in its granted context for
// `reminal permissions` polling. Prints "ok"/"no".
if args.count >= 2, args[1] == "auto-check" {
    let bundleID = "com.apple.systemevents"
    var target = AEAddressDesc()
    let bytes = Array(bundleID.utf8)
    let created = bytes.withUnsafeBufferPointer { buf in
        AECreateDesc(typeApplicationBundleID, buf.baseAddress, buf.count, &target)
    }
    if created != noErr {
        print("no")
        exit(0)
    }
    let status = AEDeterminePermissionToAutomateTarget(&target, typeWildCard, typeWildCard, false)
    AEDisposeDesc(&target)
    print(status == noErr ? "ok" : "no")
    exit(0)
}

func die(_ msg: String) -> Never {
    FileHandle.standardError.write(Data((msg + "\n").utf8))
    exit(1)
}

// ---- shared stream plumbing (one-shot mode AND `serve`) --------------------
// Declared before the `serve` dispatch below: in a main.swift, top-level `let`s
// only initialize when their line executes, and serve mode never returns — so
// anything it touches must already exist by then.

// frameByteBudget bounds each JPEG so its base64-in-JSON envelope stays well
// under every browser's DataChannel maxMessageSize (140_000 × 1.34 + overhead
// ≈ 188 KiB < Chrome's 256 KiB). See the encode loop in FrameOutput.
let frameByteBudget = 140_000

let ciContext = CIContext(options: [.useSoftwareRenderer: false])
let stdoutHandle = FileHandle.standardOutput

// encodeJPEG compresses cg at the given quality via ImageIO's hardware path.
func encodeJPEG(_ cg: CGImage, _ quality: Double) -> Data? {
    let out = NSMutableData()
    guard let dest = CGImageDestinationCreateWithData(out, "public.jpeg" as CFString, 1, nil) else { return nil }
    CGImageDestinationAddImage(dest, cg, [kCGImageDestinationLossyCompressionQuality: quality] as CFDictionary)
    guard CGImageDestinationFinalize(dest) else { return nil }
    return out as Data
}

// Frame flags for h264 framing (JPEG mode has no flag byte).
let flagH264Delta: UInt8 = 1
let flagH264Key: UInt8 = 2

// errFrameMagic mirrors winErrFrameMagic on the Go side: a reserved length
// prefix marking an out-of-band error frame (why the stream is ending) rather
// than a picture.
let errFrameMagic: UInt32 = 0xFFFF_FFFF

// Multi-stream server subcommand: `reminal-capture serve` hosts EVERY capture
// in this one process — see runServeMode below for the protocol and for why
// (replayd kills concurrent streams from same-identity sibling processes).
if args.count >= 2, args[1] == "serve" {
    runServeMode() // never returns
}

// Parent lifeline + command channel: the agent holds our stdin pipe open; EOF
// means it's gone — including deaths that skip its cleanup (SIGKILL, crash, or
// its hot-restart exec, all of which close the fd). Without this, a helper on a
// STATIC window would outlive a dead agent forever: it only notices a closed
// stdout when a frame write fails, and a static window never writes. Armed only
// when stdin is a pipe, so running the helper by hand from a terminal still works.
// In h264 mode the same pipe doubles as a command channel: a "key\n" line forces
// an immediate IDR (see H264Encoder.requestKey). Unknown lines are ignored, so
// old agents that never write and future commands both stay compatible.
var h264Encoder: H264Encoder?
var stdinStat = stat()
if fstat(0, &stdinStat) == 0 && (stdinStat.st_mode & S_IFMT) == S_IFIFO {
    DispatchQueue.global(qos: .utility).async {
        var pending = Data()
        while true {
            // availableData, NOT read(upToCount:): the latter blocks until it
            // fills its whole buffer or hits EOF on a pipe, so a "key\n" line
            // would sit undelivered until the agent died (verified live — the
            // re-key commands all arrived in one burst at EOF, milliseconds
            // before exit). availableData returns as soon as any bytes arrive.
            let d = FileHandle.standardInput.availableData
            if d.isEmpty {
                exit(0) // EOF — parent is gone
            }
            pending.append(d)
            while let nl = pending.firstIndex(of: 0x0A) {
                let line = String(data: pending[pending.startIndex..<nl], encoding: .utf8)?
                    .trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
                pending = Data(pending[pending.index(after: nl)...])
                if line == "key" { h264Encoder?.requestKey() }
            }
        }
    }
}

// Virtual display subcommand: `reminal-capture vdisplay <w> <h>` creates a
// software display of w×h points named "reminal" and keeps it alive until this
// process exits (stdin lifeline above included). Used by closed-lid mode: a
// headless Mac (lid shut, no monitor) has no display, so windows lose their
// coordinate space and capture/input die — this gives them a stable home, the
// same trick DeskPad/BetterDisplay use. Built on the private CGVirtualDisplay*
// classes, driven via the ObjC runtime: every selector is existence-checked
// first, so an OS release that changes the interface makes us exit(1) (the
// agent just doesn't get a display) rather than crash.
if args.count >= 2, args[1] == "vdisplay" {
    let vw = args.count >= 3 ? (UInt(args[2]) ?? 1920) : 1920
    let vh = args.count >= 4 ? (UInt(args[3]) ?? 1080) : 1080

    func objcNew(_ className: String) -> NSObject? {
        guard let cls = NSClassFromString(className) as? NSObject.Type else { return nil }
        return cls.init()
    }
    // KVC with an existence check so a renamed property in a future macOS makes
    // us fail cleanly instead of throwing setValue:forUndefinedKey:.
    func setIfPossible(_ obj: NSObject, _ key: String, _ value: Any?) -> Bool {
        let cap = key.prefix(1).uppercased() + key.dropFirst()
        guard obj.responds(to: NSSelectorFromString("set\(cap):")) else { return false }
        obj.setValue(value, forKey: key)
        return true
    }

    guard let desc = objcNew("CGVirtualDisplayDescriptor") else { die("vdisplay: CGVirtualDisplayDescriptor unavailable") }
    _ = setIfPossible(desc, "name", "reminal")
    guard setIfPossible(desc, "maxPixelsWide", vw), setIfPossible(desc, "maxPixelsHigh", vh) else {
        die("vdisplay: descriptor interface changed (maxPixels)")
    }
    // Physical size drives the assumed DPI; ~24" at this resolution reads sanely.
    _ = setIfPossible(desc, "sizeInMillimeters", NSValue(size: NSSize(width: 530, height: Double(vh) / Double(vw) * 530)))
    _ = setIfPossible(desc, "productID", 0x7265)
    _ = setIfPossible(desc, "vendorID", 0x6d69)
    _ = setIfPossible(desc, "serialNum", 0x0001)
    _ = setIfPossible(desc, "queue", DispatchQueue.main)

    guard let displayCls: AnyClass = NSClassFromString("CGVirtualDisplay") else { die("vdisplay: CGVirtualDisplay unavailable") }
    let initSel = NSSelectorFromString("initWithDescriptor:")
    guard class_getInstanceMethod(displayCls, initSel) != nil,
          let rawAlloc = (displayCls as AnyObject).perform(NSSelectorFromString("alloc"))?.takeUnretainedValue(),
          let display = (rawAlloc as? NSObject)?.perform(initSel, with: desc)?.takeUnretainedValue() as? NSObject
    else { die("vdisplay: CGVirtualDisplay init failed") }

    guard let modeCls: AnyClass = NSClassFromString("CGVirtualDisplayMode") else { die("vdisplay: CGVirtualDisplayMode unavailable") }
    let modeSel = NSSelectorFromString("initWithWidth:height:refreshRate:")
    guard let modeMethod = class_getInstanceMethod(modeCls, modeSel),
          let rawMode = (modeCls as AnyObject).perform(NSSelectorFromString("alloc"))?.takeUnretainedValue() as? NSObject
    else { die("vdisplay: mode interface changed") }
    typealias ModeInit = @convention(c) (NSObject, Selector, UInt, UInt, Double) -> NSObject
    let modeInit = unsafeBitCast(method_getImplementation(modeMethod), to: ModeInit.self)
    let mode = modeInit(rawMode, modeSel, vw, vh, 60.0)

    guard let settings = objcNew("CGVirtualDisplaySettings") else { die("vdisplay: CGVirtualDisplaySettings unavailable") }
    _ = setIfPossible(settings, "hiDPI", 0)
    guard setIfPossible(settings, "modes", [mode]) else { die("vdisplay: settings interface changed (modes)") }

    let applySel = NSSelectorFromString("applySettings:")
    guard let applyMethod = class_getInstanceMethod(type(of: display), applySel) else { die("vdisplay: applySettings unavailable") }
    typealias ApplyFn = @convention(c) (NSObject, Selector, NSObject) -> Bool
    let apply = unsafeBitCast(method_getImplementation(applyMethod), to: ApplyFn.self)
    guard apply(display, applySel, settings) else { die("vdisplay: applySettings failed") }

    let did = (display.value(forKey: "displayID") as? NSNumber)?.uint32Value ?? 0
    // One line to stdout so the agent can log/inspect; then hold forever. The
    // display lives exactly as long as this process (`display` is kept alive by
    // the closure reference below).
    print("vdisplay up id=\(did) \(vw)x\(vh)")
    FileHandle.standardOutput.synchronizeFile()
    withExtendedLifetime(display) { CFRunLoopRun() }
    exit(0)
}


// Target: a CGWindowID for one window, or "display:<CGDirectDisplayID>" for a
// whole desktop (the agent lists displays as pseudo-windows with that id form).
// Validated here so a typo'd invocation gets usage instead of "window 0 not
// found"; the actual lookup happens in resolveCaptureTarget.
let targetSpec = args.count >= 2 ? args[1] : ""
if UInt32(targetSpec) == nil,
   !(targetSpec.hasPrefix("display:") && UInt32(targetSpec.dropFirst("display:".count)) != nil) {
    FileHandle.standardError.write(Data("usage: reminal-capture <windowID|display:ID> [maxWidth] [quality] [fps] [jpeg|h264]\n".utf8))
    exit(2)
}
let maxWidth = args.count >= 3 ? (Int(args[2]) ?? 1100) : 1100
let quality = args.count >= 4 ? (Double(args[3]) ?? 45) / 100.0 : 0.45
// Ceiling is the panel's own rate (ProMotion tops out at 120); SCK never
// delivers faster than the display refreshes, so a higher request just idles.
let fps = args.count >= 5 ? max(1, min(120, Int(args[4]) ?? 60)) : 60
let useH264 = args.count >= 6 && args[5] == "h264"

// Serialize writes: the sample handler queue is single-threaded here, but keep a
// dedicated lock so a future multi-output setup can't interleave frame bytes.
let writeLock = NSLock()

// One queue for BOTH the SCStream sample handler and any out-of-band encoder
// submission (requestKey): VTCompressionSessionEncodeFrame is not thread-safe.
let sampleQueue = DispatchQueue(label: "reminal.capture")

// writeFramed emits one length-prefixed frame; flag is prepended inside the
// length when non-nil (h264 mode), absent for JPEG (legacy framing, byte-for-
// byte what pre-h264 agents parse). A closed pipe means the agent stopped the
// stream — exit quietly.
func writeFramed(_ payload: Data, flag: UInt8?) {
    var lenBE = UInt32(payload.count + (flag != nil ? 1 : 0)).bigEndian
    writeLock.lock()
    defer { writeLock.unlock() }
    do {
        try stdoutHandle.write(contentsOf: Data(bytes: &lenBE, count: 4))
        if let flag { try stdoutHandle.write(contentsOf: Data([flag])) }
        try stdoutHandle.write(contentsOf: payload)
    } catch {
        exit(0)
    }
}

// ---- H.264 encoder: VideoToolbox low-latency session → Annex-B access units ----
//
// Why H.264 instead of per-frame JPEG: temporal compression. Measured on a
// full-motion 1100×700 window: JPEG q45 at 30fps costs ~15 Mbps; H.264 at
// 2 Mbps is visually identical (SSIM 0.987) — a 7-10× wire reduction, which is
// the difference between "unusable on cellular P2P" and "trivial". The encoder
// runs on dedicated silicon, so CPU cost is at or below the JPEG path.
final class H264Encoder {
    // The compression session holds VideoToolbox resources that are not the
    // encoder object's to leave behind: a stream that ends — normally, or
    // because its window went away — must give them back, or they are held
    // until the process exits. The helper now outlives individual streams by
    // design, so "until the process exits" is no longer soon.
    deinit {
        VTCompressionSessionInvalidate(session)
    }

    private var session: VTCompressionSession
    private let lock = NSLock()
    private var forceNextKey = true // first frame must be an IDR regardless
    private var lastBuffer: CVPixelBuffer? // retained for static-window re-key
    // Injected so the encoder serves both one-shot mode (stdout framing, die on
    // failure) and one `serve` stream (sid-framed output; failure ends only
    // that stream). queue is the SAME queue the stream's sample handler runs
    // on — all submissions must share it, see requestKey.
    private let queue: DispatchQueue
    private let sink: (Data, UInt8) -> Void
    private let fatal: (String) -> Void

    init?(width: Int, height: Int, fps: Int, queue: DispatchQueue,
          sink: @escaping (Data, UInt8) -> Void, fatal: @escaping (String) -> Void) {
        self.queue = queue
        self.sink = sink
        self.fatal = fatal
        // Low-latency rate control (hardware-only mode: no B-frames, no frame
        // delay, bitrate honored per-frame). If the machine's encoder doesn't
        // support it, fall back to a regular realtime session.
        var s: VTCompressionSession?
        let lowLatency = [kVTVideoEncoderSpecification_EnableLowLatencyRateControl: kCFBooleanTrue] as CFDictionary
        var status = VTCompressionSessionCreate(
            allocator: nil, width: Int32(width), height: Int32(height),
            codecType: kCMVideoCodecType_H264, encoderSpecification: lowLatency,
            imageBufferAttributes: nil, compressedDataAllocator: nil,
            outputCallback: nil, refcon: nil, compressionSessionOut: &s)
        if status != noErr || s == nil {
            status = VTCompressionSessionCreate(
                allocator: nil, width: Int32(width), height: Int32(height),
                codecType: kCMVideoCodecType_H264, encoderSpecification: nil,
                imageBufferAttributes: nil, compressedDataAllocator: nil,
                outputCallback: nil, refcon: nil, compressionSessionOut: &s)
        }
        guard status == noErr, let created = s else { return nil }
        session = created

        // ~0.10 bits per pixel per frame at 30fps lands at ~2.1 Mbps for
        // 1100×636 — measured visually transparent for UI content vs the JPEG
        // stream it replaces. Frame rate scales the budget SUBLINEARLY: at a
        // higher rate consecutive frames differ less, so each costs fewer bits.
        // (A linear term made 60fps ask for exactly 2× and the encoder happily
        // spent it — measured 3.5 vs 1.7 Mbps for a picture that needs ~1.5×.)
        // Clamped so tiny windows still get enough and huge desktops don't
        // flood a P2P link. Property failures are non-fatal: an encoder that
        // ignores a hint still produces decodable output.
        let fpsScale = pow(Double(fps) / 30.0, 0.6)
        let avgBitrate = max(600_000, min(12_000_000, Int(Double(width * height) * 30.0 * 0.10 * fpsScale)))
        VTSessionSetProperty(session, key: kVTCompressionPropertyKey_RealTime, value: kCFBooleanTrue)
        VTSessionSetProperty(session, key: kVTCompressionPropertyKey_ProfileLevel, value: kVTProfileLevel_H264_Main_AutoLevel)
        VTSessionSetProperty(session, key: kVTCompressionPropertyKey_AllowFrameReordering, value: kCFBooleanFalse)
        // Periodic IDR as a SAFETY NET, not the recovery mechanism: a viewer
        // that loses sync asks for a key immediately (see requestWindowKey), so
        // this only bounds how long corruption could persist if that request
        // itself went missing. 2s costs ~4% (one ~30 KB IDR per 120 deltas);
        // the 10s it replaced meant a lost AU could smear for ten seconds.
        VTSessionSetProperty(session, key: kVTCompressionPropertyKey_MaxKeyFrameIntervalDuration, value: 2 as CFNumber)
        VTSessionSetProperty(session, key: kVTCompressionPropertyKey_ExpectedFrameRate, value: fps as CFNumber)
        VTSessionSetProperty(session, key: kVTCompressionPropertyKey_AverageBitRate, value: avgBitrate as CFNumber)
        // Hard ceiling ~1.4× average over any 1s window so keyframe spikes stay
        // under the agent's per-message DataChannel budget.
        VTSessionSetProperty(session, key: kVTCompressionPropertyKey_DataRateLimits,
                             value: [avgBitrate / 8 * 14 / 10, 1] as CFArray)
        VTCompressionSessionPrepareToEncodeFrames(session)
    }

    // encode compresses one captured frame; called on the sample handler queue.
    func encode(_ pixelBuffer: CVPixelBuffer) {
        lock.lock()
        let key = forceNextKey
        forceNextKey = false
        lastBuffer = pixelBuffer
        lock.unlock()
        submit(pixelBuffer, forceKey: key)
    }

    // requestKey forces the next frame to be an IDR. For a static window no
    // next frame is coming, so the cached last frame is re-encoded immediately
    // — a newly attached viewer must not wait for on-screen change to see
    // anything. Called from the stdin command thread.
    //
    // Two hard-won constraints (both verified live against SCK capture):
    // 1. The re-encode MUST run on the same queue as live frame submission —
    //    VTCompressionSessionEncodeFrame is not thread-safe, and a submission
    //    racing the sample handler's is silently swallowed (returns noErr, the
    //    output handler simply never fires). All submits go via sampleQueue.
    // 2. The cached buffer is DEEP-COPIED first: SCK's pooled IOSurface buffers
    //    misbehave on re-submission. A plain BGRA blit into a fresh buffer only
    //    costs on re-key, never per frame.
    func requestKey() {
        lock.lock()
        forceNextKey = true
        let cached = lastBuffer
        lock.unlock()
        guard let cached else { return } // no frame yet: the first one is a key anyway
        queue.async {
            guard let copy = Self.copyPixelBuffer(cached) else { return } // next live frame keys instead
            self.lock.lock()
            self.forceNextKey = false
            self.lock.unlock()
            self.submit(copy, forceKey: true)
        }
    }

    private static func copyPixelBuffer(_ src: CVPixelBuffer) -> CVPixelBuffer? {
        let w = CVPixelBufferGetWidth(src), h = CVPixelBufferGetHeight(src)
        var dstOpt: CVPixelBuffer?
        // IOSurface-backed like the live SCK frames: the hardware encoder
        // silently drops a malloc-backed buffer slipped into an IOSurface
        // stream (no callback at all).
        let attrs = [kCVPixelBufferIOSurfacePropertiesKey: [:] as CFDictionary] as CFDictionary
        guard CVPixelBufferCreate(nil, w, h, CVPixelBufferGetPixelFormatType(src), attrs, &dstOpt) == kCVReturnSuccess,
              let dst = dstOpt else { return nil }
        CVPixelBufferLockBaseAddress(src, .readOnly)
        CVPixelBufferLockBaseAddress(dst, [])
        defer {
            CVPixelBufferUnlockBaseAddress(dst, [])
            CVPixelBufferUnlockBaseAddress(src, .readOnly)
        }
        guard let srcBase = CVPixelBufferGetBaseAddress(src),
              let dstBase = CVPixelBufferGetBaseAddress(dst) else { return nil }
        let srcStride = CVPixelBufferGetBytesPerRow(src)
        let dstStride = CVPixelBufferGetBytesPerRow(dst)
        let rowBytes = min(srcStride, dstStride)
        for y in 0..<h {
            memcpy(dstBase + y * dstStride, srcBase + y * srcStride, rowBytes)
        }
        return dst
    }

    private func submit(_ pixelBuffer: CVPixelBuffer, forceKey: Bool) {
        // Host-clock PTS: always monotonic, even when requestKey re-submits the
        // cached buffer out of band (SCK timestamps would repeat there).
        let pts = CMClockGetTime(CMClockGetHostTimeClock())
        let props = forceKey ? [kVTEncodeFrameOptionKey_ForceKeyFrame: kCFBooleanTrue] as CFDictionary : nil
        let status = VTCompressionSessionEncodeFrame(
            session, imageBuffer: pixelBuffer, presentationTimeStamp: pts,
            duration: .invalid, frameProperties: props, infoFlagsOut: nil
        ) { status, _, sampleBuffer in
            guard status == noErr, let sampleBuffer else { return }
            self.emit(sampleBuffer)
        }
        if status != noErr {
            fatal("h264 encode failed: \(status)") // agent restarts / falls back to jpeg
        }
    }

    // emit converts one compressed sample (AVCC: length-prefixed NALs, parameter
    // sets off in the format description) to a self-contained Annex-B access
    // unit and writes it as a framed message. Keyframes carry SPS/PPS inline so
    // any AU tagged "key" is a valid decoder entry point on its own.
    private func emit(_ sampleBuffer: CMSampleBuffer) {
        let attachments = CMSampleBufferGetSampleAttachmentsArray(sampleBuffer, createIfNecessary: false)
            as? [[CFString: Any]]
        let notSync = attachments?.first?[kCMSampleAttachmentKey_NotSync] as? Bool ?? false
        let isKey = !notSync

        let startCode = Data([0, 0, 0, 1])
        var out = Data()
        var nalHeaderLen: Int32 = 4
        if let fd = CMSampleBufferGetFormatDescription(sampleBuffer) {
            var psCount = 0
            CMVideoFormatDescriptionGetH264ParameterSetAtIndex(
                fd, parameterSetIndex: 0, parameterSetPointerOut: nil,
                parameterSetSizeOut: nil, parameterSetCountOut: &psCount,
                nalUnitHeaderLengthOut: &nalHeaderLen)
            if isKey {
                for i in 0..<psCount {
                    var ptr: UnsafePointer<UInt8>?
                    var size = 0
                    let st = CMVideoFormatDescriptionGetH264ParameterSetAtIndex(
                        fd, parameterSetIndex: i, parameterSetPointerOut: &ptr,
                        parameterSetSizeOut: &size, parameterSetCountOut: nil,
                        nalUnitHeaderLengthOut: nil)
                    if st == noErr, let ptr {
                        out.append(startCode)
                        out.append(ptr, count: size)
                    }
                }
            }
        }

        guard let block = CMSampleBufferGetDataBuffer(sampleBuffer) else { return }
        var totalLen = 0
        var dataPtr: UnsafeMutablePointer<CChar>?
        guard CMBlockBufferGetDataPointer(block, atOffset: 0, lengthAtOffsetOut: nil,
                                          totalLengthOut: &totalLen, dataPointerOut: &dataPtr) == kCMBlockBufferNoErr,
              let base = dataPtr else { return }
        let hdr = Int(nalHeaderLen)
        var off = 0
        base.withMemoryRebound(to: UInt8.self, capacity: totalLen) { bytes in
            while off + hdr <= totalLen {
                var nalLen = 0
                for i in 0..<hdr { nalLen = nalLen << 8 | Int(bytes[off + i]) }
                guard nalLen > 0, off + hdr + nalLen <= totalLen else { break }
                out.append(startCode)
                out.append(UnsafeBufferPointer(start: bytes + off + hdr, count: nalLen))
                off += hdr + nalLen
            }
        }
        guard !out.isEmpty else { return }
        sink(out, isKey ? flagH264Key : flagH264Delta)
    }
}



// ---- stream output: encode changed frames to JPEG and emit them ----
final class FrameOutput: NSObject, SCStreamOutput, SCStreamDelegate {
    private let quality: Double
    private let encoder: H264Encoder? // set = h264 mode; frames bypass JPEG
    private let emit: (Data, UInt8?) -> Void // one JPEG frame (flag nil)
    private let stopped: (String) -> Void // the stream ended, with the reason

    init(quality: Double, encoder: H264Encoder?,
         emit: @escaping (Data, UInt8?) -> Void, stopped: @escaping (String) -> Void) {
        self.quality = quality
        self.encoder = encoder
        self.emit = emit
        self.stopped = stopped
    }

    // lastIngestAt stamps every frame handed to the encoder, live or refresh —
    // it is what the idle refresh consults to decide the stream has gone quiet.
    // Confined to the sample handler queue, like every path into ingest.
    private var lastIngestAt = DispatchTime(uptimeNanoseconds: 0)
    private var refreshInFlight = false // sample-queue confined
    // refreshTimer is set on the control/start path but cancelled from wherever
    // the stream dies (the encoder's fatal runs on the sample queue) — hence the
    // lock, the one cross-queue touch this class has.
    private var refreshTimer: DispatchSourceTimer?
    private let refreshLock = NSLock()

    func stream(_ stream: SCStream, didOutputSampleBuffer sampleBuffer: CMSampleBuffer, of type: SCStreamOutputType) {
        guard type == .screen else { return }

        // Emit only frames that actually changed. SCStream tags each buffer with a
        // status; .complete means new content, .idle/.blank mean "nothing changed"
        // — skipping those spares the encoder while content moves. Static windows
        // and missed changes are covered by the 1fps idle refresh instead.
        guard let attachments = CMSampleBufferGetSampleAttachmentsArray(sampleBuffer, createIfNecessary: false)
            as? [[SCStreamFrameInfo: Any]],
            let info = attachments.first,
            let statusRaw = info[.status] as? Int,
            let status = SCFrameStatus(rawValue: statusRaw),
            status == .complete
        else { return }

        guard let pixelBuffer = CMSampleBufferGetImageBuffer(sampleBuffer) else { return }
        ingest(pixelBuffer)
    }

    // startIdleRefresh floors delivery at 1fps. SCStream is change-driven — a
    // static window delivers nothing at all — and its change detection has been
    // unreliable in the field (panes stuck black or stale despite real changes).
    // So don't trust it: whenever no frame arrived for a second, re-capture the
    // target via SCScreenshotManager with the SAME filter+configuration and feed
    // the result through the ordinary frame path. A fresh screenshot is ground
    // truth, so a joining viewer always has a first frame within a second and
    // any missed change self-heals within a second. This replaces the one-shot
    // first-frame prime, which fixed only the join moment, not misses after it.
    // While content moves the tick sees a fresh lastIngestAt and does nothing.
    // Pre-Sonoma there is no one-shot SCK API — delivery stays change-driven.
    func startIdleRefresh(filter: SCContentFilter, config: SCStreamConfiguration, queue: DispatchQueue) {
        guard #available(macOS 14.0, *) else { return }
        let t = DispatchSource.makeTimerSource(queue: queue)
        refreshLock.lock()
        refreshTimer = t
        refreshLock.unlock()
        // First fire is immediate: it IS the first frame for a static target.
        t.schedule(deadline: .now(), repeating: .seconds(1), leeway: .milliseconds(100))
        t.setEventHandler { [weak self] in
            guard let self, !self.refreshInFlight,
                  DispatchTime.now().uptimeNanoseconds &- self.lastIngestAt.uptimeNanoseconds > 900_000_000
            else { return }
            self.refreshInFlight = true
            SCScreenshotManager.captureSampleBuffer(contentFilter: filter, configuration: config) { sampleBuffer, error in
                // Hop to the sample handler queue: every encoder submission must
                // share it (VTCompressionSessionEncodeFrame is not thread-safe).
                // ARC keeps the pixel buffer alive across the hop.
                queue.async {
                    self.refreshInFlight = false
                    guard error == nil, let sampleBuffer,
                          let pixelBuffer = CMSampleBufferGetImageBuffer(sampleBuffer) else { return }
                    // A live frame may have landed while the screenshot was in
                    // flight — its picture is newer; sending the screenshot now
                    // would step content backwards.
                    if DispatchTime.now().uptimeNanoseconds &- self.lastIngestAt.uptimeNanoseconds < 900_000_000 { return }
                    self.ingest(pixelBuffer)
                }
            }
        }
        t.resume()
    }

    // stopIdleRefresh ends the tick when the stream dies. Without it a dead
    // serve-mode stream would keep taking a screenshot every second forever —
    // emit() drops the frames, but the capture work itself never stops.
    func stopIdleRefresh() {
        refreshLock.lock()
        let t = refreshTimer
        refreshTimer = nil
        refreshLock.unlock()
        t?.cancel()
    }

    private func ingest(_ pixelBuffer: CVPixelBuffer) {
        lastIngestAt = DispatchTime.now()
        // h264 mode: hand the BGRA buffer straight to VideoToolbox — no CGImage,
        // no JPEG. The encoder emits framed Annex-B AUs itself.
        if let enc = encoder {
            enc.encode(pixelBuffer)
            return
        }

        let ciImage = CIImage(cvImageBuffer: pixelBuffer)
        guard let cgImage = ciContext.createCGImage(ciImage, from: ciImage.extent) else { return }

        // Encode under a byte budget: frames ride a WebRTC DataChannel as
        // base64-in-JSON (~1.34× the JPEG), and a peer CLOSES the channel on any
        // message above its maxMessageSize (Chrome 256 KiB, some browsers 64 KiB
        // — the spec says kill, not drop). A photo-heavy window at the base
        // quality can blow past that, so step quality down until the frame fits
        // — a briefly softer picture beats a dead transport.
        var q = quality
        var data = encodeJPEG(cgImage, q)
        while let d = data, d.count > frameByteBudget, q > 0.18 {
            q *= 0.65
            data = encodeJPEG(cgImage, q)
        }
        guard let jpeg = data, !jpeg.isEmpty else { return }
        emit(jpeg, nil)
    }

    func stream(_ stream: SCStream, didStopWithError error: Error) {
        stopped("stream stopped: \(error.localizedDescription)")
    }
}

// CaptureFailure is a why-not message with the Error conformance Result wants.
struct CaptureFailure: Error { let message: String }

// resolveCaptureTarget looks spec up — a CGWindowID, or "display:<id>" — in
// fresh shareable content and delivers a filter plus the target's size in
// points, or the message saying why not. Async because SCShareableContent is;
// done may run on an arbitrary queue.
func resolveCaptureTarget(_ spec: String,
                          _ done: @escaping (Result<(filter: SCContentFilter, srcW: Double, srcH: Double), CaptureFailure>) -> Void) {
    var windowID: UInt32 = 0
    var displayID: UInt32 = 0
    if spec.hasPrefix("display:"), let d = UInt32(spec.dropFirst("display:".count)) {
        displayID = d
    } else if let w = UInt32(spec) {
        windowID = w
    } else {
        done(.failure(CaptureFailure(message: "bad capture target \(spec)")))
        return
    }
    SCShareableContent.getExcludingDesktopWindows(false, onScreenWindowsOnly: true) { content, error in
        if let error {
            done(.failure(CaptureFailure(message: "shareable content (screen recording permission?): \(error.localizedDescription)")))
            return
        }
        if displayID != 0 {
            guard let display = content?.displays.first(where: { $0.displayID == displayID }) else {
                done(.failure(CaptureFailure(message: "display \(displayID) not found")))
                return
            }
            // Whole desktop: everything on the display — windows, menu bar, cursor.
            done(.success((SCContentFilter(display: display, excludingWindows: []),
                           Double(display.width), Double(display.height))))
        } else {
            guard let window = content?.windows.first(where: { $0.windowID == windowID }) else {
                done(.failure(CaptureFailure(message: "window \(windowID) not found")))
                return
            }
            done(.success((SCContentFilter(desktopIndependentWindow: window),
                           Double(window.frame.width), Double(window.frame.height))))
        }
    }
}

// buildStreamConfig sizes and configures one stream for its target.
func buildStreamConfig(filter: SCContentFilter, srcW: Double, srcH: Double,
                       maxWidth: Int, fps: Int) -> SCStreamConfiguration {
    let config = SCStreamConfiguration()

    // Output size in PIXELS. Scale the target to maxWidth wide (never up past its
    // native retina resolution), preserving aspect — SCStream does the downscale in
    // hardware, so there's no full-res intermediate to decode like the sips path.
    let pointW = max(1.0, srcW)
    let pointH = max(1.0, srcH)
    var scale = 2.0
    if #available(macOS 14.0, *) { scale = filter.pointPixelScale > 0 ? Double(filter.pointPixelScale) : 2.0 }
    let nativeW = pointW * scale
    let outW = min(Double(maxWidth), nativeW)
    let outH = outW * (pointH / pointW)
    // EVEN dimensions, always. H.264 4:2:0 subsamples chroma 2x2, so an odd width
    // or height can't be represented directly — the encoder pads to even and
    // signals a crop, and hardware decoders (Android MediaCodec especially) are
    // unreliable on that path: it renders as speckles and edge garbage. Real
    // windows hit this constantly once scaled to maxWidth: a 1728x1117 desktop
    // becomes 1100x711, a 1020x669 window becomes 1100x721. Rounding down to even
    // costs at most one pixel of height and keeps the stream on the crop-to-16
    // path every 1080p video already exercises. JPEG mode is unaffected by the
    // parity but shares the size, and one pixel there is invisible.
    config.width = max(2, Int(outW.rounded()) & ~1)
    config.height = max(2, Int(outH.rounded()) & ~1)
    config.minimumFrameInterval = CMTime(value: 1, timescale: CMTimeScale(fps)) // ceiling; the idle refresh floors delivery at 1fps
    config.pixelFormat = kCVPixelFormatType_32BGRA
    config.queueDepth = 5
    config.showsCursor = true
    return config
}

// ---- locate the target (one window, or a whole display) ----
let sema = DispatchSemaphore(value: 0)
var resolved: Result<(filter: SCContentFilter, srcW: Double, srcH: Double), CaptureFailure>!
resolveCaptureTarget(targetSpec) { resolved = $0; sema.signal() }
sema.wait()
let target: (filter: SCContentFilter, srcW: Double, srcH: Double)
switch resolved! {
case .failure(let e): die(e.message)
case .success(let t): target = t
}

// ---- configure + start capture ----
let config = buildStreamConfig(filter: target.filter, srcW: target.srcW, srcH: target.srcH,
                               maxWidth: maxWidth, fps: fps)

if useH264 {
    guard let enc = H264Encoder(width: config.width, height: config.height, fps: fps,
                                queue: sampleQueue,
                                sink: { writeFramed($0, flag: $1) },
                                fatal: { die($0) }) else {
        // No usable H.264 encoder on this machine: exit non-zero so the agent
        // falls back to requesting a JPEG stream instead.
        die("h264: VTCompressionSession unavailable")
    }
    h264Encoder = enc
}

let output = FrameOutput(quality: quality, encoder: h264Encoder,
                         emit: { writeFramed($0, flag: $1) },
                         stopped: { die($0) })
let stream = SCStream(filter: target.filter, configuration: config, delegate: output)
do {
    try stream.addStreamOutput(output, type: .screen, sampleHandlerQueue: sampleQueue)
} catch {
    die("addStreamOutput: \(error.localizedDescription)")
}
stream.startCapture { error in
    if let error { die("startCapture: \(error.localizedDescription)") }
}
// The 1fps floor doubles as the first frame: the refresh tick fires
// immediately, so a static window paints within its startup instead of
// whenever it next repaints.
output.startIdleRefresh(filter: target.filter, config: config, queue: sampleQueue)

// SCStream delivers frames on its own queue; keep the process alive until the
// agent kills it (stream stop) or the pipe closes.
CFRunLoopRun()

// ---- serve mode: every capture in ONE process --------------------------------
//
// Why: replayd (ScreenCaptureKit's backing daemon) keys a capture client's
// "application connection" by code-signing identity, not by process. The moment
// a second reminal-capture process started ANY stream, the first one's died with
// "application connection being interrupted" — one window view plus the windows-
// list previews, or two viewers, could never coexist. Streams inside one process
// coexist fine (verified: display + window side by side, none dropped), so the
// daemon keeps a single `serve` process alive and multiplexes every capture
// through it. Same binary, same sh.reminal-signed identity — the one existing
// Screen Recording grant covers it; nothing new prompts.
//
// stdin, line-based (EOF exits — the same parent-lifeline contract as one-shot):
//   start <sid> <target> <maxWidth> <quality> <fps> <jpeg|h264>
//   stop <sid>
//   key <sid>          (h264: force an immediate IDR)
// stdout: [uint32 BE sid][uint32 BE len][payload] where payload is byte-for-byte
// what one-shot mode writes (frames, or a writeMirrorError-style error frame)
// and len 0 marks end-of-stream for sid. The daemon strips the outer header and
// forwards payload verbatim, so sessions parse one unchanged format.

// ServeOut is the shared, serialized stdout writer for serve mode.
final class ServeOut {
    private let lock = NSLock()

    // send writes one outer frame. A write failure means the daemon is gone —
    // exit, taking every stream along (the daemon restarts us on demand).
    func send(_ sid: UInt32, _ payload: Data) {
        var hdr = Data(capacity: 8)
        withUnsafeBytes(of: sid.bigEndian) { hdr.append(contentsOf: $0) }
        withUnsafeBytes(of: UInt32(payload.count).bigEndian) { hdr.append(contentsOf: $0) }
        lock.lock()
        defer { lock.unlock() }
        do {
            try stdoutHandle.write(contentsOf: hdr)
            if !payload.isEmpty { try stdoutHandle.write(contentsOf: payload) }
        } catch {
            exit(0)
        }
    }

    // frame wraps one captured frame in the inner framing sessions parse —
    // identical bytes to one-shot writeFramed.
    func frame(_ sid: UInt32, _ payload: Data, flag: UInt8?) {
        var inner = Data(capacity: payload.count + 5)
        withUnsafeBytes(of: UInt32(payload.count + (flag != nil ? 1 : 0)).bigEndian) { inner.append(contentsOf: $0) }
        if let flag { inner.append(flag) }
        inner.append(payload)
        send(sid, inner)
    }

    // error emits the out-of-band error frame telling the session WHY its
    // stream is ending — the same shape the daemon's writeMirrorError produces.
    func error(_ sid: UInt32, _ msg: String) {
        let text = Data(msg.prefix(480).utf8)
        var inner = Data(capacity: text.count + 8)
        withUnsafeBytes(of: errFrameMagic.bigEndian) { inner.append(contentsOf: $0) }
        withUnsafeBytes(of: UInt32(text.count).bigEndian) { inner.append(contentsOf: $0) }
        inner.append(text)
        send(sid, inner)
    }

    // end marks sid's stream over (outer len 0); the daemon closes that session's conn.
    func end(_ sid: UInt32) { send(sid, Data()) }
}

// MuxStream is one live capture inside the serve process: its SCStream, its
// optional encoder, and a dead-latch so late sample callbacks and duplicate
// teardowns (helper error racing a daemon stop) are harmless.
final class MuxStream {
    let sid: UInt32
    let spec: String
    fileprivate var encoder: H264Encoder?
    private let out: ServeOut
    private let queue: DispatchQueue // sample handler + all encoder submissions
    private let onEnd: (UInt32) -> Void
    private var stream: SCStream?
    private var output: FrameOutput?
    private let lock = NSLock()
    private var dead = false
    // capStopped: whether this stream's capture has already been stopped.
    // Stopping is reachable from the control queue (shutdown, fail, and the
    // re-check at the end of begin) and from ScreenCaptureKit's own queue
    // (startCapture's completion), and SCStream does not promise anything
    // about being stopped twice at once. One of them does it.
    private var capStopped = false

    init(sid: UInt32, spec: String, out: ServeOut, onEnd: @escaping (UInt32) -> Void) {
        self.sid = sid
        self.spec = spec
        self.out = out
        self.onEnd = onEnd
        queue = DispatchQueue(label: "reminal.capture.\(sid)")
    }

    // stopCaptureOnce stops this stream's capture, once, whoever gets there
    // first. `known` is the stream the caller is holding — during begin the
    // property may not be set yet, and latching on a nil stream would swallow
    // the real stop that follows.
    private func stopCaptureOnce(_ known: SCStream? = nil) {
        lock.lock()
        let target = known ?? stream
        if capStopped || target == nil {
            lock.unlock()
            return
        }
        capStopped = true
        lock.unlock()
        target?.stopCapture { _ in }
    }

    private func markDead() -> Bool {
        lock.lock()
        defer { lock.unlock() }
        if dead { return false }
        dead = true
        return true
    }

    private var isDead: Bool {
        lock.lock()
        defer { lock.unlock() }
        return dead
    }

    // begin starts capturing a resolved target. Any failure is reported the way
    // the one-shot helper's die() reached sessions: same message, in-band.
    func begin(filter: SCContentFilter, srcW: Double, srcH: Double,
               maxWidth: Int, quality: Double, fps: Int, h264: Bool) {
        if isDead { return }
        let config = buildStreamConfig(filter: filter, srcW: srcW, srcH: srcH, maxWidth: maxWidth, fps: fps)
        if h264 {
            guard let enc = H264Encoder(width: config.width, height: config.height, fps: fps,
                                        queue: queue,
                                        sink: { [weak self] d, f in self?.emit(d, f) },
                                        fatal: { [weak self] m in self?.fail(m) }) else {
                fail("h264: VTCompressionSession unavailable")
                return
            }
            encoder = enc
        }
        let fo = FrameOutput(quality: quality, encoder: encoder,
                             emit: { [weak self] d, f in self?.emit(d, f) },
                             stopped: { [weak self] m in self?.fail(m) })
        output = fo
        let s = SCStream(filter: filter, configuration: config, delegate: fo)
        stream = s
        do {
            try s.addStreamOutput(fo, type: .screen, sampleHandlerQueue: queue)
        } catch {
            fail("addStreamOutput: \(error.localizedDescription)")
            return
        }
        s.startCapture { [weak self] error in
            if let error {
                self?.fail("startCapture: \(error.localizedDescription)")
            } else if self?.isDead ?? true {
                // Asked to stop while the capture was still starting: the stop
                // ran before there was anything to stop. Do it now.
                self?.stopCaptureOnce(s)
            }
        }
        // The 1fps floor doubles as the first frame: its immediate first tick is
        // what hands a joining viewer a picture on the serve-mode path — the
        // moments the black-until-clicked pane was reported at (pane open, codec
        // renegotiation restart). A dead stream's emit() already drops frames,
        // and fail/shutdown cancel the tick itself.
        fo.startIdleRefresh(filter: filter, config: config, queue: queue)
        // Setting a stream up is not instantaneous, and fail() can land
        // anywhere inside it: the encoder's fatal callback and startCapture's
        // completion both run off this queue. Landing before the properties
        // above are assigned, it finds them nil and tears down nothing;
        // landing after them but before this line, its stopIdleRefresh()
        // cancels a timer that startIdleRefresh has not created yet, and the
        // one created a moment later ticks on forever. Either way
        // ScreenCaptureKit goes on capturing a window for
        // an audience that has already gone: frames are dropped by emit(), so
        // nothing downstream ever notices, and a laptop pays for it until the
        // machine is restarted. Whoever set the stream up checks once more,
        // now that there is something to stop.
        if isDead {
            fo.stopIdleRefresh()
            stopCaptureOnce(s)
        }
    }

    private func emit(_ payload: Data, _ flag: UInt8?) {
        if isDead { return }
        out.frame(sid, payload, flag: flag)
    }

    // fail ends the stream, telling the session why (in-band error frame) and
    // the daemon log too (stderr line, relayed by the daemon — the reason only
    // exists here). Safe from any queue; the first caller wins.
    func fail(_ msg: String) {
        guard markDead() else { return }
        output?.stopIdleRefresh()
        FileHandle.standardError.write(Data("capture \(spec) ended: \(msg)\n".utf8))
        out.error(sid, msg)
        out.end(sid)
        stopCaptureOnce()
        onEnd(sid)
    }

    // shutdown tears the stream down silently — the daemon asked (session gone).
    func shutdown() {
        guard markDead() else { return }
        output?.stopIdleRefresh()
        stopCaptureOnce()
    }
}

func runServeMode() -> Never {
    let out = ServeOut()
    let control = DispatchQueue(label: "reminal.capture.serve")
    var streams: [UInt32: MuxStream] = [:] // control-queue confined

    // handle runs on the control queue. Unknown commands are ignored, so future
    // daemon verbs and this helper stay compatible in both directions.
    func handle(_ fields: [Substring]) {
        switch fields.first {
        case "start":
            guard fields.count >= 7, let sid = UInt32(fields[1]) else { return }
            let spec = String(fields[2])
            let maxWidth = Int(fields[3]) ?? 1100
            let quality = (Double(String(fields[4])) ?? 45) / 100.0
            let fps = max(1, min(120, Int(fields[5]) ?? 60))
            let h264 = fields[6] == "h264"
            let ms = MuxStream(sid: sid, spec: spec, out: out,
                               onEnd: { sid in control.async { streams.removeValue(forKey: sid) } })
            streams[sid] = ms
            resolveCaptureTarget(spec) { result in
                control.async {
                    guard streams[sid] === ms else { return } // stopped while resolving
                    switch result {
                    case .failure(let e):
                        ms.fail(e.message)
                    case .success(let t):
                        ms.begin(filter: t.filter, srcW: t.srcW, srcH: t.srcH,
                                 maxWidth: maxWidth, quality: quality, fps: fps, h264: h264)
                    }
                }
            }
        case "stop":
            guard fields.count >= 2, let sid = UInt32(fields[1]) else { return }
            streams.removeValue(forKey: sid)?.shutdown()
        case "key":
            guard fields.count >= 2, let sid = UInt32(fields[1]) else { return }
            streams[sid]?.encoder?.requestKey()
        default:
            break
        }
    }

    // Hello: one sid-0 frame announcing "this binary speaks serve". The daemon
    // probes on it — a PRE-serve binary argv-parses "serve" as a window id and
    // dies with usage, writing nothing to stdout, and process-liveness checks
    // can't tell (a zombie still signals as alive). sid 0 is never allocated,
    // so the daemon's demux drops the payload unread beyond its arrival.
    out.send(0, Data("READY".utf8))

    // Command loop on the main thread — availableData, not read(upToCount:),
    // for the same reason as the one-shot lifeline reader: on a pipe the latter
    // sits on lines until EOF.
    var pending = Data()
    while true {
        let d = FileHandle.standardInput.availableData
        if d.isEmpty { exit(0) } // EOF — the daemon is gone; streams die with us
        pending.append(d)
        while let nl = pending.firstIndex(of: 0x0A) {
            let line = String(data: pending[pending.startIndex..<nl], encoding: .utf8) ?? ""
            pending = Data(pending[pending.index(after: nl)...])
            let fields = line.split(separator: " ")
            if !fields.isEmpty { control.async { handle(fields) } }
        }
    }
}
