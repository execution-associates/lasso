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

/// One thing in the inbox (PLAN 13.2): a pane waiting on the human, or one
/// whose agent finished a turn. A bot enters as the pane it runs in.
public struct InboxEntry: Identifiable, Equatable, Sendable {
    public enum Kind: Sendable { case waiting, finished }

    public var pane: HostPane
    public var kind: Kind
    /// The bot running in the pane, if it is one.
    public var bot: String?
    /// Acknowledge writes this key and value to ui_state.card_dismiss
    /// (PLAN 13.6; the wire name is S1's).
    public var ackKey: String
    public var ackAt: Int64
    /// What makes this showing of it new: a later turn or another question
    /// changes it, so it is unseen again and sounds again.
    public var mark: String
    /// When it happened (unix ms), for the order.
    public var at: Int64

    public var id: String { pane.key }
}

/// A notice lasso raised: one it keeps for the inbox (/api/notices), or an
/// older lasso's SSE `notice`. Acknowledge is shared, as `notice:<id>`.
public struct InboxNotice: Codable, Identifiable, Equatable, Sendable {
    public var id: String
    public var at: Int64
    public var level: String
    public var title: String
    public var detail: String?
    /// lasso_notice, or agent_message for an agent's own ping (NotifyKinds).
    public var kind: String = NotifyKinds.lassoNotice

    /// Its Acknowledge key in ui_state.card_dismiss.
    public static func ackKey(_ id: String) -> String { "notice:\(id)" }

    public init(live n: LiveNotification) {
        self.init(id: n.id, at: n.at, level: n.level ?? "info", title: n.title, detail: n.body, kind: n.kind)
    }

    public init(id: String, at: Int64, level: String, title: String, detail: String?, kind: String) {
        self.id = id
        self.at = at
        self.level = level
        self.title = title
        self.detail = detail
        self.kind = kind
    }

    /// A lasso older than notice ids sends none: stamp it here.
    public init(from d: Decoder) throws {
        let c = try d.container(keyedBy: CodingKeys.self)
        id = try c.decodeIfPresent(String.self, forKey: .id) ?? UUID().uuidString
        at = try c.decodeIfPresent(Int64.self, forKey: .at) ?? Int64(Date().timeIntervalSince1970 * 1000)
        level = try c.decodeIfPresent(String.self, forKey: .level) ?? "info"
        title = try c.decode(String.self, forKey: .title)
        detail = try c.decodeIfPresent(String.self, forKey: .detail)
        kind = try c.decodeIfPresent(String.self, forKey: .kind) ?? NotifyKinds.lassoNotice
    }
}

extension FleetModel {
    /// The harnesses the inbox speaks for (PLAN 13.1): their asks share one
    /// shape lasso reads and answers.
    static let inboxHarnesses: Set<String> = ["claude", "omp"]

    /// The inbox, in order: waiting on you, then finished turns, newest
    /// first in each (ties by pane key). Everything it reads is lasso's, never
    /// this Mac's: the fleet, the bots, ui_state's acknowledgements and its
    /// baseline (`inboxSince`, ui_state.inbox_since), so the phone's simple
    /// app (src/web/src/simple/inbox-rule.ts) lists the same entries. Both are
    /// pinned by mac/Spec/inbox/rules.json. A finished turn counts after the
    /// baseline, so a lasso starts with what is waiting, not with history.
    public var inbox: [InboxEntry] {
        let acked = uiState.dismissed_cards ?? [:]
        var waiting: [InboxEntry] = []
        var finished: [InboxEntry] = []
        for p in agents where Self.inboxHarnesses.contains(p.agent.lowercased()) {
            let bot = bot(for: p)
            let pending = bot?.runtime == "omp" ? bot?.pending_ui?.first : nil
            let at = p.transcript_at ?? 0
            if p.state == .blocked || pending != nil {
                if let pending, let b = bot {
                    // An omp bot's question: hidden while that question is the acknowledged one.
                    let key = "bot:\(b.name)#\(pending.id)"
                    if acked[key] != nil { continue }
                    waiting.append(
                        InboxEntry(pane: p, kind: .waiting, bot: b.name, ackKey: key, ackAt: 0, mark: "ask:\(pending.id)", at: at))
                } else {
                    if let d = acked[p.key], at <= d { continue }
                    waiting.append(
                        InboxEntry(pane: p, kind: .waiting, bot: bot?.name, ackKey: p.key, ackAt: at, mark: "ask:\(at)", at: at))
                }
                continue
            }
            guard p.state == .done || p.state == .idle else { continue }
            if let b = bot {
                // A bot's turn is its last reply.
                guard b.last_kind == "agent", let ms = b.last_at.flatMap(Self.unixMS), ms > inboxSince else { continue }
                let key = "bot:\(b.name)"
                if let d = acked[key], ms <= d { continue }
                finished.append(InboxEntry(pane: p, kind: .finished, bot: b.name, ackKey: key, ackAt: ms, mark: "turn:\(ms)", at: ms))
            } else {
                guard at > inboxSince else { continue }
                if let d = acked[p.key], at <= d { continue }
                finished.append(InboxEntry(pane: p, kind: .finished, bot: nil, ackKey: p.key, ackAt: at, mark: "turn:\(at)", at: at))
            }
        }
        let newest: (InboxEntry, InboxEntry) -> Bool = { $0.at != $1.at ? $0.at > $1.at : $0.id < $1.id }
        // Acknowledged here, and not yet in ui_state: gone unless it is new.
        let hidden = acknowledged
        let shown: (InboxEntry) -> Bool = { hidden[$0.ackKey] != $0.mark }
        return waiting.filter(shown).sorted(by: newest) + finished.filter(shown).sorted(by: newest)
    }

    /// lasso's notices nobody has acknowledged, on any device: newest last.
    public var inboxNotices: [InboxNotice] {
        let acked = uiState.dismissed_cards ?? [:]
        return notices.filter { n in
            let key = InboxNotice.ackKey(n.id)
            return acked[key] == nil && acknowledged[key] == nil
        }
    }

    /// The rail's badge: every entry and notice the inbox holds. The same
    /// number as the phone's Inbox count, since both read only lasso's state.
    public var badgeCount: Int { inbox.count + inboxNotices.count }

    /// What is new since this Mac looked: entries whose mark is not the one
    /// seen, and notices not yet seen. The peek's dot (PLAN 13.7); seen is
    /// this Mac's and never changes what the inbox holds.
    public var unseen: [InboxEntry] { inbox.filter { seen[$0.id] != $0.mark } }

    public var unseenNotices: [InboxNotice] { inboxNotices.filter { seen["notice:\($0.id)"] == nil } }

    /// The badge's colour: blocked when something new waits on you, done
    /// when only finished turns or notices are new, nil when nothing is new.
    public var attention: AgentState? {
        let u = unseen
        if u.contains(where: { $0.kind == .waiting }) { return .blocked }
        return u.isEmpty && unseenNotices.isEmpty ? nil : .done
    }

    public var attentionCount: Int { unseen.count + unseenNotices.count }

    static func unixMS(_ iso: String) -> Int64? {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        guard let d = f.date(from: iso) ?? ISO8601DateFormatter().date(from: iso) else { return nil }
        return Int64(d.timeIntervalSince1970 * 1000)
    }
}

/// The inbox's state on this Mac: what has been seen (by entry, its mark).
/// A viewer convenience that never changes what the inbox holds, so it lives
/// in UserDefaults; what decides membership (Acknowledge, the baseline) is
/// lasso's ui_state.
@MainActor
public enum InboxMemory {
    static let seenKey = "inboxSeen"

    public static func load(into fleet: FleetModel) {
        let d = UserDefaults.standard
        // This Mac's own baseline, from before lasso kept one for everybody.
        d.removeObject(forKey: "inboxSince")
        fleet.fallbackSince = Int64(Date().timeIntervalSince1970 * 1000)
        fleet.seen = d.dictionary(forKey: seenKey) as? [String: String] ?? [:]
    }

    static func save(_ fleet: FleetModel) {
        // Keep it bounded: only what is still in the inbox (or a notice) matters.
        let live = Set(fleet.inbox.map(\.id)).union(fleet.inboxNotices.map { "notice:\($0.id)" })
        UserDefaults.standard.set(fleet.seen.filter { live.contains($0.key) }, forKey: seenKey)
    }
}

/// The inbox: the card pages its entries; the panel shows when there is no
/// message to open, with the notices.
@MainActor
public final class InboxModel: ObservableObject {
    public let fleet: FleetModel
    public let now: () -> Date
    /// Persist what was seen (live only; fixtures keep it in memory).
    public var persist = false
    public var onOpen: (HostPane) -> Void = { _ in }
    public var onClose: () -> Void = {}
    /// The card showed this pane's entry, or the panel the notices: their
    /// macOS notifications have been read.
    public var onSeen: (HostPane) -> Void = { _ in }
    public var onNoticesSeen: () -> Void = {}

    public init(fleet: FleetModel, now: @escaping () -> Date = Date.init) {
        self.fleet = fleet
        self.now = now
    }

    public var entries: [InboxEntry] { fleet.inbox }

    /// The panes the card pages through, in the inbox's order.
    public var panes: [HostPane] { entries.map(\.pane) }

    /// The card reached this pane: its entry, as it is now, is seen.
    public func markSeen(_ p: HostPane) {
        onSeen(p)
        guard let e = entries.first(where: { $0.id == p.key }), fleet.seen[e.id] != e.mark else { return }
        fleet.seen[e.id] = e.mark
        if persist { InboxMemory.save(fleet) }
    }

    /// The panel showed the notices.
    public func markNoticesSeen() {
        onNoticesSeen()
        var changed = false
        for n in fleet.inboxNotices where fleet.seen["notice:\(n.id)"] == nil {
            fleet.seen["notice:\(n.id)"] = "seen"
            changed = true
        }
        if changed, persist { InboxMemory.save(fleet) }
    }

    /// Acknowledge a notice on every device (ui_state.card_dismiss
    /// `notice:<id>`); it leaves this inbox at once.
    public func acknowledge(_ n: InboxNotice) {
        fleet.markAcknowledged(n)
        guard let write = fleet.writeAcknowledgement else { return }
        Task { await write(InboxNotice.ackKey(n.id), n.at) }
    }

    /// What the inbox holds as arrivals, for the arrival sound and macOS's
    /// notifications: an entry by its mark, or a notice, each with the kinds
    /// that announce it (this Mac may want none). A bot's turn is one
    /// announcement, as lasso's notifDelivers has it: the reply (bot_message)
    /// or, for a Mac that does not take replies, the finished turn; a bot
    /// with notify off announces no turn at all. A bot's question is
    /// agent_blocked like any agent's.
    public var arrivalList: [InboxArrival] {
        var out: [InboxArrival] = []
        for e in entries {
            let kinds: [String]
            switch (e.kind, e.bot) {
            case (.waiting, _): kinds = [NotifyKinds.agentBlocked]
            case (.finished, nil): kinds = [NotifyKinds.agentFinished]
            case (.finished, let name?):
                let notify = fleet.bots.first { $0.name == name && $0.host == e.pane.host }?.notify ?? true
                kinds = notify ? [NotifyKinds.botMessage, NotifyKinds.agentFinished] : []
            }
            out.append(InboxArrival(key: "\(e.id)@\(e.mark)", kinds: kinds, item: .entry(e)))
        }
        for n in fleet.inboxNotices { out.append(InboxArrival(key: "notice:\(n.id)", kinds: [n.kind], item: .notice(n))) }
        return out
    }

    /// The arrivals by key, with their kinds.
    public var arrivals: [String: [String]] {
        Dictionary(arrivalList.map { ($0.key, $0.kinds) }, uniquingKeysWith: { a, _ in a })
    }
}

/// Something the inbox holds, as an arrival: new when its key is.
public struct InboxArrival: Equatable, Sendable {
    public enum Item: Equatable, Sendable {
        case entry(InboxEntry)
        case notice(InboxNotice)
    }

    /// An entry's id and mark, or `notice:<id>`.
    public var key: String
    public var kinds: [String]
    public var item: Item
}
