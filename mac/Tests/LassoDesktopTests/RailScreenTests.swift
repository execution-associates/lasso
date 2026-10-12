import AppKit
import Foundation
import Testing

@testable import LassoDesktopKit

// The rail's pop-outs go on the rail's own screen (Screens), never on
// NSScreen.main's: that follows the key window and is nil while none is
// key, as after the herdr window closes (out of full screen or not). Placed
// against `.zero` then, every pop-out opened below the screen: open but out
// of sight, so nothing showed on hover or click and the rail stayed out for
// them.

private let left = (frame: NSRect(x: 0, y: 0, width: 1512, height: 982), visible: NSRect(x: 0, y: 0, width: 1512, height: 944))
private let right = (frame: NSRect(x: 1512, y: -200, width: 2560, height: 1440), visible: NSRect(x: 1512, y: -200, width: 2560, height: 1415))

/// A window goes on the screen its anchor is on, whichever screen is "main":
/// the one holding the anchor's centre, else the one it overlaps most, else
/// the nearest. Never nothing while there is a screen.
@Test func popOutsGoOnTheRailsScreen() {
    let railOnRight = NSRect(x: 4072 - 52, y: 400, width: 52, height: 400)
    #expect(Screens.visibleFrame(for: railOnRight, in: [left, right]) == right.visible)
    let railOnLeft = NSRect(x: 1512 - 52, y: 300, width: 52, height: 400)
    #expect(Screens.visibleFrame(for: railOnLeft, in: [left, right]) == left.visible)
    // Straddling, most of it on the right.
    #expect(Screens.visibleFrame(for: NSRect(x: 1500, y: 100, width: 200, height: 50), in: [left, right]) == right.visible)
    // Off every screen (a screen unplugged): the nearest.
    #expect(Screens.visibleFrame(for: NSRect(x: -900, y: 100, width: 10, height: 10), in: [left, right]) == left.visible)
    #expect(Screens.visibleFrame(for: .zero, in: []) == nil)
}

/// Beside the rail, on its screen, whole: never at the origin's corner of
/// some other screen, never below one.
@MainActor @Test func aPopOutBesideTheRailIsOnScreen() {
    let rail = NSRect(x: 4072 - 52, y: 400, width: 52, height: 400)
    let size = Theme.List.size.size
    let o = originBesideRail(rail, size: size, visible: right.visible)
    let f = NSRect(origin: o, size: size)
    #expect(right.visible.contains(f), "\(f)")
    #expect(f.maxX <= rail.minX + Theme.Rail.panelMargin.pt)
}

/// After a Lasso window that took the keyboard (the herdr window, say) has
/// gone, every pop-out still opens where it can be seen, on hover and on
/// click, Settings included; and once they are put away the rail has
/// nothing holding it out.
@MainActor @Test func popOutsOpenOnScreenAfterAWindowWentAway() async {
    let fleet = FleetModel()
    let p = HostPane(
        host: "local", host_label: "titan", pane_id: "w1:p1", workspace_id: "w1", workspace_label: "Agents",
        tab_id: "w1:t1", tab_label: "stub", cwd: "/", agent: "claude", agent_status: "blocked", has_agent: true, focused: false)
    fleet.apply(panes: AllPanes(panes: [p]))
    let app = AppCoordinator(fleet: fleet, sync: nil, fixture: nil)
    defer { app.closeRailPanels() }
    app.rail.show()
    defer { app.rail.panel.orderOut(nil) }

    // A titled window takes the keyboard and goes, as the herdr window does.
    let w = NSWindow(
        contentRect: NSRect(x: 200, y: 200, width: 600, height: 400), styleMask: [.titled, .closable, .resizable],
        backing: .buffered, defer: false)
    w.isReleasedWhenClosed = false
    w.makeKeyAndOrderFront(nil)
    w.performClose(nil)
    #expect(!w.isVisible)

    func onScreen(_ f: NSRect) -> Bool { NSScreen.screens.contains { $0.visibleFrame.contains(f) } }
    let rail = app.rail.panel.frame
    func beside(_ f: NSRect) -> Bool { f.maxX <= rail.minX + Theme.Rail.panelMargin.pt && onScreen(f) }

    app.hoverOpen(.agents)
    #expect(app.list.isOpen && beside(app.list.panel.frame), "\(app.list.panel.frame)")
    app.railActivated(.inbox, byPointer: true)
    #expect(app.card.isOpen && beside(app.card.panel.frame), "\(app.card.panel.frame)")
    fleet.apply(bots: BotsPayload(bots: []))
    app.openSettings()
    #expect(app.isSettingsOpen && beside(app.settingsFrame), "\(app.settingsFrame)")
    app.closeRailPanels()
    app.closeSettings()
    // Each goes once its fade is over.
    await settle { !app.rail.keepOpen() }
    #expect(!app.rail.keepOpen())
}
