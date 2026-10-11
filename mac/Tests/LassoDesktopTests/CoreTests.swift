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
    #expect(fleet.badgeCount == fx.expect?.badge)
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
    let views = ["Rail", "Card", "List", "Inbox", "Chat", "Settings", "Platform"]
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
