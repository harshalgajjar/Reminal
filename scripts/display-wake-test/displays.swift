// Prints the main display's id, whether every display is asleep, and the id of
// an app window on screen (0 for none): "<display> <0|1> <window>".
import CoreGraphics
var ids = [CGDirectDisplayID](repeating: 0, count: 16)
var n: UInt32 = 0
_ = CGGetOnlineDisplayList(16, &ids, &n)
let asleep = ids.prefix(Int(n)).allSatisfy { CGDisplayIsAsleep($0) != 0 }
let wins = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID) as? [[String: Any]] ?? []
let win = wins.first { ($0[kCGWindowLayer as String] as? Int) == 0 && ($0[kCGWindowOwnerName as String] as? String) == "TextEdit" }?[kCGWindowNumber as String] as? Int ?? 0
print(CGMainDisplayID(), asleep ? 1 : 0, win)
