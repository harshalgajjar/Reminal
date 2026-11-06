// Prints the id of a TextEdit window on screen, 0 for none.
import CoreGraphics
import Foundation
let wins = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID) as? [[String: Any]] ?? []
let win = wins.first { ($0[kCGWindowLayer as String] as? Int) == 0 && ($0[kCGWindowOwnerName as String] as? String) == "TextEdit" }?[kCGWindowNumber as String] as? Int ?? 0
print(win)
