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

    public init() {}

    public func apply(panes: AllPanes) {
        self.panes = panes.panes
        hostErrors = panes.errors ?? [:]
    }

    public func apply(bots: BotsPayload) { self.bots = bots.bots }
    public func apply(uiState: UIStateSlice) { self.uiState = uiState }

    /// Forget everything: another lasso is about to fill it.
    public func clear() {
        panes = []
        bots = []
        uiState = UIStateSlice()
        hostErrors = [:]
    }

    /// Agents only (a pane without one is not a fleet member, as in the web
    /// client), most urgent first, newest transcript first within a state.
    public var agents: [HostPane] {
        panes.filter { $0.has_agent && $0.closed != true }.sorted(by: FleetModel.urgencyOrder)
    }

    nonisolated static func urgencyOrder(_ a: HostPane, _ b: HostPane) -> Bool {
        if a.state.urgency != b.state.urgency { return a.state.urgency < b.state.urgency }
        let ta = a.transcript_at ?? .min
        let tb = b.transcript_at ?? .min
        if ta != tb { return ta > tb }
        return a.key < b.key
    }

    /// What is waiting on the human: blocked agents that are not a bot's own
    /// pane, plus blocked bots (a bot and its pane count once).
    public var badgeCount: Int {
        agents.filter { $0.state == .blocked && bot(for: $0) == nil }.count + bots.filter { $0.state == "blocked" }.count
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
