import Foundation
import Testing

@testable import LassoDesktopKit

/// The Inbox's shared cases (mac/Spec/inbox/rules.json): the phone's simple
/// app runs the same ones (src/web/src/simple/inbox-rule.test.js), so the Mac
/// and the phone list the same entries, and the rail's badge is the phone's
/// count, for the same lasso.
private struct InboxRules: Decodable {
    struct Case: Decodable {
        struct Want: Decodable {
            struct Entry: Decodable, Equatable, CustomStringConvertible {
                var kind: String
                var pane: String
                var ack_key: String
                var ack_at: Int64
                var description: String { "\(kind) \(pane.replacingOccurrences(of: "\u{0}", with: "/")) \(ack_key) \(ack_at)" }
            }
            var entries: [Entry]
            var notices: [String]
            var badge: Int
        }
        var name: String
        var panes: [HostPane]
        var bots: [BotView]
        var ui_state: UIStateSlice
        var notices: [LiveNotification]
        var want: Want
    }
    var cases: [Case]
}

private let rulesURL = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
    .deletingLastPathComponent().appendingPathComponent("Spec/inbox/rules.json")

@MainActor @Test func inboxFollowsTheSharedRules() throws {
    let rules = try JSONDecoder().decode(InboxRules.self, from: Data(contentsOf: rulesURL))
    #expect(!rules.cases.isEmpty)
    for c in rules.cases {
        let fleet = FleetModel()
        // A baseline this Mac would make up must never be the one used.
        fleet.fallbackSince = .max
        fleet.apply(panes: AllPanes(panes: c.panes))
        fleet.apply(bots: BotsPayload(bots: c.bots))
        fleet.apply(uiState: c.ui_state)
        fleet.apply(notices: NoticesPayload(notices: c.notices))
        let got = fleet.inbox.map {
            InboxRules.Case.Want.Entry(
                kind: $0.kind == .waiting ? "waiting" : "finished", pane: $0.pane.key, ack_key: $0.ackKey, ack_at: $0.ackAt)
        }
        #expect(got == c.want.entries, "\(c.name)")
        #expect(fleet.inboxNotices.reversed().map(\.id) == c.want.notices, "\(c.name)")
        #expect(fleet.badgeCount == c.want.badge, "\(c.name)")
    }
}

/// Seen is this Mac's and only lights the peek's dot: it never changes what
/// the inbox holds or the badge. A notice acknowledged here leaves at once
/// and is written for every device.
@MainActor @Test func seenNeverChangesTheBadgeAndNoticesAckEverywhere() async throws {
    let rules = try JSONDecoder().decode(InboxRules.self, from: Data(contentsOf: rulesURL))
    let c = try #require(rules.cases.first { $0.name == "notices" })
    let fleet = FleetModel()
    fleet.apply(panes: AllPanes(panes: c.panes))
    fleet.apply(uiState: c.ui_state)
    fleet.apply(notices: NoticesPayload(notices: c.notices))
    var written: [String: Int64] = [:]
    fleet.writeAcknowledgement = { key, at in written[key] = at }
    let inbox = InboxModel(fleet: fleet)
    let before = fleet.badgeCount
    for e in fleet.inbox { inbox.markSeen(e.pane) }
    inbox.markNoticesSeen()
    #expect(fleet.badgeCount == before && fleet.attentionCount == 0)

    let n = try #require(fleet.inboxNotices.last)
    inbox.acknowledge(n)
    #expect(fleet.badgeCount == before - 1 && !fleet.inboxNotices.contains { $0.id == n.id })
    try await Task.sleep(nanoseconds: 50_000_000)
    #expect(written == [InboxNotice.ackKey(n.id): n.at])
}
