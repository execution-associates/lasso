import AppKit
import Foundation
import Testing

@testable import LassoDesktopKit

/// The inbox card's chat button: the inbox goes and the agent's own chat
/// window opens; pressed again from a later card, the same window comes
/// forward rather than a second one.
@MainActor @Test func theCardsChatButtonOpensTheAgentsWindow() throws {
    let p = HostPane(
        host: "local", host_label: "titan", pane_id: "w1:p1", workspace_id: "w1", workspace_label: "Agents",
        tab_id: "w1:t1", tab_label: "agent1", cwd: "/", agent: "claude", agent_status: "blocked", has_agent: true,
        focused: false)
    let fleet = FleetModel()
    fleet.apply(panes: AllPanes(panes: [p]))
    let a = AppCoordinator(fleet: fleet, sync: nil, fixture: nil)
    defer {
        a.closeRailPanels()
        for w in a.windows.values { w.close() }
    }
    a.openCard(p, takeKey: false)
    let m = try #require(a.card.model)
    #expect(m.canOpenWindow)
    m.openWindow()
    #expect(a.card.shown == nil, "the inbox went")
    let w = try #require(a.windows[p.key], "the agent's window opened")
    a.openCard(p, takeKey: false)
    try #require(a.card.model).openWindow()
    #expect(a.windows.count == 1 && a.windows[p.key] === w, "the same window, brought forward")
}
