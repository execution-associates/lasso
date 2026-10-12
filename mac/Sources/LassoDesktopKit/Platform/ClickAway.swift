import AppKit

/// The panels that open off the rail (the inbox's card or panel, the agents
/// column, a hover preview, a bot's chat) are glances: a click anywhere else
/// puts them away (PLAN 16). Agent windows, the herdr window, Settings and
/// the New Agent form are windows the user arranges, and stay.
enum ClickAway {
    /// Whether a mouse-down closes the rail's panels. `window` is the Lasso
    /// window it landed in, nil for another app's (then `point`, in screen
    /// coordinates, says where). A click on one of `kept` (the panels and the
    /// rail, whose own buttons toggle them), on a menu or popover, or on a
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

/// Watches mouse-downs while a rail panel is open: in other apps (a global
/// monitor, which needs no Accessibility for mouse events) and in Lasso's
/// own other windows (a local one). It removes itself once nothing is open.
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
