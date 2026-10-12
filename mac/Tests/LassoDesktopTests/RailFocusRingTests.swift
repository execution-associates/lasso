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
