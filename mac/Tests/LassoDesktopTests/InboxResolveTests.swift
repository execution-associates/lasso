import Foundation
import Testing

@testable import LassoDesktopKit

private func pane(_ id: String, _ status: String, _ at: Int64) -> HostPane {
    var p = HostPane(
        host: "local", host_label: "titan", pane_id: id, workspace_id: "w1", workspace_label: "Agents",
        tab_id: "w1:t1", tab_label: id, cwd: "/", agent: "claude", agent_status: status, has_agent: true, focused: false)
    p.transcript_at = at
    p.agent_id = "a-\(id)"
    return p
}

private let settle: UInt64 = 100_000_000

/// Acknowledge and Kill share one path: the entry leaves the inbox at once,
/// the card shows the next entry in Inbox order (the previous one when it
/// was the last), and the empty inbox when none remain.
@MainActor @Test func acknowledgeAndKillMoveOnInInboxOrder() async throws {
    let fleet = FleetModel()
    let all = [pane("w1:p1", "blocked", 30), pane("w1:p2", "blocked", 20), pane("w1:p3", "blocked", 10)]
    fleet.apply(panes: AllPanes(panes: all))
    let inbox = InboxModel(fleet: fleet)
    #expect(inbox.panes.map(\.pane_id) == ["w1:p1", "w1:p2", "w1:p3"])
    let actions = FixtureCardActions(chats: [:], canAcknowledge: true)
    let m = CardModel(pane: all[1], fleet: fleet, actions: actions)
    m.inbox = inbox
    var emptied = false
    m.onInboxEmpty = { emptied = true }

    m.acknowledge()
    try await Task.sleep(nanoseconds: settle)
    #expect(actions.calls == ["acknowledge"])
    #expect(inbox.panes.map(\.pane_id) == ["w1:p1", "w1:p3"])
    #expect(m.pane.pane_id == "w1:p3")

    // The last entry: the card goes back to the previous one.
    m.kill()
    m.kill()
    try await Task.sleep(nanoseconds: settle)
    #expect(actions.calls == ["acknowledge", "kill"])
    #expect(inbox.panes.map(\.pane_id) == ["w1:p1"] && m.pane.pane_id == "w1:p1")

    m.kill()
    m.kill()
    try await Task.sleep(nanoseconds: settle)
    #expect(inbox.panes.isEmpty && emptied)
}

/// A poll that still lists the dying pane, or a ui_state that has not caught
/// up with the acknowledgement, brings nothing back; something new does.
@MainActor @Test func aStalePollDoesNotResurrectAnEntry() async throws {
    let fleet = FleetModel()
    let all = [pane("w1:p1", "blocked", 30), pane("w1:p2", "done", 20)]
    fleet.apply(panes: AllPanes(panes: all))
    fleet.fallbackSince = 0
    let inbox = InboxModel(fleet: fleet)
    let actions = FixtureCardActions(chats: [:], canAcknowledge: true)
    let m = CardModel(pane: all[0], fleet: fleet, actions: actions)
    m.inbox = inbox
    m.kill()
    m.kill()
    try await Task.sleep(nanoseconds: settle)
    fleet.apply(panes: AllPanes(panes: all))
    #expect(inbox.panes.map(\.pane_id) == ["w1:p2"])
    #expect(!fleet.agents.contains { $0.pane_id == "w1:p1" })
    // Gone from the fleet: forgotten, so a new pane with that id is not hidden.
    fleet.apply(panes: AllPanes(panes: [all[1]]))
    #expect(fleet.killed.isEmpty)

    m.acknowledge()
    try await Task.sleep(nanoseconds: settle)
    fleet.apply(panes: AllPanes(panes: [all[1]]))
    #expect(inbox.panes.isEmpty)
    // A later turn is new: it is back.
    fleet.apply(panes: AllPanes(panes: [pane("w1:p2", "done", 25)]))
    #expect(inbox.panes.map(\.pane_id) == ["w1:p2"])
}

/// A draft holds Acknowledge back: no E, no keycap, and the button greyed.
@MainActor @Test func acknowledgeWaitsForTheDraft() async throws {
    let fleet = FleetModel()
    let p = pane("w1:p1", "blocked", 30)
    fleet.apply(panes: AllPanes(panes: [p]))
    let actions = FixtureCardActions(chats: [:], canAcknowledge: true)
    let m = CardModel(pane: p, fleet: fleet, actions: actions)
    m.inbox = InboxModel(fleet: fleet)
    #expect(m.canAcknowledgeNow && m.activeBindings.contains(Keys.acknowledge))
    m.composer = "  wait, one more thing"
    #expect(!m.canAcknowledgeNow && !m.activeBindings.contains(Keys.acknowledge))
    m.acknowledge()
    try await Task.sleep(nanoseconds: settle)
    #expect(actions.calls.isEmpty)
    m.composer = "   "
    #expect(m.canAcknowledgeNow)
}

/// Kill from an agent window: X arms, X kills, the window closes, and the
/// agent's inbox entry is gone too.
@MainActor @Test func killFromAWindowClosesIt() async throws {
    let fleet = FleetModel()
    let p = pane("w1:p1", "blocked", 30)
    fleet.apply(panes: AllPanes(panes: [p]))
    let actions = FixtureCardActions(chats: [:], canAcknowledge: true)
    let m = CardModel(pane: p, fleet: fleet, actions: actions)
    var closed = false
    m.onClose = { closed = true }
    m.kill()
    #expect(m.killArmed && !closed && actions.calls.isEmpty)
    m.kill()
    try await Task.sleep(nanoseconds: settle)
    #expect(actions.calls == ["kill"] && closed)
    #expect(fleet.inbox.isEmpty)
}
