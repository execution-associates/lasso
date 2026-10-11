import Combine
import Foundation

/// The list panel's side effects, through lasso's API or recorded no-ops.
@MainActor
public protocol FleetActions: AnyObject {
    /// ui_state agent_pins: shared with the web client's pins.
    func pin(_ pane: HostPane, _ on: Bool) async throws
    /// herdr focus: brings the pane forward wherever its terminal is open.
    func focus(_ pane: HostPane) async throws
    func end(_ pane: HostPane) async throws -> String
}

/// One workspace's agents.
public struct FleetGroup: Identifiable, Equatable {
    public var id: String
    public var title: String
    public var agents: [HostPane]
}

/// The fleet as the list shows it: pins first, then one group per herdr
/// workspace (and machine, when there is more than one), each agent named by
/// its label or tab and listed most urgent first.
@MainActor
public final class ListModel: ObservableObject {
    @Published public var selected: String?
    @Published public var hovered: String?
    /// Each row's frame in the panel (top-left origin), by row id: what the
    /// pointer is over. Not published: the controller reads it as it polls.
    public var rowFrames: [String: CGRect] = [:]
    /// The agent whose End is waiting for a confirm.
    @Published public var confirmEnd: String?
    @Published public private(set) var notice: String?

    public let fleet: FleetModel
    public let actions: FleetActions
    public let now: () -> Date
    public var onOpen: (HostPane) -> Void = { _ in }
    public var onClose: () -> Void = {}

    public init(fleet: FleetModel, actions: FleetActions, now: @escaping () -> Date = Date.init) {
        self.fleet = fleet
        self.actions = actions
        self.now = now
    }

    public var pinnedKeys: [String] { fleet.uiState.pinned_agents ?? [] }

    public var pinned: [HostPane] {
        let byKey = Dictionary(fleet.agents.map { ($0.key, $0) }, uniquingKeysWith: { a, _ in a })
        return pinnedKeys.compactMap { byKey[$0] }
    }

    public var groups: [FleetGroup] {
        let agents = fleet.agents
        let machines = Set(agents.map(\.host)).count > 1
        var order: [String] = []
        var byID: [String: FleetGroup] = [:]
        for a in agents {
            let id = a.host + "\u{0}" + a.workspace_id
            if byID[id] == nil {
                order.append(id)
                byID[id] = FleetGroup(id: id, title: machines ? "\(a.group) · \(a.host_label)" : a.group, agents: [])
            }
            byID[id]?.agents.append(a)
        }
        return order.compactMap { byID[$0] }
    }

    /// Every row in display order, for keyboard movement.
    public var order: [HostPane] { pinned + groups.flatMap(\.agents) }

    public func move(_ delta: Int) {
        let list = order
        guard !list.isEmpty else { return }
        let i = list.firstIndex { $0.key == selected } ?? (delta > 0 ? -1 : list.count)
        selected = list[max(0, min(list.count - 1, i + delta))].key
        confirmEnd = nil
    }

    public func openSelected() {
        guard let key = selected, let p = fleet.agents.first(where: { $0.key == key }) else { return }
        open(p)
    }

    public func togglePin(_ p: HostPane) {
        let on = !pinnedKeys.contains(p.key)
        Task {
            do {
                try await actions.pin(p, on)
                notice = on ? "Pinned \(p.displayName)" : "Unpinned \(p.displayName)"
            } catch {
                notice = "Pin failed: \(error)"
            }
        }
    }

    public func focus(_ p: HostPane) {
        Task {
            do {
                try await actions.focus(p)
                notice = "Focused \(p.displayName) in herdr"
            } catch {
                notice = "Focus failed: \(error)"
            }
        }
    }

    /// Open a row: the agent's own window (a bot's too: its pane chats
    /// through the bot's routes).
    public func open(_ p: HostPane) {
        selected = p.key
        onOpen(p)
    }

    /// End asks first: the row turns into a confirm, and only that ends it.
    public func askEnd(_ p: HostPane) { confirmEnd = p.key }

    public func end(_ p: HostPane) {
        confirmEnd = nil
        Task {
            do { notice = try await actions.end(p) } catch { notice = "End failed: \(error)" }
        }
    }
}
