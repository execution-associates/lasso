import AppKit
import Foundation
import Testing

@testable import LassoDesktopKit

private let packageDir = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
    .deletingLastPathComponent()
private let specDir = packageDir.appendingPathComponent("Spec")

@Test func ciede2000MatchesSharmaVectors() {
    typealias L = ColorMath.Lab
    let cases: [(L, L, Double)] = [
        (L(l: 50, a: 2.6772, b: -79.7751), L(l: 50, a: 0, b: -82.7485), 2.0425),
        (L(l: 50, a: 0, b: 0), L(l: 50, a: -1, b: 2), 2.3669),
        (L(l: 50, a: 2.5, b: 0), L(l: 73, a: 25, b: -18), 27.1492),
        (L(l: 2.0776, a: 0.0795, b: -1.1350), L(l: 0.9033, a: -0.0636, b: -0.5514), 0.9082),
    ]
    for (p, q, want) in cases {
        #expect(abs(ColorMath.deltaE2000(p, q) - want) < 0.0005)
    }
}

@Test func identicalColoursHaveNoDifference() {
    let c = RGBA(parsing: "#1E2020")!
    #expect(ColorMath.deltaE2000(c, c) < 1e-9)
}

@Test func specSpringOvershootsHalfAPointOn330() {
    // The brief's own check: response 0.2, damping 0.9 overshoots <= 0.5 pt
    // on ~330 pt of travel.
    let m = SpringModel(response: 0.2, damping: 0.9)
    #expect(abs(m.overshootFraction * 330 - 0.50) < 0.01)
}

@Test func tokenColoursParse() {
    #expect(RGBA(parsing: "#3877F4") == RGBA(r: 0x38 / 255.0, g: 0x77 / 255.0, b: 0xF4 / 255.0, a: 1))
    #expect(RGBA(parsing: "rgba(255,255,255,0.06)")?.a == 0.06)
    #expect(RGBA(parsing: "blue") == nil)
}

@Test func everyThemeTokenIsInTheSpecWithTheSameValue() throws {
    let spec = try Spec.load(specDir.appendingPathComponent("tokens.json"))
    let ids = Set(Theme.all.map(\.id))
    #expect(ids.count == Theme.all.count, "duplicate ids in Theme.all")
    #expect(ids == Set(spec.tokens.keys))
    for t in Theme.all {
        #expect(spec.tokens[t.id]?.value == t.value, "\(t.id)")
        #expect(spec.tokens[t.id]?.provenance == t.provenance.kind, "\(t.id)")
    }
}

@MainActor @Test func fixtureOrdersTheRailByUrgency() throws {
    let fx = try Fixture.load("rail-expanded", from: specDir.appendingPathComponent("fixtures"))
    let fleet = FleetModel()
    fleet.apply(panes: fx.server.all_panes)
    fleet.apply(bots: fx.server.bots)
    fleet.inboxSince = Int64((fx.now.timeIntervalSince1970 - 3600) * 1000)
    #expect(fleet.attentionCount == fx.expect?.badge)
    #expect(fleet.railSlots(maxSlots: Theme.Rail.maxSlots.int).map(\.id) == fx.expect?.rail_slots)
}

@Test func collapsedFixtureExtendsExpanded() throws {
    let fx = try Fixture.load("rail-collapsed", from: specDir.appendingPathComponent("fixtures"))
    #expect(fx.ui.rail == "collapsed")
    #expect(fx.server.all_panes.panes.count == 10)
}

/// Constraint 4: nothing may print to stdout/stderr from the app's sources
/// (transcripts and keys must never reach a log). The parity report writes
/// through FileHandle on purpose.
@Test func sourcesNeverPrint() throws {
    let src = packageDir.appendingPathComponent("Sources")
    let files = FileManager.default.enumerator(at: src, includingPropertiesForKeys: nil)!
        .compactMap { $0 as? URL }.filter { $0.pathExtension == "swift" }
    #expect(!files.isEmpty)
    for f in files {
        let text = try String(contentsOf: f, encoding: .utf8)
        #expect(!text.contains("print("), "\(f.lastPathComponent) calls print")
        #expect(!text.contains("NSLog("), "\(f.lastPathComponent) calls NSLog")
    }
}

/// Constraint 1: nothing visual is written inline. A view that pads, spaces,
/// sizes or scales with a raw number (other than 0) fails here; the number
/// belongs in Theme.swift with its provenance.
@Test func viewsTakeTheirLayoutFromTheme() throws {
    let src = packageDir.appendingPathComponent("Sources/LassoDesktopKit")
    let views = ["Rail", "Card", "List", "Inbox", "Chat", "Settings", "Platform", "Agent"]
    let literal = try Regex(
        #"\.padding\((\.[a-zA-Z]+, )?[1-9][0-9]*(\.[0-9]+)?\)|spacing: [1-9]|(width|height|size): [1-9][0-9]*(\.[0-9]+)?[,)]|\* 0\.[0-9]|offset\(x: [1-9]"#)
    for dir in views {
        let files = FileManager.default.enumerator(at: src.appendingPathComponent(dir), includingPropertiesForKeys: nil)!
            .compactMap { $0 as? URL }.filter { $0.pathExtension == "swift" }
        for f in files {
            for (n, line) in try String(contentsOf: f, encoding: .utf8).split(separator: "\n").enumerated()
            where line.contains(literal) && !line.contains("//") {
                Issue.record("\(f.lastPathComponent):\(n + 1): \(line.trimmingCharacters(in: .whitespaces))")
            }
        }
    }
}

/// Hover opens the control under the pointer, found from RailMetrics: its
/// frames must be the ones the rail draws (the parity anchors).
@MainActor @Test func hoverFramesMatchTheRendered() async throws {
    let spec = URL(fileURLWithPath: #filePath).deletingLastPathComponent().appendingPathComponent("../../Spec")
    let fx = try Fixture.load("rail-expanded", from: spec.appendingPathComponent("fixtures"))
    let fleet = FleetModel()
    fleet.apply(panes: fx.server.all_panes)
    fleet.apply(bots: fx.server.bots)
    fleet.apply(uiState: fx.server.ui_state)
    let model = RailModel(fleet: fleet)
    model.setExpanded(true, animated: false)
    let cap = try await Capturer.capture(RailView(model: model), size: CGSize(
        width: RailMetrics.panelWidth, height: RailMetrics.expandedHeight(Theme.Rail.maxSlots.int) + 2 * RailMetrics.margin))
    let anchors: [RailModel.Item: String] = [.inbox: "rail.inbox", .agents: "rail.agents.capsule", .settings: "rail.ellipsis"]
    for (item, frame) in RailMetrics.itemFrames(model.slots.count) {
        let drawn = try #require(cap.frames[anchors[item]!])
        #expect(abs(drawn.minY - frame.minY) <= 1 && abs(drawn.height - frame.height) <= 1, "\(item): \(drawn) vs \(frame)")
    }
}

/// The inbox shortcut survives a round trip through UserDefaults' string,
/// and a recorded combination reads the way macOS menus write it.
@Test func inboxShortcutChoicesRoundTrip() {
    for c: InboxShortcutChoice in [.off, .doubleTap(.option), .doubleTap(.command), .combo(keyCode: 49, modifiers: 1 << 19, display: "⌥Space")] {
        #expect(InboxShortcutChoice(stored: c.stored) == c)
    }
    #expect(InboxShortcutChoice(stored: "double:hyper") == nil)
    #expect(InboxShortcutChoice.fallback == .combo(keyCode: 49, modifiers: 1 << 19, display: "⌥Space"))
    #expect(!InboxShortcutChoice.fallback.needsAccessibility)
    #expect(InboxShortcutChoice.doubleTap(.option).needsAccessibility)
}

/// The inbox (PLAN 13.2): waiting first, then finished turns; claude and
/// omp only; a Discard hides an entry until its transcript moves; a new turn
/// is unseen again (the badge).
@MainActor @Test func inboxMembershipDiscardAndSeen() {
    func pane(_ id: String, _ agent: String, _ status: String, _ at: Int64) -> HostPane {
        var p = HostPane(
            host: "local", host_label: "titan", pane_id: id, workspace_id: "w1", workspace_label: "Agents",
            tab_id: "w1:t1", tab_label: id, cwd: "/", agent: agent, agent_status: status, has_agent: true, focused: false)
        p.transcript_at = at
        return p
    }
    let fleet = FleetModel()
    fleet.apply(panes: AllPanes(panes: [
        pane("w1:p1", "claude", "done", 10), pane("w1:p2", "omp", "blocked", 5),
        pane("w1:p3", "codex", "blocked", 20), pane("w1:p4", "claude", "working", 30),
    ]))
    #expect(fleet.inbox.map(\.pane.pane_id) == ["w1:p2", "w1:p1"])
    #expect(fleet.attention == .blocked && fleet.attentionCount == 2)

    let inbox = InboxModel(fleet: fleet)
    inbox.markSeen(fleet.inbox[0].pane)
    inbox.markSeen(fleet.inbox[1].pane)
    #expect(fleet.attention == nil && fleet.attentionCount == 0)

    var ui = UIStateSlice()
    ui.dismissed_cards = ["local\u{0}w1:p1": 10]
    fleet.apply(uiState: ui)
    #expect(fleet.inbox.map(\.pane.pane_id) == ["w1:p2"])
    fleet.apply(panes: AllPanes(panes: [pane("w1:p1", "claude", "done", 11), pane("w1:p2", "omp", "blocked", 5)]))
    #expect(fleet.inbox.map(\.pane.pane_id) == ["w1:p2", "w1:p1"])
    #expect(fleet.unseen.map(\.pane.pane_id) == ["w1:p1"])
}

/// An answer is built from the question as drawn (PLAN 13.4): a new question
/// drops the pick in progress, and Other sends free text where the dialog
/// has that row (S4), never where claude lays out previews.
@MainActor @Test func answersUseTheDrawnQuestion() throws {
    func chat(_ question: String, previews: Bool = false) throws -> ChatPayload {
        let preview = previews ? #","preview":"x""# : ""
        return try JSONDecoder().decode(
            ChatPayload.self,
            from: Data(
                #"{"pane_id":"w1:p1","agent":"claude","host":"local","items":[{"kind":"tool","id":"t\#(question.count)","tool":{"name":"AskUserQuestion","state":"running","ask":{"questions":[{"question":"\#(question)","options":[{"label":"Yes"\#(preview)},{"label":"No"}]}]}}}]}"#
                    .utf8))
    }
    let pane = HostPane(
        host: "local", host_label: "titan", pane_id: "w1:p1", workspace_id: "w1", workspace_label: "Agents",
        tab_id: "w1:t1", tab_label: "stub", cwd: "/", agent: "claude", agent_status: "blocked", has_agent: true, focused: false)
    let actions = FixtureCardActions(chats: [:], canDismiss: true)
    let m = CardModel(pane: pane, fleet: FleetModel(), actions: actions)
    m.show(chat: try chat("Deploy?"))
    #expect(m.captured?.question == "Deploy?" && m.allowsCustom)
    m.highlight(2)
    #expect(m.highlighted == 1)
    m.show(chat: try chat("Deploy to production?"))
    #expect(m.captured?.question == "Deploy to production?" && m.highlighted == nil)
    m.show(chat: try chat("Pick a layout", previews: true))
    #expect(!m.allowsCustom)
}

private func key(_ c: String, _ code: UInt16) -> NSEvent {
    NSEvent.keyEvent(
        with: .keyDown, location: .zero, modifierFlags: [], timestamp: 0, windowNumber: 0, context: nil,
        characters: c, charactersIgnoringModifiers: c, isARepeat: false, keyCode: code)!
}

/// The inbox card answers j/k/x/e (PLAN 13.7a): they are in its live
/// bindings, they route to paging, kill and discard, a focused composer keeps
/// them as text, and a keyboard the pointer lent takes those four (and esc)
/// only, handing everything else back.
@MainActor @Test func inboxKeysReachTheCard() {
    var p = HostPane(
        host: "local", host_label: "titan", pane_id: "w1:p1", workspace_id: "w1", workspace_label: "Agents",
        tab_id: "w1:t1", tab_label: "stub", cwd: "/", agent: "claude", agent_status: "blocked", has_agent: true, focused: false)
    p.transcript_at = 10
    let fleet = FleetModel()
    fleet.apply(panes: AllPanes(panes: [p]))
    let m = CardModel(pane: p, fleet: fleet, actions: FixtureCardActions(chats: [:], canDismiss: true))
    m.inbox = InboxModel(fleet: fleet)
    for b in [Keys.next, Keys.previous, Keys.kill, Keys.discard] {
        #expect(m.activeBindings.contains(b), "\(b.id) not live in the inbox card")
    }
    let j = key("j", 38), k = key("k", 40), x = key("x", 7), e = key("e", 14)
    #expect(CardKeys.route(j, editing: false, pointerKeyed: false) == .next)
    #expect(CardKeys.route(k, editing: false, pointerKeyed: false) == .previous)
    #expect(CardKeys.route(x, editing: false, pointerKeyed: false) == .kill)
    #expect(CardKeys.route(e, editing: false, pointerKeyed: false) == .discard)
    // A focused composer: the field types them.
    for ev in [j, k, x, e] { #expect(CardKeys.route(ev, editing: true, pointerKeyed: false) == nil) }
    // Lent by the pointer: the four and esc act; anything else goes back.
    #expect(CardKeys.route(j, editing: false, pointerKeyed: true) == .next)
    #expect(CardKeys.route(key("a", 0), editing: false, pointerKeyed: true) == .handBack)
    #expect(CardKeys.route(key("q", 12), editing: false, pointerKeyed: true) == .handBack)
}
