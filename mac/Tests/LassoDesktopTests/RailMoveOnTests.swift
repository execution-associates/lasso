import AppKit
import Foundation
import Testing

@testable import LassoDesktopKit

// Resting on a rail control with nothing to show on hover (New, the
// terminal, the browser) moves on from what was open, as resting on another
// column's control does.

@MainActor private func app() -> AppCoordinator {
    let fleet = FleetModel()
    fleet.apply(panes: AllPanes(panes: [
        HostPane(
            host: "local", host_label: "titan", pane_id: "w1:p1", workspace_id: "w1", workspace_label: "Agents",
            tab_id: "w1:t1", tab_label: "agent1", cwd: "/", agent: "claude", agent_status: "idle", has_agent: true,
            focused: false),
    ]))
    fleet.apply(bots: try! JSONDecoder().decode(
        BotsPayload.self,
        from: Data(#"{"bots":[{"id":1,"host":"local","name":"bot1","runtime":"omp","stopped":false,"state":"idle"}]}"#.utf8)))
    let a = AppCoordinator(fleet: fleet, sync: nil, fixture: nil)
    a.list.hover.timerEnabled = false
    a.bots.hover.timerEnabled = false
    return a
}

@MainActor @Test(arguments: [RailModel.Item.terminal, .browser, .newAgent])
func restingOnAControlWithoutAColumnClosesTheOpenOne(_ control: RailModel.Item) {
    let a = app()
    defer { a.closeRailPanels() }
    a.hoverOpen(.bots)
    #expect(a.bots.isOpen)
    a.hoverOpen(control)
    #expect(!a.bots.isOpen, "the Bots column closed")
    a.hoverOpen(.agents)
    #expect(a.list.isOpen)
    a.hoverOpen(control)
    #expect(!a.list.isOpen, "the agents column closed")
    a.hoverOpen(.settings)
    #expect(a.settings.isOpen)
    a.hoverOpen(control)
    #expect(!a.settings.isOpen, "a hover's Settings closed")
}

/// Settings clicked open is the user's: moving over the terminal leaves it.
@MainActor @Test func settingsClickedOpenStays() {
    let a = app()
    defer { a.closeRailPanels(); a.settings.close() }
    a.openSettings(takeKey: true)
    a.hoverOpen(.terminal)
    #expect(a.settings.isOpen)
}
