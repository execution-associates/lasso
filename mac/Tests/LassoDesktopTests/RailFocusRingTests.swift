import AppKit
import Foundation
import Testing

@testable import LassoDesktopKit

// The rail's focus ring is the keyboard's: on after a key moves the focus,
// off once the rail hides or loses the keyboard, so it never follows the
// pointer from control to control.

@MainActor @Test func theFocusRingGoesWhenTheRailHides() {
    let m = RailModel(fleet: FleetModel())
    m.setExpanded(true, animated: false)
    m.moveFocus(1)
    #expect(m.focusVisible)
    m.setExpanded(false, animated: false)
    #expect(!m.focusVisible)
}

@MainActor @Test func theFocusRingGoesWhenTheRailLosesTheKeyboard() async {
    let a = AppCoordinator(fleet: FleetModel(), sync: nil, fixture: nil)
    defer { a.closeRailPanels() }
    a.rail.summon()
    #expect(a.rail.model.focusVisible)
    // What AppKit posts as the keyboard goes to another window or app.
    NotificationCenter.default.post(name: NSWindow.didResignKeyNotification, object: a.rail.panel)
    #expect(!a.rail.model.focusVisible)
}

/// A pop-out that counts as open with no window the user can see (macOS
/// left it on another Space, or it was ordered out under it) holds nothing:
/// the rail does not stay out for it, and the next pointer reading closes it.
@MainActor @Test func aPopOutOpenOutOfSightHoldsNothing() {
    let fleet = FleetModel()
    fleet.apply(panes: AllPanes(panes: [
        HostPane(
            host: "local", host_label: "titan", pane_id: "w1:p1", workspace_id: "w1", workspace_label: "Agents",
            tab_id: "w1:t1", tab_label: "agent1", cwd: "/", agent: "claude", agent_status: "idle", has_agent: true,
            focused: false),
    ]))
    let a = AppCoordinator(fleet: fleet, sync: nil, fixture: nil)
    defer { a.closeRailPanels() }
    a.list.hover.timerEnabled = false
    a.hoverOpen(.agents)
    #expect(a.list.isOpen && a.rail.keepOpen(), "open and seen: it holds the rail")
    a.list.panel.orderOut(nil)
    #expect(!a.rail.keepOpen(), "out of sight: it does not")
    a.pointerTick(NSPoint(x: a.rail.panel.frame.midX, y: a.rail.panel.frame.midY), now: Date())
    #expect(!a.list.isOpen, "and it closes")
}
