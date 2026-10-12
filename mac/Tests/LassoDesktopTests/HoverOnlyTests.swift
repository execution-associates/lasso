import AppKit
import Foundation
import Testing

@testable import LassoDesktopKit

/// A rail pop-out lives under the pointer: it closes once the pointer has
/// been off the rail and every pop-out for the grace, and coming back in
/// time keeps it. One a shortcut opened waits for the pointer to come first.
@Test func aGlanceFollowsThePointer() {
    let t0 = Date(timeIntervalSince1970: 1000)
    func run(_ g: inout Glance, _ steps: [(Bool, Double)]) -> [Bool] {
        steps.map { inside, at in g.tick(inside: inside, now: t0.addingTimeInterval(at), grace: 0.3) }
    }
    var g = Glance()
    #expect(run(&g, [(false, 0), (true, 0.2), (false, 0.3), (false, 0.5), (false, 0.6)]) == [false, false, false, false, true])
    var s = Glance(byShortcut: true)
    #expect(run(&s, [(false, 0), (false, 60)]) == [false, false] && !s.entered)
    #expect(run(&s, [(true, 61), (false, 62), (false, 62.35)]) == [false, false, true] && s.entered)
}

private func pane(_ id: String, _ status: String) -> HostPane {
    HostPane(
        host: "local", host_label: "titan", pane_id: id, workspace_id: "w1", workspace_label: "Agents",
        tab_id: "w1:t1", tab_label: id, cwd: "/", agent: "claude", agent_status: status, has_agent: true, focused: false)
}

@MainActor private func coordinator() -> (AppCoordinator, HostPane, HostPane) {
    let fleet = FleetModel()
    let blocked = pane("w1:p1", "blocked")
    let idle = pane("w1:p2", "idle")
    fleet.apply(panes: AllPanes(panes: [blocked, idle]))
    return (AppCoordinator(fleet: fleet, sync: nil, fixture: nil), blocked, idle)
}

/// Pointer readings from `t`, one per tick, at `at`.
@MainActor private func move(_ app: AppCoordinator, to at: NSPoint, from t: inout Date, for seconds: Double) {
    let end = t.addingTimeInterval(seconds)
    while t <= end {
        app.pointerTick(at, now: t)
        t = t.addingTimeInterval(0.05)
    }
}

private let away = NSPoint(x: -9000, y: -9000)

/// A click on the inbox button pins nothing: the card it opened (with the
/// keyboard) closes once the pointer leaves, as after a hover. A hovered
/// agents column goes the same way.
@MainActor @Test func aClickOnTheRailPinsNothing() async {
    let (app, _, _) = coordinator()
    defer { app.closeRailPanels() }
    var t = Date(timeIntervalSince1970: 1000)
    app.railActivated(.inbox, byPointer: true)
    #expect(app.card.isOpen && app.card.isKey)
    move(app, to: app.card.panel.frame.center, from: &t, for: 1)
    #expect(app.card.isOpen)
    move(app, to: away, from: &t, for: 0.5)
    await settle { !app.card.isOpen }
    #expect(!app.card.isOpen && !app.railPanelOpen)

    app.hoverOpen(.agents)
    #expect(app.list.isOpen)
    move(app, to: app.list.panel.frame.center, from: &t, for: 0.2)
    move(app, to: away, from: &t, for: 0.5)
    #expect(!app.list.isOpen)
}

/// A row click opens the agent's own window, which stays when the pointer
/// leaves and the column closes.
@MainActor @Test func aRowsWindowOutlivesTheColumn() async {
    let (app, _, idle) = coordinator()
    defer { app.closeRailPanels() }
    var t = Date(timeIntervalSince1970: 1000)
    app.hoverOpen(.agents)
    app.list.model.open(idle)
    let w = try! #require(app.agentWindow(idle.key))
    defer {
        w.model.stopPolling()
        w.panel.orderOut(nil)
    }
    move(app, to: w.panel.frame.center, from: &t, for: 0.6)
    #expect(!app.list.isOpen && w.panel.isVisible)
}

/// The inbox shortcut's card waits with the pointer elsewhere; it goes once
/// the pointer has come onto it and left, on a click elsewhere, or on esc.
@MainActor @Test func aShortcutsInboxWaitsForThePointer() async {
    let (app, _, _) = coordinator()
    defer { app.closeRailPanels() }
    var t = Date(timeIntervalSince1970: 1000)
    app.inboxShortcut()
    #expect(app.card.isOpen)
    move(app, to: away, from: &t, for: 2)
    #expect(app.card.isOpen)
    move(app, to: app.card.panel.frame.center, from: &t, for: 0.1)
    move(app, to: away, from: &t, for: 0.5)
    await settle { !app.card.isOpen }
    #expect(!app.card.isOpen)

    app.inboxShortcut()
    #expect(app.card.isOpen)
    app.clicked(at: away, in: nil)
    await settle { !app.card.isOpen }
    #expect(!app.card.isOpen)

    app.inboxShortcut()
    #expect(app.card.isOpen)
    #expect(app.card.panel.onKey?(Keys.esc.event()!) == true)
    await settle { !app.card.isOpen }
    #expect(!app.card.isOpen)
}

extension NSRect {
    var center: NSPoint { NSPoint(x: midX, y: midY) }
}
