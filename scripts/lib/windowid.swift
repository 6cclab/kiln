// Prints the CGWindowID of the frontmost window whose title contains the
// argument. Used by scripts/visual-drive.sh to screenshot one iTerm2 window.
import CoreGraphics
import Foundation
let needle = CommandLine.arguments.count > 1 ? CommandLine.arguments[1] : ""
let list = CGWindowListCopyWindowInfo([.optionOnScreenOnly], kCGNullWindowID) as! [[String: Any]]
for w in list {
  guard (w["kCGWindowLayer"] as? Int) == 0 else { continue }
  let name = (w["kCGWindowName"] as? String) ?? ""
  if name.contains(needle) { print(w["kCGWindowNumber"]!); break }
}
