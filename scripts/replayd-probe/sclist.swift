// One SCShareableContent call, then exit: a short replayd connection, like a
// browser enumerating screens.
import Foundation
import ScreenCaptureKit
let s = DispatchSemaphore(value: 0)
SCShareableContent.getExcludingDesktopWindows(false, onScreenWindowsOnly: true) { c, e in
    print("sclist displays=\(c?.displays.count ?? -1) err=\(e?.localizedDescription ?? "none")")
    s.signal()
}
_ = s.wait(timeout: .now() + 5)
