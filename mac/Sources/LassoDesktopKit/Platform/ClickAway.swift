import AppKit

/// A rail pop-out's life under the pointer (PLAN 16): the inbox's card or
/// panel, the agents column, a hover preview, a bot's chat. However it
/// opened, it closes once the pointer has been off the rail and every
/// pop-out for `grace`. One a shortcut opened (the pointer was elsewhere)
/// first waits for the pointer to come onto it; until then esc or a click
/// elsewhere (`ClickAway`) puts it away.
struct Glance: Equatable {
    var byShortcut = false
    /// The pointer has been on the rail or a pop-out since it opened.
    private(set) var entered = false
    private var away: Date?

    /// One pointer reading: whether to close now.
    mutating func tick(inside: Bool, now: Date, grace: Double) -> Bool {
        if inside {
            entered = true
            away = nil
            return false
        }
        if byShortcut, !entered { return false }
        if away == nil { away = now }
        guard now.timeIntervalSince(away!) >= grace else { return false }
        away = nil
        return true
    }
}

/// A click elsewhere puts a shortcut-opened pop-out away. Agent windows, the
/// herdr window, Settings and the New form are windows the user arranges,
/// and stay.
enum ClickAway {
    /// Whether a mouse-down closes the rail's panels. `window` is the Lasso
    /// window it landed in, nil for another app's (then `point`, in screen
    /// coordinates, says where). A click on one of `kept` (the panels and the
    /// rail, whose buttons open them), on a menu or popover, or on a
    /// window one of them owns, keeps them.
    @MainActor
    static func closes(point: NSPoint, window: NSWindow?, kept: [NSWindow]) -> Bool {
        if let window {
            if kept.contains(where: { $0 === window || $0 === window.parent }) { return false }
            // A context menu or a popover over a panel is the panel's.
            if window.level.rawValue >= NSWindow.Level.popUpMenu.rawValue { return false }
            return true
        }
        return !kept.contains { $0.isVisible && $0.frame.contains(point) }
    }
}

/// Watches mouse-downs while a shortcut-opened pop-out waits for the
/// pointer: in other apps (a global monitor, which needs no Accessibility
/// for mouse events) and in Lasso's own other windows (a local one). It
/// removes itself once that is over.
@MainActor
final class ClickAwayWatch {
    private var monitors: [Any] = []
    /// Whether a rail panel is open.
    var active: () -> Bool = { false }
    /// A mouse-down, with its window (nil: another app's) and screen point.
    var onClick: (NSPoint, NSWindow?) -> Void = { _, _ in }

    var watching: Bool { !monitors.isEmpty }

    static let mask: NSEvent.EventTypeMask = [.leftMouseDown, .rightMouseDown, .otherMouseDown]

    func start() {
        guard monitors.isEmpty else { return }
        if let g = NSEvent.addGlobalMonitorForEvents(matching: Self.mask, handler: { [weak self] _ in
            let at = NSEvent.mouseLocation
            MainActor.assumeIsolated { self?.saw(at, nil) }
        }) { monitors.append(g) }
        if let l = NSEvent.addLocalMonitorForEvents(matching: Self.mask, handler: { [weak self] e in
            let at = NSEvent.mouseLocation
            let w = e.window
            MainActor.assumeIsolated { self?.saw(at, w) }
            return e
        }) { monitors.append(l) }
    }

    func stop() {
        monitors.forEach(NSEvent.removeMonitor)
        monitors.removeAll()
    }

    private func saw(_ point: NSPoint, _ window: NSWindow?) {
        guard active() else { return stop() }
        onClick(point, window)
        if !active() { stop() }
    }
}
