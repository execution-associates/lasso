import AppKit
import Foundation
import Testing

@testable import LassoDesktopKit

// A column's rows stay where they are while it is open (PLAN 16): a fleet
// update reorders nothing under the pointer; the next open shows the live
// order again.

@Test func aHeldOrderKeepsRowsAndAppendsNewOnes() {
    var o = FrozenOrder()
    #expect(o.apply(["b", "a"], key: { $0 }) == ["b", "a"], "thawed: the live order")
    o.freeze(["a", "b", "c"])
    #expect(o.apply(["c", "b", "a"], key: { $0 }) == ["a", "b", "c"])
    #expect(o.apply(["y", "c", "x", "a"], key: { $0 }) == ["a", "c", "y", "x"], "b went; y and x came, in their order")
    o.thaw()
    #expect(o.apply(["c", "b", "a"], key: { $0 }) == ["c", "b", "a"])
}

private func agent(_ i: Int, at: Int64, status: String = "done") -> HostPane {
    var p = HostPane(
        host: "local", host_label: "titan", pane_id: "w1:p\(i)", workspace_id: "w1", workspace_label: "Agents",
        tab_id: "w1:t\(i)", tab_label: "agent\(i)", cwd: "/", agent: "claude", agent_status: status, has_agent: true,
        focused: false)
    p.transcript_at = at
    return p
}

private func bots(_ states: [(String, String)]) -> BotsPayload {
    let json = states.enumerated().map { i, b in
        #"{"id":\#(i + 1),"host":"local","name":"\#(b.0)","runtime":"omp","stopped":false,"state":"\#(b.1)"}"#
    }
    return try! JSONDecoder().decode(BotsPayload.self, from: Data(("{\"bots\":[" + json.joined(separator: ",") + "]}").utf8))
}

@MainActor private func spin(_ seconds: Double) async {
    let end = Date().addingTimeInterval(seconds)
    while Date() < end { try? await Task.sleep(nanoseconds: 10_000_000) }
}

/// Open, the agents column keeps its order through a fleet update that
/// reorders the fleet (newest first); a new agent comes last; closed and
/// opened again, it shows the live order.
@MainActor @Test func theAgentsColumnHoldsItsOrderWhileOpen() async {
    let fleet = FleetModel()
    fleet.apply(panes: AllPanes(panes: [agent(1, at: 300), agent(2, at: 200), agent(3, at: 100)]))
    let a = AppCoordinator(fleet: fleet, sync: nil, fixture: nil)
    defer { a.closeRailPanels() }
    a.list.hover.timerEnabled = false
    func rows() -> [String] { a.list.model.groups.flatMap(\.agents).map(\.pane_id) }
    a.hoverOpen(.agents)
    #expect(rows() == ["w1:p1", "w1:p2", "w1:p3"])
    // agent3 speaks last; agent4 starts.
    fleet.apply(panes: AllPanes(panes: [agent(1, at: 300), agent(2, at: 200), agent(3, at: 900), agent(4, at: 950)]))
    #expect(fleet.agents.map(\.pane_id) == ["w1:p4", "w1:p3", "w1:p1", "w1:p2"], "the live order moved")
    #expect(rows() == ["w1:p1", "w1:p2", "w1:p3", "w1:p4"], "the open column did not")
    a.list.close()
    await spin(0.3)
    a.hoverOpen(.agents)
    #expect(rows() == ["w1:p4", "w1:p3", "w1:p1", "w1:p2"], "opened again: the live order")
}

/// The Bots column alike: a bot that starts asking does not jump to the top
/// while the column is open.
@MainActor @Test func theBotsColumnHoldsItsOrderWhileOpen() async {
    let fleet = FleetModel()
    fleet.apply(bots: bots([("alpha", "idle"), ("beta", "idle")]))
    let a = AppCoordinator(fleet: fleet, sync: nil, fixture: nil)
    defer { a.closeRailPanels() }
    a.bots.hover.timerEnabled = false
    a.hoverOpen(.bots)
    #expect(a.bots.model.bots.map(\.name) == ["alpha", "beta"])
    fleet.apply(bots: bots([("alpha", "idle"), ("beta", "working")]))
    #expect(a.bots.model.bots.map(\.name) == ["alpha", "beta"])
    a.bots.close()
    await spin(0.3)
    a.hoverOpen(.bots)
    #expect(a.bots.model.bots.map(\.name) == ["beta", "alpha"])
}
