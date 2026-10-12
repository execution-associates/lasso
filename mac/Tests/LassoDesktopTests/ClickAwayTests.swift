import AppKit
import Foundation
import Testing

@testable import LassoDesktopKit

private func agent(_ id: String) -> HostPane {
    HostPane(
        host: "local", host_label: "titan", pane_id: id, workspace_id: "w1", workspace_label: "Agents",
        tab_id: "w1:t1", tab_label: id, cwd: "/", agent: "claude", agent_status: "done", has_agent: true, focused: false)
}

/// Wait (suspended, so the panels' close tasks run) until `done` or 2 s.
@MainActor func settle(_ done: () -> Bool) async {
    let end = Date().addingTimeInterval(2)
    while !done(), Date() < end { try? await Task.sleep(nanoseconds: 20_000_000) }
}

/// A click on the rail or one of its panels (or a menu over one, or a window
/// one owns) keeps them; anywhere else, in another app or another Lasso
/// window, puts them away.
@MainActor @Test func aClickElsewherePutsTheRailsPanelsAway() {
    func window(_ r: NSRect, level: NSWindow.Level = .normal) -> NSWindow {
        let w = NSWindow(contentRect: r, styleMask: [.borderless], backing: .buffered, defer: false)
        w.level = level
        return w
    }
    let rail = window(NSRect(x: 1800, y: 300, width: 60, height: 400))
    let card = window(NSRect(x: 1200, y: 200, width: 500, height: 500))
    let agentWindow = window(NSRect(x: 100, y: 100, width: 400, height: 400))
    let menu = window(NSRect(x: 1300, y: 300, width: 100, height: 100), level: .popUpMenu)
    let owned = window(NSRect(x: 1300, y: 300, width: 100, height: 100))
    card.addChildWindow(owned, ordered: .above)
    card.orderFrontRegardless()
    rail.orderFrontRegardless()
    defer { [rail, card, agentWindow, menu, owned].forEach { $0.orderOut(nil) } }
    let kept = [rail, card]
    let far = NSPoint(x: 20, y: 20)
    #expect(!ClickAway.closes(point: far, window: card, kept: kept))
    #expect(!ClickAway.closes(point: far, window: rail, kept: kept))
    #expect(!ClickAway.closes(point: far, window: menu, kept: kept))
    #expect(!ClickAway.closes(point: far, window: owned, kept: kept))
    #expect(ClickAway.closes(point: far, window: agentWindow, kept: kept))
    // Another app's window: by where it landed.
    #expect(ClickAway.closes(point: far, window: nil, kept: kept))
    #expect(!ClickAway.closes(point: NSPoint(x: 1300, y: 400), window: nil, kept: kept))
    #expect(!ClickAway.closes(point: NSPoint(x: 1820, y: 500), window: nil, kept: kept))
    card.orderOut(nil)
    #expect(ClickAway.closes(point: NSPoint(x: 1300, y: 400), window: nil, kept: kept))
}

/// The inbox's card goes on a click elsewhere and when the herdr window
/// opens; an agent's own window stays open and where it was through both,
/// and a reply typed in the card comes back with that agent's next card.
@MainActor @Test func agentWindowsSurviveWhatPutsTheRailsPanelsAway() async {
    let fleet = FleetModel()
    let a = agent("w1:p1")
    let b = agent("w1:p2")
    fleet.apply(panes: AllPanes(panes: [a, b]))
    let app = AppCoordinator(fleet: fleet, sync: nil, fixture: nil)
    app.openWindow(a)
    let w = try! #require(app.agentWindow(a.key))
    defer {
        w.model.stopPolling()
        w.panel.orderOut(nil)
        app.closeRailPanels()
    }
    let level = w.panel.level
    let behavior = w.panel.collectionBehavior
    let frame = w.panel.frame

    app.openCard(b, takeKey: false)
    #expect(app.card.isOpen && app.railPanelOpen)
    // On the rail: kept (its buttons toggle the panels themselves).
    app.clicked(at: app.rail.panel.frame.origin, in: app.rail.panel)
    #expect(app.card.isOpen)
    app.card.model?.composer = "half a reply"
    // In another app.
    app.clicked(at: NSPoint(x: -5000, y: -5000), in: nil)
    await settle { !app.card.isOpen }
    #expect(!app.card.isOpen)
    #expect(w.panel.isVisible && app.agentWindow(a.key) != nil)

    app.openCard(b, takeKey: false)
    #expect(app.card.isOpen && app.card.model?.composer == "half a reply")
    // In the agent's own window: the card goes, the window stays.
    app.clicked(at: NSPoint(x: w.panel.frame.midX, y: w.panel.frame.midY), in: w.panel)
    await settle { !app.card.isOpen }
    #expect(!app.card.isOpen)
    #expect(w.panel.isVisible)

    // The herdr window comes up alone: the card goes, the agent window stays.
    app.openCard(b, takeKey: false)
    #expect(app.card.isOpen)
    app.openTerminal()
    await settle { !app.card.isOpen }
    #expect(!app.card.isOpen && !app.railPanelOpen)
    app.openCard(b, takeKey: false)
    #expect(app.card.isOpen)
    app.showInHerdr(b)
    await settle { !app.card.isOpen }
    #expect(!app.card.isOpen)
    #expect(w.panel.isVisible && app.agentWindow(a.key) != nil)
    #expect(w.panel.level == level && w.panel.collectionBehavior == behavior && w.panel.frame == frame)
}
