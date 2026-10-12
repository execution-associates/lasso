import Combine
import Foundation

/// The fleet as the rail, the list and the card see it: lasso's panes,
/// bots and shared UI state, from either the live client or a fixture.
@MainActor
public final class FleetModel: ObservableObject {
    @Published public private(set) var panes: [HostPane] = []
    @Published public private(set) var bots: [BotView] = []
    @Published public private(set) var uiState = UIStateSlice()
    @Published public private(set) var hostErrors: [String: String] = [:]
    /// The inbox's seen marks on this Mac, by entry (InboxMemory).
    @Published public var seen: [String: String] = [:]
    /// lasso's notices since launch, newest last (at most 50).
    @Published public var notices: [InboxNotice] = []
    /// lasso's notification kinds and this Mac's choices; nil when lasso has
    /// none (an older lasso), and then every arrival sounds.
    @Published public var notify: NotifyPayload?
    /// Finished turns count from here (unix ms); 0 counts them all.
    public var inboxSince: Int64 = 0
    /// No lasso answers this Mac (FleetSync): the rail says so, and the
    /// inbox opens the first-run panel instead.
    @Published public var offline = false

    public init() {}

    /// Agents killed from this Mac, by pane key: gone from the fleet at
    /// once, and kept gone while a poll still lists the dying pane.
    @Published public private(set) var killed: Set<String> = []
    /// Inbox entries acknowledged from this Mac, by key, with the mark they
    /// had: hidden at once, before ui_state says so, until something new
    /// (another mark) brings the entry back.
    @Published public private(set) var acknowledged: [String: String] = [:]

    public func apply(panes: AllPanes) {
        self.panes = panes.panes
        hostErrors = panes.errors ?? [:]
        // A killed pane is remembered until the fleet stops listing it.
        let listed = Set(panes.panes.map(\.key))
        if !killed.isSubset(of: listed) { killed.formIntersection(listed) }
        if acknowledged.keys.contains(where: { !listed.contains($0) && !$0.hasPrefix("bot:") }) {
            acknowledged = acknowledged.filter { listed.contains($0.key) || $0.key.hasPrefix("bot:") }
        }
    }

    /// The agent in this pane was killed: it leaves the fleet now.
    public func markKilled(_ key: String) { killed.insert(key) }

    /// This inbox entry was acknowledged: it leaves the inbox now.
    public func markAcknowledged(_ entry: InboxEntry) { acknowledged[entry.ackKey] = entry.mark }

    public func apply(bots: BotsPayload) { self.bots = bots.bots }

    /// An agent was renamed: show it now, before the next poll says so.
    public func relabel(_ key: String, _ label: String?) {
        guard let i = panes.firstIndex(where: { $0.key == key }) else { return }
        panes[i].agent_label = label
    }
    public func apply(uiState: UIStateSlice) { self.uiState = uiState }

    /// Forget everything: another lasso is about to fill it.
    public func clear() {
        panes = []
        bots = []
        uiState = UIStateSlice()
        hostErrors = [:]
        killed = []
        acknowledged = [:]
    }

    /// Agents only (a pane without one is not a fleet member, as in the web
    /// client), most urgent first, newest transcript first within a state.
    public var agents: [HostPane] {
        panes.filter { $0.has_agent && $0.closed != true && !killed.contains($0.key) }.sorted(by: FleetModel.urgencyOrder)
    }

    nonisolated static func urgencyOrder(_ a: HostPane, _ b: HostPane) -> Bool {
        if a.state.urgency != b.state.urgency { return a.state.urgency < b.state.urgency }
        let ta = a.transcript_at ?? .min
        let tb = b.transcript_at ?? .min
        if ta != tb { return ta > tb }
        return a.key < b.key
    }

    /// A notice from lasso's event stream: kept once by id, the last 50.
    public func add(notice n: InboxNotice) {
        guard !notices.contains(where: { $0.id == n.id }) else { return }
        notices.append(n)
        if notices.count > 50 { notices.removeFirst(notices.count - 50) }
    }

    /// The agents capsule's slots: at most `maxSlots`; when the fleet is
    /// larger, the last slot is "+N" for the rest.
    public func railSlots(maxSlots: Int) -> [RailSlot] {
        let all = agents
        guard all.count > maxSlots, maxSlots > 0 else { return all.map(RailSlot.agent) }
        let shown = all.prefix(maxSlots - 1).map(RailSlot.agent)
        return shown + [.overflow(all.count - (maxSlots - 1))]
    }
}

public enum RailSlot: Equatable, Sendable, Identifiable {
    case agent(HostPane)
    case overflow(Int)

    public var id: String {
        switch self {
        case .agent(let p): p.key
        case .overflow(let n): "+\(n)"
        }
    }
}
