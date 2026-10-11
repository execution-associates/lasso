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

/// A one-shot notice lasso raised (SSE `notice`), kept on this Mac.
public struct InboxNotice: Codable, Identifiable, Equatable, Sendable {
    public var id: String
    public var at: Int64
    public var level: String
    public var title: String
    public var detail: String?
    /// lasso_notice, or agent_message for an agent's own ping (NotifyKinds).
    public var kind: String = NotifyKinds.lassoNotice

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
    /// first in each. A finished turn counts from `inboxSince` (unix ms), so
    /// the first launch starts with what is waiting, not with history.
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
        return waiting.sorted(by: newest) + finished.sorted(by: newest)
    }

    /// What is new since this Mac looked: entries whose mark is not the one
    /// seen, and notices not yet seen. This is the badge (PLAN 13.7).
    public var unseen: [InboxEntry] { inbox.filter { seen[$0.id] != $0.mark } }

    public var unseenNotices: [InboxNotice] { notices.filter { seen["notice:\($0.id)"] == nil } }

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

/// The inbox's state on this Mac: what has been seen (by entry, its mark),
/// and since when finished turns count. A viewer convenience, so it lives in
/// UserDefaults; what is shared (Acknowledge) lives in lasso's ui_state.
@MainActor
public enum InboxMemory {
    static let seenKey = "inboxSeen"
    static let sinceKey = "inboxSince"

    /// Load into the fleet; a first launch sets `since` to now.
    public static func load(into fleet: FleetModel) {
        let d = UserDefaults.standard
        if d.object(forKey: sinceKey) == nil { d.set(Int64(Date().timeIntervalSince1970 * 1000), forKey: sinceKey) }
        fleet.inboxSince = (d.object(forKey: sinceKey) as? NSNumber)?.int64Value ?? 0
        fleet.seen = d.dictionary(forKey: seenKey) as? [String: String] ?? [:]
    }

    static func save(_ fleet: FleetModel) {
        // Keep it bounded: only what is still in the inbox (or a notice) matters.
        let live = Set(fleet.inbox.map(\.id)).union(fleet.notices.map { "notice:\($0.id)" })
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

    public init(fleet: FleetModel, now: @escaping () -> Date = Date.init) {
        self.fleet = fleet
        self.now = now
    }

    public var entries: [InboxEntry] { fleet.inbox }

    /// The panes the card pages through, in the inbox's order.
    public var panes: [HostPane] { entries.map(\.pane) }

    /// The card reached this pane: its entry, as it is now, is seen.
    public func markSeen(_ p: HostPane) {
        guard let e = entries.first(where: { $0.id == p.key }), fleet.seen[e.id] != e.mark else { return }
        fleet.seen[e.id] = e.mark
        if persist { InboxMemory.save(fleet) }
    }

    /// The panel showed the notices.
    public func markNoticesSeen() {
        var changed = false
        for n in fleet.notices where fleet.seen["notice:\(n.id)"] == nil {
            fleet.seen["notice:\(n.id)"] = "seen"
            changed = true
        }
        if changed, persist { InboxMemory.save(fleet) }
    }

    public func acknowledge(_ n: InboxNotice) { fleet.notices.removeAll { $0.id == n.id } }

    /// What arrived, for the arrival sound: an entry with a new mark, or a
    /// notice, each with the kind it is (which this Mac may not want to hear).
    public var arrivals: [String: String] {
        var out: [String: String] = [:]
        for e in entries {
            let kind =
                e.kind == .waiting ? NotifyKinds.agentBlocked : (e.bot != nil ? NotifyKinds.botMessage : NotifyKinds.agentFinished)
            out["\(e.id)@\(e.mark)"] = kind
        }
        for n in fleet.notices { out["notice:\(n.id)"] = n.kind }
        return out
    }
}
