import Combine
import Foundation

extension FleetModel {
    /// The bot that runs in this pane, if any: by its pane, or by the herdr
    /// label a bot's pane carries (its name).
    public func bot(for pane: HostPane) -> BotView? {
        bots.first { b in
            b.host == pane.host && ((b.pane_id.map { $0 == pane.pane_id } ?? false) || b.name == pane.agent_label)
        }
    }
}

/// When each bot's last message was read here, so a new reply shows in the
/// inbox once. Per Mac (a viewer convenience, like the web client's own
/// record); a bot seen for the first time sets the baseline instead of
/// flooding the inbox.
@MainActor
public final class BotReads {
    static let key = "botSeen"
    private var seen: [String: String]

    public init() { seen = UserDefaults.standard.dictionary(forKey: Self.key) as? [String: String] ?? [:] }

    public func unread(_ b: BotView) -> Bool {
        guard b.last_kind == "agent", let at = b.last_at else { return false }
        guard let mark = seen[b.name] else {
            markRead(b)
            return false
        }
        return at > mark
    }

    public func markRead(_ b: BotView) {
        guard let at = b.last_at else { return }
        seen[b.name] = at
        UserDefaults.standard.set(seen, forKey: Self.key)
    }
}

public enum InboxItem: Identifiable, Equatable {
    case agent(HostPane)
    case bot(BotView)

    public var id: String {
        switch self {
        case .agent(let p): "agent:" + p.key
        case .bot(let b): "bot:" + b.name
        }
    }
}

/// The inbox: what waits on the human. Agents that are blocked or finished
/// unseen (not dismissed), and bots that are waiting or have a new reply,
/// most urgent first.
@MainActor
public final class InboxModel: ObservableObject {
    public let fleet: FleetModel
    public let reads: BotReads
    public let now: () -> Date
    public var onOpenAgent: (HostPane) -> Void = { _ in }
    public var onOpenBot: (BotView) -> Void = { _ in }
    public var onClose: () -> Void = {}

    public init(fleet: FleetModel, reads: BotReads, now: @escaping () -> Date = Date.init) {
        self.fleet = fleet
        self.reads = reads
        self.now = now
    }

    public var items: [InboxItem] {
        let dismissed = fleet.uiState.dismissed_cards ?? [:]
        var blocked: [InboxItem] = []
        var fresh: [InboxItem] = []
        var botsListed = Set<String>()
        for b in fleet.bots where b.state == "blocked" {
            blocked.append(.bot(b))
            botsListed.insert(b.name)
        }
        for p in fleet.agents where p.state == .blocked || p.state == .done {
            if let b = fleet.bot(for: p) {
                // A bot's pane is listed as the bot.
                if p.state == .blocked, !botsListed.contains(b.name) {
                    blocked.append(.bot(b))
                    botsListed.insert(b.name)
                }
                continue
            }
            if let at = dismissed[p.key], (p.transcript_at ?? 0) <= at { continue }
            if p.state == .blocked { blocked.append(.agent(p)) } else { fresh.append(.agent(p)) }
        }
        for b in fleet.bots where !botsListed.contains(b.name) && reads.unread(b) {
            fresh.append(.bot(b))
        }
        return blocked + fresh
    }

    /// The inbox as the card pages it: each item's pane, a bot by the pane
    /// it runs in (a bot that has none is left out).
    public var panes: [HostPane] {
        items.compactMap { item in
            switch item {
            case .agent(let p): p
            case .bot(let b): fleet.agents.first { fleet.bot(for: $0)?.name == b.name }
            }
        }
    }

    /// The card reached this pane: a bot's reply counts as read.
    public func markSeen(_ p: HostPane) {
        if let b = fleet.bot(for: p) { reads.markRead(b) }
    }

    public func open(_ item: InboxItem) {
        switch item {
        case .agent(let p): onOpenAgent(p)
        case .bot(let b):
            reads.markRead(b)
            onOpenBot(b)
        }
    }
}
