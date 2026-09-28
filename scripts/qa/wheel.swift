// wheel posts real scroll-wheel events at a screen point, for the QA
// driver's SCROLL step: Orca's scroll reports success but never reaches a
// terminal's mouse reporting. The pointer moves to the point (wheel events
// go to the window under it) and returns to where it was afterwards.
//
// usage: wheel <x> <y> <up|down> <notches>
import CoreGraphics
import Foundation

let args = CommandLine.arguments
guard args.count == 5, let x = Double(args[1]), let y = Double(args[2]),
      let notches = Int(args[4]), args[3] == "up" || args[3] == "down" else {
    FileHandle.standardError.write("usage: wheel <x> <y> <up|down> <notches>\n".data(using: .utf8)!)
    exit(2)
}
guard CGPreflightPostEventAccess() else {
    FileHandle.standardError.write("wheel: this process may not post input events (Accessibility)\n".data(using: .utf8)!)
    exit(1)
}
let target = CGPoint(x: x, y: y)
let original = CGEvent(source: nil)?.location ?? target
CGWarpMouseCursorPosition(target)
if let move = CGEvent(mouseEventSource: nil, mouseType: .mouseMoved, mouseCursorPosition: target, mouseButton: .left) {
    move.post(tap: .cghidEventTap)
}
usleep(50_000)
// With natural scrolling on (the macOS default, and the setting when the
// key is unset) the terminal reads a posted positive delta as wheel-down:
// probed in iTerm2 with SGR mouse reporting, +1 arrived as button 65.
let natural = UserDefaults.standard.object(forKey: "com.apple.swipescrolldirection") as? Bool ?? true
var delta: Int32 = args[3] == "up" ? 1 : -1
if natural {
    delta = -delta
}
for _ in 0..<notches {
    if let ev = CGEvent(scrollWheelEvent2Source: nil, units: .line, wheelCount: 1, wheel1: delta, wheel2: 0, wheel3: 0) {
        ev.location = target
        ev.post(tap: .cghidEventTap)
    }
    usleep(40_000)
}
usleep(50_000)
CGWarpMouseCursorPosition(original)
