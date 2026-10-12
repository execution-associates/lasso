import AppKit
import Combine
import Foundation

// The Bots view (PLAN 23): the rail's Bots button, the column it opens on a
// hover, and a bot's own window. Bots are lasso's (src/bots.go); creating,
// deleting and configuring them stays in the web app's /bots.

/// How a bot stands, most urgent first: the column's order, its dot, the
/// rail button's dot.
public enum BotStanding: Int, Comparable, Sendable {
    case asking, working, error, idle, stopped

    public init(_ b: BotView) {
        if b.stopped || b.state == "stopped" {
            self = .stopped
        } else if b.state == "blocked" || !(b.pending_ui ?? []).isEmpty {
            self = .asking
        } else if b.state == "working" || b.state == "starting" {
            self = .working
        } else if !(b.error ?? "").isEmpty {
            self = .error
        } else {
            self = .idle
        }
    }

    public static func < (a: Self, b: Self) -> Bool { a.rawValue < b.rawValue }

    public var label: String {
        switch self {
        case .asking: "asking"
        case .working: "working"
        case .error: "error"
        case .idle: "idle"
        case .stopped: "stopped"
        }
    }

    /// The dot's colour, in the agents' state colours.
    public var state: AgentState {
        switch self {
        case .asking, .error: .blocked
        case .working: .working
        case .idle, .stopped: .idle
        }
    }

    /// The column's order: asking, working, error, idle, stopped; by name within each.
    public static func ordered(_ bots: [BotView]) -> [BotView] {
        bots.sorted { a, b in
            let (x, y) = (BotStanding(a), BotStanding(b))
            return x == y ? a.name < b.name : x < y
        }
    }

    /// The rail button's dot: the most urgent bot that needs a look (asking,
    /// working or failed), none when every bot is idle or stopped.
    public static func railDot(_ bots: [BotView]) -> AgentState? {
        guard let top = bots.map(BotStanding.init).min(), top <= .error else { return nil }
        return top.state
    }
}

extension BotView {
    public var standing: BotStanding { BotStanding(self) }

    /// What the row says under the name: the question it asks, else its
    /// last words, else why it failed.
    public var lastLine: String? {
        let s = standing
        if s == .asking, let q = pending_ui?.first?.title ?? waiting_for, !q.isEmpty { return q }
        if s == .error, let e = error, !e.isEmpty { return e }
        guard let t = last_text?.trimmingCharacters(in: .whitespacesAndNewlines), !t.isEmpty else { return nil }
        return t.split(separator: "\n").first.map(String.init)
    }

    /// The model it runs: omp's live reading, else the configured one.
    public var modelName: String? { [live_model?.name, live_model?.id, model].compactMap { $0 }.first { !$0.isEmpty } }
}

/// One of a bot's conversations (GET /api/bots/<name>/sessions; src/botsessions.go).
public struct BotSession: Codable, Sendable, Equatable, Identifiable {
    public var id: String
    public var title: String?
    public var preview: String?
    public var updated_at: String
    public var messages: Int?
    public var current: Bool?

    public var name: String { [title, preview].compactMap { $0 }.first { !$0.isEmpty } ?? "Untitled conversation" }
}

/// What a bot's window does besides chatting: lasso's /api/bots/<name> routes
/// (src/botsapi.go), or a fixture's records.
@MainActor
public protocol BotActions: AnyObject {
    /// POST start | stop | restart.
    func lifecycle(_ bot: BotView, _ action: String) async throws
    func sessions(_ bot: BotView) async throws -> [BotSession]
    func newSession(_ bot: BotView) async throws
    func resume(_ bot: BotView, session: String) async throws
    /// An omp bot switches live; the answer says when a restart is needed.
    func setModel(_ bot: BotView, _ model: String) async throws -> Bool
    func jobCount(_ bot: BotView) async throws -> Int
    /// The bot's picture, when it has one.
    func avatar(_ bot: BotView) async -> NSImage?
}

@MainActor
public final class LiveBotActions: BotActions {
    let client: LassoClient
    public init(client: LassoClient) { self.client = client }

    static func path(_ bot: BotView, _ tail: String) -> String {
        "/api/bots/\(bot.name.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? bot.name)/\(tail)"
    }

    struct Empty: Codable {}
    struct Sessions: Decodable { var sessions: [BotSession] }
    struct Resume: Encodable { var id: String }
    struct Model: Encodable { var model: String }
    struct Modeled: Decodable { var restart_needed: Bool? }
    struct Jobs: Decodable { var jobs: [JSONValue] }

    public func lifecycle(_ bot: BotView, _ action: String) async throws {
        let _: JSONValue = try await client.post(Self.path(bot, action), json: Empty())
    }

    public func sessions(_ bot: BotView) async throws -> [BotSession] {
        try await client.get(Self.path(bot, "sessions"), as: Sessions.self).sessions
    }

    public func newSession(_ bot: BotView) async throws {
        let _: JSONValue = try await client.post(Self.path(bot, "sessions/new"), json: Empty())
    }

    public func resume(_ bot: BotView, session: String) async throws {
        let _: JSONValue = try await client.post(Self.path(bot, "sessions/resume"), json: Resume(id: session))
    }

    public func setModel(_ bot: BotView, _ model: String) async throws -> Bool {
        let m: Modeled = try await client.post(Self.path(bot, "model"), json: Model(model: model))
        return m.restart_needed == true
    }

    public func jobCount(_ bot: BotView) async throws -> Int {
        try await client.get(Self.path(bot, "jobs"), as: Jobs.self).jobs.count
    }

    public func avatar(_ bot: BotView) async -> NSImage? {
        guard let file = bot.avatar_image, !file.isEmpty else { return nil }
        // The stored name carries the revision after "?", which keeps the URL fresh.
        let rev = file.split(separator: "?", maxSplits: 1).dropFirst().first.map { "?" + $0 } ?? ""
        var r = client.request(raw: Self.path(bot, "avatar") + rev)
        r.setValue("image/*", forHTTPHeaderField: "Accept")
        guard let (data, http) = try? await client.response(r), http?.statusCode == 200 else { return nil }
        return NSImage(data: data)
    }
}

/// The fixtures' actions: recorded, with answers a test sets.
@MainActor
public final class FixtureBotActions: BotActions {
    public private(set) var calls: [String] = []
    public var refusal: LassoError?
    public var sessionList: [BotSession] = [
        BotSession(id: "s2", title: "Calendar this week", updated_at: "2026-10-11T18:50:00Z", messages: 12, current: true),
        BotSession(id: "s1", title: nil, preview: "What's waiting on me?", updated_at: "2026-10-10T09:00:00Z", messages: 4),
    ]
    public init() {}

    private func record(_ s: String) throws {
        calls.append(s)
        if let refusal { throw refusal }
    }

    public func lifecycle(_ bot: BotView, _ action: String) async throws { try record("\(action) \(bot.name)") }
    public func sessions(_ bot: BotView) async throws -> [BotSession] { sessionList }
    public func newSession(_ bot: BotView) async throws { try record("new \(bot.name)") }
    public func resume(_ bot: BotView, session: String) async throws { try record("resume \(bot.name) \(session)") }
    public func setModel(_ bot: BotView, _ model: String) async throws -> Bool {
        try record("model \(bot.name) \(model)")
        return false
    }
    public func jobCount(_ bot: BotView) async throws -> Int { 3 }
    public func avatar(_ bot: BotView) async -> NSImage? { nil }
}

/// The Bots column's state: the bots in standing order, the selected and
/// hovered rows, and the rows' frames for the hover poll.
@MainActor
public final class BotsListModel: ObservableObject, HoverColumn {
    public let fleet: FleetModel
    @Published public var selected: String?
    @Published public var hovered: String?
    /// The keyboard moved last: `selected` has the highlight, not the pointer.
    @Published public var keyboardLit = false
    /// Each row's frame in the scrolled rows (`ColumnRows.space`), by bot name.
    public var rowFrames: [String: CGRect] = [:] {
        didSet { if rowFrames != oldValue { rowFramesChanged() } }
    }
    /// The hover's: a layout landed (`ColumnHover`).
    public var rowFramesChanged: () -> Void = {}
    public var onOpen: (BotView) -> Void = { _ in }
    public var onManage: () -> Void = {}
    public var onClose: () -> Void = {}
    public let now: () -> Date

    public init(fleet: FleetModel, now: @escaping () -> Date = Date.init) {
        self.fleet = fleet
        self.now = now
    }

    /// The row order held while the column is open. Not published: held
    /// as the column opens, it is the order already drawn, and a redraw then
    /// lays the rows out before the column's scroll view exists, where the
    /// hover cannot map the pointer to them.
    public private(set) var held = FrozenOrder()

    public var bots: [BotView] { held.apply(BotStanding.ordered(fleet.bots), key: \.name) }

    /// Hold the rows where they are now, until `thawOrder`.
    public func freezeOrder() { held.freeze(BotStanding.ordered(fleet.bots).map(\.name)) }
    /// Back to the live order, drawn while the column is away.
    public func thawOrder() {
        held.thaw()
        objectWillChange.send()
    }

    /// J/K (and ↑/↓) move the selection.
    public func move(_ delta: Int) {
        let list = bots
        guard !list.isEmpty else { return }
        let i = list.firstIndex { $0.name == selected } ?? (delta > 0 ? -1 : list.count)
        selected = list[max(0, min(list.count - 1, i + delta))].name
    }

    /// ⏎: the selected bot's window.
    public func openSelected() {
        guard let b = bots.first(where: { $0.name == selected }) else { return }
        onOpen(b)
    }

    public func open(_ b: BotView) {
        selected = b.name
        onOpen(b)
    }
}
