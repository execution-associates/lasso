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

/// A draft holds A back: no live binding, no acknowledgement, and a focused
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

/// R starts the rename in place, in the card and in an agent window, and
/// under a lending pointer too; a bot has none.
@MainActor @Test func rRenames() throws {
    let m = inboxCard(FixtureCardActions(chats: [:], canAcknowledge: true))
    #expect(m.activeBindings.contains(Keys.rename))
    #expect(CardKeys.route(r, editing: false, pointerKeyed: true) == .rename)
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

/// L puts the cursor in the composer, a draft or not, under a lending
/// pointer too. ⏎ still goes to an empty composer, or sends.
@MainActor @Test func lFocusesTheReply() throws {
    let m = inboxCard(FixtureCardActions(chats: [:], canAcknowledge: true))
    #expect(m.activeBindings.contains(Keys.reply))
    #expect(CardKeys.route(l, editing: false, pointerKeyed: true) == .reply)
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

/// Two blocked agents in the inbox, the card on the first, with a kill that
/// takes `wait` and fails with `error`.
@MainActor private func killableCard(wait: UInt64, error: String? = nil) -> (CardModel, FixtureCardActions, FleetModel) {
    let fleet = FleetModel()
    // p1 is the newer, so first in the inbox.
    var older = agentPane("w1:p2")
    older.transcript_at = 20
    fleet.apply(panes: AllPanes(panes: [agentPane("w1:p1"), older]))
    let actions = FixtureCardActions(chats: [:], canAcknowledge: true)
    actions.killWait = wait
    actions.killError = error
    let m = CardModel(pane: agentPane("w1:p1"), fleet: fleet, actions: actions)
    m.inbox = InboxModel(fleet: fleet)
    return (m, actions, fleet)
}

/// A kill in flight shows its wait: Kill is off, and every key but paging
/// waits. Once lasso answers, the card moves on.
@MainActor @Test func killWaitsThenMovesOn() async throws {
    let (m, actions, _) = killableCard(wait: 400_000_000)
    let first = m.pane.key
    m.perform(.kill)
    #expect(!m.killing && m.killArmed)
    m.perform(.kill)
    try await Task.sleep(nanoseconds: 50_000_000)
    #expect(m.killing && m.killingKey == first && actions.calls == ["kill"])
    #expect(!m.activeBindings.contains(Keys.kill) && !m.canAcknowledgeNow)
    // X, A and L wait; a third X does not arm another kill.
    for a: CardKeyAction in [.kill, .acknowledge, .reply, .rename] { #expect(m.perform(a)) }
    m.kill()
    #expect(!m.killArmed && !m.renaming && m.focusRequest == 0 && actions.calls == ["kill"])
    try await Task.sleep(nanoseconds: 600_000_000)
    #expect(!m.killing && m.killingKey == nil)
    #expect(m.pane.pane_id == "w1:p2")
}

/// A kill lasso refuses clears the wait and says why; J/K page meanwhile,
/// and the wait belongs to the agent being killed, not the card.
@MainActor @Test func aFailedKillSaysWhy() async throws {
    let (m, actions, fleet) = killableCard(wait: 400_000_000, error: "the pane is gone")
    m.kill()
    m.kill()
    try await Task.sleep(nanoseconds: 50_000_000)
    #expect(m.killing)
    #expect(m.perform(.next))
    #expect(m.pane.pane_id == "w1:p2" && !m.killing && m.activeBindings.contains(Keys.kill))
    #expect(m.perform(.previous))
    #expect(m.pane.pane_id == "w1:p1" && m.killing)
    try await Task.sleep(nanoseconds: 600_000_000)
    #expect(!m.killing && m.killingKey == nil && actions.calls == ["kill"])
    #expect(m.notice == "Kill failed: the pane is gone" && m.killFailed == "Kill failed: the pane is gone")
    #expect(m.pane.pane_id == "w1:p1" && fleet.killed.isEmpty)
    #expect(m.activeBindings.contains(Keys.kill))
    // The next X starts over.
    m.kill()
    #expect(m.killArmed && m.killFailed == nil)
}

/// Keys work but are not drawn: every key of every surface, pressed, is
/// routed to its own action, and the bot chat answers its set.
@Test func everySurfacesKeysAreRouted() {
    for set in [CardKeys.cardSet, CardKeys.windowSet, CardKeys.previewSet, CardKeys.chatSet, CardKeys.hoverSet] {
        for b in set {
            let want = CardKeys.bound.first { $0.binding == b }?.action
            #expect(want != nil && CardKeys.route(b.event()!, editing: false, among: set) == want, "\(b.id)")
        }
    }
    #expect(CardKeys.route(Keys.stop.event()!, editing: true, among: CardKeys.chatSet) == .stop)
    #expect(CardKeys.route(Keys.reply.event()!, editing: true, among: CardKeys.chatSet) == nil)
    #expect(CardKeys.route(Keys.kill.event()!, editing: false, among: CardKeys.chatSet) == .unbound)
}

/// Under a lending pointer (a hover-opened inbox), R and L make the keyboard
/// the card's, as a click would, and then act: R starts the rename (its field
/// takes the caret as it appears), L asks the reply for the caret. The pointer
/// leaving afterwards keeps the keyboard with the card, and a key typed into
/// the field is text.
@MainActor @Test func rAndLUnderThePointerTakeTheKeyboard() throws {
    let p = agentPane()
    let fleet = FleetModel()
    fleet.apply(panes: AllPanes(panes: [p]))
    let c = CardController(fleet: fleet, actions: FixtureCardActions(chats: [:], canAcknowledge: true))
    c.open(p, takeKey: false)
    defer { c.close() }
    let m = try #require(c.model)

    c.takeKeyForPointer()
    #expect(c.pointerKeyed && c.panel.isKeyWindow && m.keyMode == .lent)
    #expect(c.panel.onKey?(r) == true)
    #expect(m.renaming && !c.pointerKeyed && m.keyMode == .full)
    c.pointerLeft()
    #expect(c.panel.isKeyWindow && m.renaming && m.keyMode == .full)
    m.cancelRename()

    // Lent again on a later visit, L goes to the reply the same way.
    c.panel.resignKey()
    #expect(!c.panel.isKeyWindow && m.keyMode == .none)
    c.takeKeyForPointer()
    #expect(c.pointerKeyed && m.keyMode == .lent)
    let asked = m.focusRequest
    #expect(c.panel.onKey?(l) == true)
    #expect(m.focusRequest == asked + 1 && !c.pointerKeyed && m.keyMode == .full)
    c.pointerLeft()
    #expect(c.panel.isKeyWindow && m.keyMode == .full)
    // In the field, an l is text: the router leaves it to the field.
    m.composerFocused = true
    #expect(c.panel.onKey?(l) == false)
    m.composerFocused = false

    // A bot's card has no rename: R under the pointer hands the keyboard back.
    c.panel.resignKey()
    c.pointerLeft()
    c.takeKeyForPointer()
    m.fleet.apply(bots: try JSONDecoder().decode(BotsPayload.self, from: Data("""
        {"bots":[{"id":1,"host":"local","name":"stub","runtime":"claude","stopped":false,"notify":true,
        "state":"idle","pane_id":"\(p.pane_id)","last_kind":"agent","last_at":"2026-10-11T12:00:00Z"}]}
        """.utf8)))
    #expect(c.pointerKeyed && m.isBot)
    #expect(c.panel.onKey?(r) == true)
    #expect(!m.renaming && !c.pointerKeyed && !c.panel.isKeyWindow)
}
