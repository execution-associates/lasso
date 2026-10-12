import AppKit
import Foundation
import Testing

@testable import LassoDesktopKit

/// macOS's notifications, faked: what was posted, removed and asked.
@MainActor final class FakeNotifier: Notifier {
    var onAction: (NoteTarget, NoteAction) -> Void = { _, _ in }
    var state: PermissionState = .granted
    var asked = 0
    var posted: [UserNote] = []
    var removed: [String] = []
    var leftOver: [(id: String, target: NoteTarget)] = []

    func authorization() async -> PermissionState { state }
    func requestAuthorization() async -> PermissionState {
        asked += 1
        state = .granted
        return state
    }
    func post(_ note: UserNote) { posted.append(note) }
    func remove(_ ids: [String]) { removed += ids }
    func delivered() async -> [(id: String, target: NoteTarget)] { leftOver }
    func openSettings() {}
}

private func pane(_ id: String, _ status: String, _ at: Int64, agent: String = "claude", label: String? = nil) -> HostPane {
    var p = HostPane(
        host: "local", host_label: "titan", pane_id: id, workspace_id: "w1", workspace_label: "Agents",
        tab_id: "w1:t1", tab_label: label ?? id, cwd: "/", agent: agent, agent_status: status, has_agent: true, focused: false)
    p.transcript_at = at
    return p
}

private func chat(_ p: HostPane, _ itemsJSON: String) throws -> ChatPayload {
    try JSONDecoder().decode(
        ChatPayload.self,
        from: Data(#"{"pane_id":"\#(p.pane_id)","agent":"claude","host":"local","items":\#(itemsJSON)}"#.utf8))
}

private let askItems = #"[{"kind":"agent","id":"1","text":"Ready."},{"kind":"tool","id":"2","tool":{"state":"running","ask":{"questions":[{"question":"Deploy   to production now?","options":[{"label":"Yes"},{"label":"No"}]}]}}}]"#
private let replyItems = #"[{"kind":"user","id":"1","text":"go"},{"kind":"agent","id":"2","text":"Done.\n\n## All tests pass"}]"#

private let settle: UInt64 = 100_000_000

@MainActor private func setUp(_ panes: [HostPane], chats: [String: ChatPayload] = [:]) -> (FleetModel, InboxAlerts, FakeNotifier, FixtureCardActions) {
    let fleet = FleetModel()
    fleet.apply(panes: AllPanes(panes: panes))
    let actions = FixtureCardActions(chats: chats, canAcknowledge: true)
    let notifier = FakeNotifier()
    let alerts = InboxAlerts(fleet: fleet, inbox: InboxModel(fleet: fleet), actions: actions, notifier: notifier)
    return (fleet, alerts, notifier, actions)
}

/// The backlog is the baseline: only what arrives after it is posted, once,
/// with the agent's name, what it is and its question, under the agent's own
/// id, so a newer arrival from it replaces the older.
@MainActor @Test func onlyArrivalsAreNotified() async throws {
    let old = pane("w1:p1", "blocked", 10)
    let new = pane("w1:p2", "blocked", 20, label: "deployer")
    let (fleet, alerts, n, _) = setUp([old], chats: [new.key: try chat(new, askItems)])
    var sounds = 0
    alerts.playSound = { sounds += 1 }
    alerts.note()
    try await Task.sleep(nanoseconds: settle)
    #expect(n.posted.isEmpty && sounds == 0)

    fleet.apply(panes: AllPanes(panes: [old, new]))
    alerts.note()
    alerts.note()
    try await Task.sleep(nanoseconds: settle)
    #expect(sounds == 1)
    #expect(n.posted.count == 1)
    let note = try #require(n.posted.first)
    #expect(note.id == "agent:local/w1:p2" && note.thread == note.id)
    #expect(note.title == "deployer" && note.subtitle == "Asking" && note.body == "Deploy to production now?")
    #expect(note.category == .waiting && note.target == .entry(host: "local", pane: "w1:p2", mark: "ask:20"))

    // Asked again (a later transcript): the same id, so it replaces the last.
    fleet.apply(panes: AllPanes(panes: [old, pane("w1:p2", "blocked", 25, label: "deployer")]))
    alerts.note()
    try await Task.sleep(nanoseconds: settle)
    #expect(n.posted.map(\.id) == ["agent:local/w1:p2", "agent:local/w1:p2"])
    #expect(n.posted.last?.target == .entry(host: "local", pane: "w1:p2", mark: "ask:25"))
}

/// Before lasso has answered every read, nothing is an arrival: the first
/// reading after is the baseline.
@MainActor @Test func noBaselineUntilLassoHasAnswered() async throws {
    let (fleet, alerts, n, _) = setUp([])
    alerts.ready = { fleet.ready }
    alerts.note()
    fleet.apply(panes: AllPanes(panes: [pane("w1:p1", "blocked", 10)]))
    fleet.markHeard(.panes)
    alerts.note()
    for part: FleetModel.Part in [.bots, .uiState, .notices] { fleet.markHeard(part) }
    alerts.note()
    try await Task.sleep(nanoseconds: settle)
    #expect(n.posted.isEmpty)
    fleet.apply(panes: AllPanes(panes: [pane("w1:p1", "blocked", 10), pane("w1:p2", "blocked", 20)]))
    alerts.note()
    try await Task.sleep(nanoseconds: settle)
    #expect(n.posted.map(\.id) == ["agent:local/w1:p2"])
    // Another lasso: the fleet empties, and its first reading is a baseline again.
    fleet.clear()
    alerts.note()
    #expect(!fleet.ready)
}

/// A kind this Mac does not want neither sounds nor notifies; an entry on
/// screen sounds but is not notified; a finished turn says its last line.
@MainActor @Test func kindsAndTheCardGateNotifications() async throws {
    let p = pane("w1:p1", "idle", 5)
    let done = pane("w1:p1", "done", 30)
    let (fleet, alerts, n, _) = setUp([p], chats: [p.key: try chat(p, replyItems)])
    var sounds = 0
    alerts.playSound = { sounds += 1 }
    fleet.notify = NotifyPayload(
        kinds: [NotifyKind(id: NotifyKinds.agentFinished, label: "", description: "", default: false)],
        device: .init(id: "mac-test", label: nil, wants: [:]))
    alerts.note()
    fleet.apply(panes: AllPanes(panes: [done]))
    alerts.note()
    try await Task.sleep(nanoseconds: settle)
    #expect(sounds == 0 && n.posted.isEmpty)

    fleet.notify?.device.wants[NotifyKinds.agentFinished] = true
    var shown = true
    alerts.showing = { _ in shown }
    fleet.apply(panes: AllPanes(panes: [pane("w1:p1", "done", 40)]))
    alerts.note()
    try await Task.sleep(nanoseconds: settle)
    #expect(sounds == 1 && n.posted.isEmpty)

    shown = false
    fleet.apply(panes: AllPanes(panes: [pane("w1:p1", "done", 50)]))
    alerts.note()
    try await Task.sleep(nanoseconds: settle)
    #expect(n.posted.count == 1)
    #expect(n.posted.first?.subtitle == "Finished" && n.posted.first?.body == "All tests pass")
    #expect(n.posted.first?.category == .finished)
}

/// Acknowledged, answered or killed anywhere: the notification goes. So
/// does one the card has shown.
@MainActor @Test func aResolvedEntryWithdrawsItsNotification() async throws {
    let (fleet, alerts, n, _) = setUp([])
    alerts.note()
    let p = pane("w1:p1", "blocked", 10)
    fleet.apply(panes: AllPanes(panes: [p]))
    alerts.note()
    try await Task.sleep(nanoseconds: settle)
    #expect(n.posted.count == 1 && n.removed.isEmpty)
    var ui = UIStateSlice()
    ui.dismissed_cards = [p.key: 10]
    fleet.apply(uiState: ui)
    alerts.note()
    #expect(n.removed == ["agent:local/w1:p1"])

    fleet.apply(panes: AllPanes(panes: [pane("w1:p1", "blocked", 11)]))
    alerts.note()
    try await Task.sleep(nanoseconds: settle)
    alerts.seen(fleet.inbox[0].pane)
    #expect(n.removed == ["agent:local/w1:p1", "agent:local/w1:p1"])
}

/// A notification a previous run left stays while its entry is in the
/// inbox, and goes when it is not.
@MainActor @Test func theBaselineAdoptsLeftOverNotifications() async throws {
    let p = pane("w1:p1", "blocked", 10)
    let (_, alerts, n, _) = setUp([p])
    n.leftOver = [
        ("agent:local/w1:p1", .entry(host: "local", pane: "w1:p1", mark: "ask:10")),
        ("agent:local/w1:p9", .entry(host: "local", pane: "w1:p9", mark: "ask:3")),
        ("notice:gone", .notice("gone")),
    ]
    alerts.note()
    try await Task.sleep(nanoseconds: settle)
    #expect(n.removed.sorted() == ["agent:local/w1:p9", "notice:gone"])
    #expect(alerts.delivered.keys.sorted() == ["agent:local/w1:p1"])
}

/// macOS is asked once, on the first arrival after onboarding.
@MainActor @Test func permissionIsAskedOnTheFirstNeedAfterOnboarding() async throws {
    let (fleet, alerts, n, _) = setUp([])
    n.state = .notDetermined
    var onboarded = false
    alerts.mayAsk = { onboarded }
    alerts.note()
    fleet.apply(panes: AllPanes(panes: [pane("w1:p1", "blocked", 10)]))
    alerts.note()
    try await Task.sleep(nanoseconds: settle)
    #expect(n.asked == 0 && n.posted.isEmpty)

    onboarded = true
    fleet.apply(panes: AllPanes(panes: [pane("w1:p1", "blocked", 10), pane("w1:p2", "blocked", 20), pane("w1:p3", "blocked", 30)]))
    alerts.note()
    try await Task.sleep(nanoseconds: settle)
    #expect(n.asked == 1 && n.posted.count == 2)

    n.state = .denied
    fleet.apply(panes: AllPanes(panes: [pane("w1:p4", "blocked", 40)]))
    alerts.note()
    try await Task.sleep(nanoseconds: settle)
    #expect(n.asked == 1 && n.posted.count == 2)
}

/// A bot speaks in its own name, with its picture; its turn is its last
/// reply, and an omp bot's question is its pending request.
@MainActor @Test func botsAndNoticesSayWhatTheyAre() throws {
    let bots = try JSONDecoder().decode(BotsPayload.self, from: Data("""
        {"bots":[{"id":1,"host":"local","name":"stub","runtime":"claude","stopped":false,"state":"idle","pane_id":"w1:p9",
        "last_kind":"agent","last_text":"Checked the inbox.\\n- Nothing new","last_at":"2026-10-11T12:00:00Z"},
        {"id":2,"host":"local","name":"olivia","runtime":"omp","stopped":false,"state":"idle","pane_id":"w1:p8",
        "pending_ui":[{"id":"q1","method":"confirm","title":"Send the invoice?"}]}]}
        """.utf8))
    let fleet = FleetModel()
    fleet.apply(panes: AllPanes(panes: [pane("w1:p9", "done", 1), pane("w1:p8", "idle", 2, agent: "omp")]))
    fleet.apply(bots: bots)
    let inbox = InboxModel(fleet: fleet)
    let arrivals = inbox.arrivalList
    func note(_ bot: String) throws -> UserNote {
        let a = try #require(arrivals.first { if case .entry(let e) = $0.item { e.bot == bot } else { false } })
        guard case .entry(let e) = a.item else { throw CancellationError() }
        return InboxAlerts.note(a, bot: fleet.bots.first { $0.name == e.bot }, chat: nil, image: nil)
    }
    let stub = try note("stub")
    #expect(stub.id == "bot:local/stub" && stub.title == "stub" && stub.subtitle == "Message" && stub.body == "Nothing new")
    #expect(stub.category == .finished)
    let olivia = try note("olivia")
    #expect(olivia.subtitle == "Asking" && olivia.body == "Send the invoice?" && olivia.category == .waiting)
    #expect(olivia.target == .entry(host: "local", pane: "w1:p8", mark: "ask:q1"))

    let ping = InboxArrival(
        key: "notice:n1", kinds: [NotifyKinds.agentMessage],
        item: .notice(InboxNotice(id: "n1", at: 1, level: "info", title: "builder", detail: "PR is up", kind: NotifyKinds.agentMessage)))
    let p = InboxAlerts.note(ping, bot: nil, chat: nil, image: nil)
    #expect(p.subtitle == "Message" && p.thread == "lasso.messages" && p.body == "PR is up" && p.target == .notice("n1"))
    #expect(InboxAlerts.lastLine("x\n\n1. **Bold** start  ") == "**Bold** start")
    #expect(InboxAlerts.trimmed(String(repeating: "a", count: 300)).count == 240)
}

/// Acknowledge acts only on what the notification said; Reply sends a
/// finished turn the composer's way and never types into a pane that waits.
@MainActor @Test func notificationActions() async throws {
    let blocked = pane("w1:p1", "blocked", 10)
    let done = pane("w1:p2", "done", 20)
    let (fleet, alerts, n, actions) = setUp([blocked, done])
    var opened: [String] = []
    alerts.openEntry = { opened.append($0.pane_id) }
    alerts.openInbox = { opened.append("inbox") }

    n.onAction(.entry(host: "local", pane: "w1:p1", mark: "ask:9"), .acknowledge)
    try await Task.sleep(nanoseconds: settle)
    #expect(actions.calls.isEmpty)

    n.onAction(.entry(host: "local", pane: "w1:p1", mark: "ask:10"), .reply("yes"))
    try await Task.sleep(nanoseconds: settle)
    #expect(actions.calls.isEmpty && n.posted.last?.subtitle == "Reply not sent")

    n.onAction(.entry(host: "local", pane: "w1:p2", mark: "turn:20"), .reply("  ship it  "))
    try await Task.sleep(nanoseconds: settle)
    #expect(actions.calls == ["send"])

    n.onAction(.entry(host: "local", pane: "w1:p1", mark: "ask:10"), .acknowledge)
    try await Task.sleep(nanoseconds: settle)
    #expect(actions.calls == ["send", "acknowledge"])
    #expect(fleet.inbox.map(\.pane.pane_id) == ["w1:p2"])

    n.onAction(.entry(host: "local", pane: "w1:p2", mark: "turn:1"), .open)
    n.onAction(.entry(host: "local", pane: "w1:p1", mark: "ask:10"), .open)
    #expect(opened == ["w1:p2", "inbox"])
}

/// A click on a notification opens the inbox card on its entry, and an
/// arrival the card already shows is not notified.
@MainActor @Test func aClickOpensTheCardOnTheEntry() async throws {
    let fleet = FleetModel()
    let a = pane("w1:p1", "blocked", 10)
    let b = pane("w1:p2", "blocked", 20)
    fleet.apply(panes: AllPanes(panes: [a, b]))
    let n = FakeNotifier()
    let app = AppCoordinator(fleet: fleet, sync: nil, fixture: nil, notifier: n)
    n.onAction(.entry(host: "local", pane: "w1:p1", mark: "ask:10"), .open)
    #expect(app.card.shown?.pane.key == a.key)
    app.alerts.note()
    fleet.apply(panes: AllPanes(panes: [pane("w1:p1", "blocked", 30), b]))
    app.alerts.note()
    try await Task.sleep(nanoseconds: settle)
    #expect(n.posted.isEmpty)
    app.closeRailPanels()
}
