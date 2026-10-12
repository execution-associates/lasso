import AppKit
import Foundation
import Testing

@testable import LassoDesktopKit

private let settle: UInt64 = 100_000_000

private func agentPane(_ id: String = "w1:p1", status: String = "blocked") -> HostPane {
    var p = HostPane(
        host: "local", host_label: "titan", pane_id: id, workspace_id: "w1", workspace_label: "Agents",
        tab_id: "w1:t1", tab_label: "stub", cwd: "/", agent: "claude", agent_status: status, has_agent: true, focused: false)
    p.transcript_at = 30
    p.agent_id = "a-\(id)"
    return p
}

@MainActor private func inboxCard(_ actions: FixtureCardActions) -> CardModel {
    let p = agentPane()
    let fleet = FleetModel()
    fleet.apply(panes: AllPanes(panes: [p]))
    let m = CardModel(pane: p, fleet: fleet, actions: actions)
    m.inbox = InboxModel(fleet: fleet)
    return m
}

private var a: NSEvent { key("a", 0) }
private var e: NSEvent { key("e", 14) }
private var r: NSEvent { key("r", 15) }
private var l: NSEvent { key("l", 37) }
private var u: NSEvent { key("u", 32) }

/// A acknowledges in the inbox card (PLAN 13.7a); E does nothing now.
@MainActor @Test func aAcknowledgesAndEIsFree() async throws {
    let actions = FixtureCardActions(chats: [:], canAcknowledge: true)
    let m = inboxCard(actions)
    #expect(Keys.acknowledge.legend == "A" && m.activeBindings.contains(Keys.acknowledge))
    #expect(CardKeys.route(e, editing: false, pointerKeyed: false) == .unbound)
    #expect(CardKeys.route(e, editing: false, pointerKeyed: true) == .handBack)
    #expect(!m.perform(try #require(CardKeys.route(e, editing: false, pointerKeyed: false))))
    try await Task.sleep(nanoseconds: settle)
    #expect(actions.calls.isEmpty)
    #expect(m.perform(try #require(CardKeys.route(a, editing: false, pointerKeyed: false))))
    try await Task.sleep(nanoseconds: settle)
    #expect(actions.calls == ["acknowledge"])
}

/// A draft holds A back: no keycap, no acknowledgement, and a focused
/// composer types it.
@MainActor @Test func aWaitsForTheDraft() async throws {
    let actions = FixtureCardActions(chats: [:], canAcknowledge: true)
    let m = inboxCard(actions)
    m.composer = "one more thing"
    #expect(!m.activeBindings.contains(Keys.acknowledge))
    #expect(CardKeys.route(a, editing: true, pointerKeyed: false) == nil)
    m.perform(try #require(CardKeys.route(a, editing: false, pointerKeyed: false)))
    try await Task.sleep(nanoseconds: settle)
    #expect(actions.calls.isEmpty)
}

/// R starts the rename in place, in the card and in an agent window; a bot
/// has none, and the key is not lent to a hovering pointer.
@MainActor @Test func rRenames() throws {
    let m = inboxCard(FixtureCardActions(chats: [:], canAcknowledge: true))
    #expect(m.activeBindings.contains(Keys.rename))
    #expect(CardKeys.route(r, editing: false, pointerKeyed: true) == .handBack)
    #expect(CardKeys.route(r, editing: true, pointerKeyed: false) == nil)
    #expect(m.perform(try #require(CardKeys.route(r, editing: false, pointerKeyed: false))))
    #expect(m.renaming)
    m.cancelRename()
    #expect(CardKeys.route(r, editing: false, among: CardKeys.windowSet) == .rename)
    #expect(CardKeys.route(r, editing: false, among: CardKeys.previewSet) == .rename)

    let fleet = FleetModel()
    let p = agentPane("w1:p9", status: "done")
    fleet.apply(panes: AllPanes(panes: [p]))
    fleet.apply(bots: try JSONDecoder().decode(BotsPayload.self, from: Data("""
        {"bots":[{"id":1,"host":"local","name":"stub","runtime":"claude","stopped":false,"notify":true,
        "state":"idle","pane_id":"w1:p9","last_kind":"agent","last_at":"2026-10-11T12:00:00Z"}]}
        """.utf8)))
    let bot = CardModel(pane: p, fleet: fleet, actions: FixtureCardActions(chats: [:], canAcknowledge: true))
    #expect(bot.isBot && !bot.canRename && !bot.activeBindings.contains(Keys.rename))
    #expect(!bot.perform(.rename) && !bot.renaming)
}

/// L puts the cursor in the composer, a draft or not; it is not lent to a
/// hovering pointer. ⏎ still goes to an empty composer, or sends.
@MainActor @Test func lFocusesTheReply() throws {
    let m = inboxCard(FixtureCardActions(chats: [:], canAcknowledge: true))
    #expect(m.activeBindings.contains(Keys.reply))
    #expect(CardKeys.route(l, editing: false, pointerKeyed: true) == .handBack)
    #expect(m.perform(try #require(CardKeys.route(l, editing: false, pointerKeyed: false))))
    #expect(m.focusRequest == 1)
    m.composer = "half a thought"
    m.perform(.reply)
    #expect(m.focusRequest == 2)
    // Once there, an l is text.
    #expect(CardKeys.route(l, editing: true, pointerKeyed: false) == nil)
    m.composer = ""
    m.perform(.send)
    #expect(m.focusRequest == 3)
    #expect(CardKeys.route(l, editing: false, among: CardKeys.windowSet) == .reply)
}

/// U attaches, in the card, an agent window and a preview; A attaches nowhere.
@MainActor @Test func uAttaches() throws {
    let m = inboxCard(FixtureCardActions(chats: [:], canAcknowledge: true))
    var chose = 0
    m.chooseFiles = {
        chose += 1
        return []
    }
    #expect(Keys.attach.legend == "U" && m.activeBindings.contains(Keys.attach))
    #expect(CardKeys.route(u, editing: false, pointerKeyed: false) == .attach)
    #expect(CardKeys.route(u, editing: false, pointerKeyed: true) == .handBack)
    m.perform(try #require(CardKeys.route(u, editing: false, pointerKeyed: false)))
    #expect(chose == 1)
    for set in [CardKeys.windowSet, CardKeys.previewSet] {
        #expect(CardKeys.route(u, editing: false, among: set) == .attach)
        // A window has no inbox entry to acknowledge.
        #expect(CardKeys.route(a, editing: false, among: set) == .unbound)
    }
}

/// Every key is bound once: no two bindings answer the same key.
@Test func bindingsAreDistinct() {
    let ids = Keys.all.map(\.id)
    #expect(Set(ids).count == ids.count)
    let chars = Keys.all.compactMap(\.character)
    #expect(Set(chars).count == chars.count)
}
