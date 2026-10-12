import AppKit
import Foundation
import Testing

@testable import LassoDesktopKit

// Real clicks: the pointer's events posted to the window server, which sends
// them to the window under the pointer, where NSApp.run hands them on (the
// tests run under Scripts/test-host.swift's NSApp.run, as the app does). So
// they meet everything a click on the Mac meets: which window is under the
// pointer, which view hit-tests there, which view the release goes to. A
// click made with NSEvent and sent to a window skips all of that: the
// Browser window's page took every such click while it ignored every real
// one (PLAN 24).

/// Whether real clicks can be made here: this process may post the
/// pointer's events (Accessibility; minime's ssh session may) and NSApp.run
/// is handing the window server's events on (test-host, not `swift test`).
/// Elsewhere the real-click tests check nothing.
@MainActor func canPostClicks() -> Bool { AXIsProcessTrusted() && NSApp?.isRunning == true }

/// `point` in `view` (top-left origin, as a page's coordinates are) in the
/// window server's coordinates (top-left of the main screen).
@MainActor func screenPoint(_ view: NSView, _ point: NSPoint) -> CGPoint {
    let inWindow = view.convert(NSPoint(x: point.x, y: view.isFlipped ? point.y : view.bounds.height - point.y), to: nil)
    let s = view.window?.convertPoint(toScreen: inWindow) ?? inWindow
    return CGPoint(x: s.x, y: NSScreen.screens[0].frame.maxY - s.y)
}

/// The window on top at `at` (window server coordinates), as the window
/// server sees it, and its owner, layer and alpha for a click that missed.
/// Notification Center's full-screen layer passes clicks through.
@MainActor func windowOnTop(at at: CGPoint) -> (number: Int, name: String) {
    let list = CGWindowListCopyWindowInfo([.optionOnScreenOnly], kCGNullWindowID) as? [[String: Any]] ?? []
    for w in list {
        guard let b = w[kCGWindowBounds as String] as? [String: Any], let r = CGRect(dictionaryRepresentation: b as CFDictionary),
              r.contains(at), (w[kCGWindowAlpha as String] as? Double ?? 1) > 0
        else { continue }
        let owner = w[kCGWindowOwnerName as String] as? String ?? "?"
        if owner == "Notification Center" { continue }
        let layer = w[kCGWindowLayer as String] as? Int ?? 0
        let alpha = w[kCGWindowAlpha as String] as? Double ?? 1
        return (w[kCGWindowNumber as String] as? Int ?? 0, "\(owner)'s window at layer \(layer), alpha \(alpha), \(r)")
    }
    return (0, "nothing")
}

/// A real left click at `point` in `view` (top-left origin): the pointer
/// moves there, presses, releases. This process's other windows over the
/// spot (an earlier test's panel left on screen, at the rail's level above
/// the page windows') are put away first, and `view`'s window must then be
/// the one on top there. A panel an earlier test let go of mid-fade can stay
/// on screen nearly transparent, outside NSApp.windows, and take the click:
/// the real-click tests put their windows in the top middle of the screen,
/// away from the rail's edge and the pop-outs beside it.
@MainActor func realClick(_ view: NSView, at point: NSPoint) async {
    let at = screenPoint(view, point)
    let cocoa = NSPoint(x: at.x, y: NSScreen.screens[0].frame.maxY - at.y)
    for w in NSApp.windows where w !== view.window && w.isVisible && w.frame.contains(cocoa) { w.orderOut(nil) }
    func post(_ t: CGEventType, wait ms: UInt64) async {
        CGEvent(mouseEventSource: nil, mouseType: t, mouseCursorPosition: at, mouseButton: .left)?.post(tap: .cghidEventTap)
        try? await Task.sleep(nanoseconds: ms * 1_000_000)
    }
    await post(.mouseMoved, wait: 150)
    let top = windowOnTop(at: at)
    #expect(top.number == view.window?.windowNumber, "on top where the click goes: \(top.name)")
    await post(.leftMouseDown, wait: 80)
    await post(.leftMouseUp, wait: 300)
}

/// The view a click at `point` in `view` (top-left origin) goes to.
@MainActor func hitView(_ view: NSView, at point: NSPoint) -> NSView? {
    guard let root = view.window?.contentView else { return nil }
    let inWindow = view.convert(NSPoint(x: point.x, y: view.isFlipped ? point.y : view.bounds.height - point.y), to: nil)
    return root.hitTest(root.superview?.convert(inWindow, from: nil) ?? inWindow)
}
