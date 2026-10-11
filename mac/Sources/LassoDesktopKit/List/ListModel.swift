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

public struct FleetGroup: Identifiable, Equatable {
    public struct Repo: Identifiable, Equatable {
        public var id: String { name }
        public var name: String
        public var agents: [HostPane]
    }

    public var id: String { machine }
    public var machine: String
    public var repos: [Repo]
}

/// The fleet as the list shows it: pins first, then machine → repo → agent,
/// each level most urgent first.
@MainActor
public final class ListModel: ObservableObject {
    @Published public var selected: String?
    @Published public var hovered: String?
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
        var machines: [String] = []
        var byMachine: [String: [HostPane]] = [:]
        for a in agents {
            if byMachine[a.host_label] == nil { machines.append(a.host_label) }
            byMachine[a.host_label, default: []].append(a)
        }
        return machines.map { m in
            var repos: [String] = []
            var byRepo: [String: [HostPane]] = [:]
            for a in byMachine[m] ?? [] {
                let r = a.repo_label ?? a.repo ?? "No repository"
                if byRepo[r] == nil { repos.append(r) }
                byRepo[r, default: []].append(a)
            }
            return FleetGroup(machine: m, repos: repos.map { FleetGroup.Repo(name: $0, agents: byRepo[$0] ?? []) })
        }
    }

    /// Every row in display order, for keyboard movement.
    public var order: [HostPane] { pinned + groups.flatMap { $0.repos.flatMap(\.agents) } }

    public func move(_ delta: Int) {
        let list = order
        guard !list.isEmpty else { return }
        let i = list.firstIndex { $0.key == selected } ?? (delta > 0 ? -1 : list.count)
        selected = list[max(0, min(list.count - 1, i + delta))].key
        confirmEnd = nil
    }

    public func openSelected() {
        guard let key = selected, let p = fleet.agents.first(where: { $0.key == key }) else { return }
        onOpen(p)
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

    /// End asks first: the row turns into a confirm, and only that ends it.
    public func askEnd(_ p: HostPane) { confirmEnd = p.key }

    public func end(_ p: HostPane) {
        confirmEnd = nil
        Task {
            do { notice = try await actions.end(p) } catch { notice = "End failed: \(error)" }
        }
    }
}
