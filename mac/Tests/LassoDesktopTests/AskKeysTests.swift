import AppKit
import Foundation
import Testing

@testable import LassoDesktopKit

private let settle: UInt64 = 150_000_000

private func blocked(_ id: String, at: Int64) -> HostPane {
    var p = HostPane(
        host: "local", host_label: "titan", pane_id: id, workspace_id: "w1", workspace_label: "Agents",
        tab_id: "w1:t1", tab_label: "agent-\(id.suffix(2))", cwd: "/", agent: "claude", agent_status: "blocked",
        has_agent: true, focused: false)
    p.transcript_at = at
    p.agent_id = "a-\(id)"
    return p
}

/// An open AskUserQuestion with three choices; `questions` of them in the ask.
private func askChat(_ p: HostPane, recommended: Int? = nil, questions: Int = 1) throws -> ChatPayload {
    let rec = recommended.map { #","recommended":\#($0)"# } ?? ""
    let q = #"{"header":"Deploy","question":"Ship it?","options":[{"label":"Yes"},{"label":"Maybe","description":"after the tests"},{"label":"No"}]\#(rec)}"#
    let qs = Array(repeating: q, count: questions).joined(separator: ",")
    return try JSONDecoder().decode(
        ChatPayload.self,
        from: Data(
            #"{"pane_id":"\#(p.pane_id)","agent":"claude","host":"local","items":[{"kind":"tool","id":"t-\#(p.pane_id)","tool":{"name":"AskUserQuestion","state":"running","ask":{"questions":[\#(qs)]}}}]}"#
                .utf8))
}

/// Two agents blocked on questions, the inbox card on the newer one.
@MainActor private func askingInbox(recommended: Int? = nil, questions: Int = 1) async throws -> (CardModel, FixtureCardActions) {
    let first = blocked("w1:p1", at: 30), second = blocked("w1:p2", at: 20)
    let fleet = FleetModel()
    fleet.apply(panes: AllPanes(panes: [first, second]))
    let actions = FixtureCardActions(
        chats: [
            first.key: try askChat(first, recommended: recommended, questions: questions),
            second.key: try askChat(second),
        ], canAcknowledge: true)
    let m = CardModel(pane: first, fleet: fleet, actions: actions)
    m.inbox = InboxModel(fleet: fleet)
    await m.load()
    return (m, actions)
}

private var up: NSEvent { Keys.choiceUp.event()! }
private var down: NSEvent { Keys.choiceDown.event()! }
private var enter: NSEvent { Keys.send.event()! }
private var enterHeld: NSEvent {
    NSEvent.keyEvent(
        with: .keyDown, location: .zero, modifierFlags: [], timestamp: 0, windowNumber: 0, context: nil,
        characters: "\r", charactersIgnoringModifiers: "\r", isARepeat: true, keyCode: 36)!
}

/// ↑/↓ move the highlight through the choices, stopping at either end, and
/// ⏎ answers with the highlighted one. The inbox card then moves on to the
/// next entry, saying what was answered, and acknowledges nothing.
@MainActor @Test func arrowsChooseAndReturnAnswers() async throws {
    let (m, actions) = try await askingInbox()
    #expect(m.hasChoices && m.selectedStep == 0 && m.answersOnReturn)
    #expect(m.question?.text == "Ship it?" && m.question?.header == "Deploy")
    #expect(m.capturedItem == "t-w1:p1")
    #expect(m.activeBindings.isSuperset(of: [Keys.choiceUp, Keys.choiceDown]))
    #expect(CardKeys.route(down, editing: false, pointerKeyed: false) == .choiceDown)
    #expect(CardKeys.route(up, editing: false, pointerKeyed: false) == .choiceUp)
    // A focused composer keeps its arrows.
    #expect(CardKeys.route(down, editing: true, pointerKeyed: false) == nil)

    #expect(m.perform(.choiceUp) && m.selectedStep == 0)
    for _ in 0..<5 { m.perform(.choiceDown) }
    #expect(m.selectedStep == 2)
    m.perform(.choiceUp)
    #expect(m.selectedStep == 1 && m.chosen)

    #expect(m.perform(try #require(CardKeys.route(enter, editing: false, pointerKeyed: false))))
    try await Task.sleep(nanoseconds: settle)
    #expect(actions.answered == [1])
    #expect(!actions.calls.contains("acknowledge") && !actions.calls.contains("send"))
    #expect(m.pane.pane_id == "w1:p2")
    #expect(m.notice?.hasPrefix("Answered") == true && m.notice?.hasSuffix(": Maybe") == true)
    // The next question starts from its own default, nothing chosen.
    #expect(m.highlighted == nil && !m.chosen && m.selectedStep == 0)
}

/// The agent's recommendation is where the highlight starts.
@MainActor @Test func theRecommendationIsHighlightedFirst() async throws {
    let (m, actions) = try await askingInbox(recommended: 2)
    #expect(m.selectedStep == 2)
    m.perform(.send)
    try await Task.sleep(nanoseconds: settle)
    #expect(actions.answered == [2])
}

/// A reply being written keeps ⏎ for the reply; the arrows still choose.
@MainActor @Test func aDraftKeepsReturnForTheReply() async throws {
    let (m, actions) = try await askingInbox()
    m.composer = "wait, one thing"
    #expect(!m.answersOnReturn)
    m.perform(.send)
    try await Task.sleep(nanoseconds: settle)
    #expect(actions.answered.isEmpty && actions.sent == ["wait, one thing"])
}

/// A multi-question ask stays on its card after the first answer (its dialog
/// shows the next question), and a refused answer stays too.
@MainActor @Test func onlyAWholeAnswerMovesOn() async throws {
    let (multi, a1) = try await askingInbox(questions: 2)
    #expect(multi.capturedItem == nil)
    multi.perform(.send)
    try await Task.sleep(nanoseconds: settle)
    #expect(a1.answered == [0] && multi.pane.pane_id == "w1:p1")

    let (m, a2) = try await askingInbox()
    a2.answerOutcome = "refused"
    m.perform(.send)
    try await Task.sleep(nanoseconds: settle)
    #expect(a2.answered == [0] && m.pane.pane_id == "w1:p1" && m.notice?.hasPrefix("Refused") == true)
}

/// While the card holds a question, a keyboard the pointer lent takes the
/// choice keys too (↑/↓, 1–9, ⏎, →); without one they hand it back.
@Test func theChoiceKeysAreLentWithAQuestion() {
    for e in [up, down, enter, Keys.takeStep.event()!, key("2", 19)] {
        #expect(CardKeys.route(e, editing: false, pointerKeyed: true, choosing: false) == .handBack)
        #expect(CardKeys.route(e, editing: false, pointerKeyed: true, choosing: true) != .handBack)
    }
    #expect(CardKeys.route(down, editing: false, pointerKeyed: true, choosing: true) == .choiceDown)
    #expect(CardKeys.route(key("2", 19), editing: false, pointerKeyed: true, choosing: true) == .digit(2))
    // J/K still page past the question.
    #expect(CardKeys.route(key("j", 38), editing: false, pointerKeyed: true, choosing: true) == .next)
    #expect(CardKeys.route(key("u", 32), editing: false, pointerKeyed: true, choosing: true) == .handBack)
}

/// Under a lent keyboard, a ⏎ with nothing chosen only picks the highlighted
/// choice (a pointer resting on the inbox must not answer for whoever types
/// elsewhere); the next ⏎ answers. A chosen choice answers on the first ⏎,
/// and a held ⏎ answers nothing.
@MainActor @Test func aLentReturnAnswersOnlyAChosenChoice() async throws {
    let p = blocked("w1:p1", at: 30)
    let fleet = FleetModel()
    fleet.apply(panes: AllPanes(panes: [p, blocked("w1:p2", at: 20)]))
    let actions = FixtureCardActions(chats: [p.key: try askChat(p)], canAcknowledge: true)
    let c = CardController(fleet: fleet, actions: actions)
    c.inbox = InboxModel(fleet: fleet)
    c.open(p, takeKey: false)
    defer { c.close() }
    let m = try #require(c.model)
    for _ in 0..<40 where !m.hasChoices { try await Task.sleep(nanoseconds: 50_000_000) }
    #expect(m.hasChoices)
    c.takeKeyForPointer()
    #expect(c.pointerKeyed && m.keyMode == .lent)

    #expect(c.panel.onKey?(enterHeld) == true)
    #expect(c.panel.onKey?(enter) == true)
    try await Task.sleep(nanoseconds: settle)
    #expect(actions.answered.isEmpty && m.chosen && m.notice == "Press ⏎ again to answer: Yes")
    // Still lent: the choice keys did not hand the keyboard back.
    #expect(c.pointerKeyed && c.panel.isKeyWindow)
    #expect(c.panel.onKey?(down) == true && m.selectedStep == 1)
    #expect(c.panel.onKey?(enter) == true)
    try await Task.sleep(nanoseconds: settle)
    #expect(actions.answered == [1])
}

/// An agent's window opens with the cursor in its composer, so typing
/// starts at once; one asking a question opens on the question, its keys
/// (1–9, ⏎) left free to answer it.
@MainActor @Test func aWindowOpensInItsComposerUnlessItAsks() async throws {
    let near = NSRect(x: 900, y: 300, width: 10, height: 10)
    let idle = blocked("w1:p3", at: 10)
    let fleet = FleetModel()
    fleet.apply(panes: AllPanes(panes: [idle]))
    let plain = CardModel(pane: idle, fleet: fleet, actions: FixtureCardActions(chats: [:], canAcknowledge: true))
    let a = AgentWindowController(model: plain, near: near, media: nil)
    defer {
        plain.stopPolling()
        a.panel.orderOut(nil)
    }
    a.show()
    for _ in 0..<40 where !plain.composerHasKeys { try await Task.sleep(nanoseconds: 25_000_000) }
    #expect(plain.composerHasKeys)

    let (asking, _) = try await askingInbox()
    let b = AgentWindowController(model: asking, near: near, media: nil)
    defer {
        asking.stopPolling()
        b.panel.orderOut(nil)
    }
    #expect(asking.asksQuestion)
    b.show()
    try await Task.sleep(nanoseconds: settle)
    #expect(!asking.composerHasKeys)
}
