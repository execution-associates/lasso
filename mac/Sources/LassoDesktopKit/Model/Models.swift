import Foundation

// Wire types, in lasso's own JSON shapes. The Go structs they mirror are named
// on each; a field the server marks omitempty is optional here.

/// One pane in the fleet. src/hostpanes.go:263 (hostPane).
public struct HostPane: Codable, Sendable, Equatable, Identifiable {
    public var host: String
    public var host_label: String
    public var pane_id: String
    public var workspace_id: String
    public var workspace_label: String
    public var tab_id: String
    public var tab_label: String
    public var pane_label: String?
    public var terminal_title: String?
    public var cwd: String
    public var agent: String
    public var agent_status: String
    public var has_agent: Bool
    public var focused: Bool
    /// Unix milliseconds of the transcript's last write.
    public var transcript_at: Int64?
    public var repo: String?
    public var repo_key: String?
    public var repo_label: String?
    public var prompt: String?
    public var agent_id: String?
    public var closed: Bool?

    /// The fleet-wide identity the web client and pinned_agents use:
    /// host + NUL + pane_id (src/web/src/lib/agents.ts).
    public var key: String { host + "\u{0}" + pane_id }
    public var id: String { key }

    /// The display name, by the web client's rule (agents.ts): the workspace
    /// label unless it is "Scratch", then the pane label, tab label, terminal
    /// title, pane id.
    public var displayName: String {
        for s in [workspace_label == "Scratch" ? "" : workspace_label, pane_label ?? "", tab_label,
                  terminal_title ?? ""] where !s.isEmpty {
            return s
        }
        return pane_id
    }

    public var state: AgentState { AgentState(wire: agent_status) }
}

/// The state vocabulary of PLAN 2.3-3/4: one hue per state.
public enum AgentState: String, Sendable, Equatable, CaseIterable {
    case blocked, done, working, idle

    /// herdr's statuses (src/panestatus.go); anything else, "unknown"
    /// included, reads as idle.
    public init(wire: String) {
        self = AgentState(rawValue: wire) ?? .idle
    }

    /// Most urgent first. The web client's priority order (agents.ts:190).
    public var urgency: Int {
        switch self {
        case .blocked: 0
        case .done: 1
        case .working: 2
        case .idle: 3
        }
    }

    public var label: String {
        switch self {
        case .blocked: "needs you"
        case .done: "finished"
        case .working: "working"
        case .idle: "idle"
        }
    }
}

/// GET /api/all-panes. src/hostpanes.go:923.
public struct AllPanes: Codable, Sendable, Equatable {
    public var panes: [HostPane]
    public var errors: [String: String]?
}

/// GET /api/version. src/main.go:1863.
public struct VersionInfo: Codable, Sendable, Equatable {
    public var lasso_protocol: Int
    public var lasso_version: String
    public var herdr_protocol: Int?
    public var herdr_version: String?
    public var compatible: Bool
}

/// GET /api/hosts. src/hosts.go:72 (hostsPayload), :43 (HostInfo).
public struct HostsPayload: Codable, Sendable, Equatable {
    public struct Local: Codable, Sendable, Equatable {
        public var version: String
        public var `protocol`: Int
        public var hostname: String
        public var user: String
    }

    public struct HostInfo: Codable, Sendable, Equatable {
        public var alias: String
        public var hostname: String
        public var user: String
        public var reachable: Bool
        public var running: Bool
        public var version: String
        public var `protocol`: Int
        public var compatible: Bool
        public var err: String?
        public var state: String?
    }

    public var active: String
    public var local: Local
    public var hosts: [HostInfo]
    public var probing: Bool
}

/// The slice of GET /api/ui-state the app reads. src/db.go:276 (uiState).
/// assistant_bot and dismissed_cards are the proposed S1 fields (PLAN 3);
/// their absence is how the app knows the server lacks them.
public struct UIStateSlice: Codable, Sendable, Equatable {
    public var pinned_agents: [String]?
    public var assistant_bot: String?
    public var dismissed_cards: [String: Int64]?
}

/// One bot. src/botruntime.go:215 (botView) over src/bots.go:81 (botRecord);
/// only the fields the app uses.
public struct BotView: Codable, Sendable, Equatable, Identifiable {
    public var id: Int64
    public var host: String
    public var name: String
    public var runtime: String
    public var stopped: Bool
    public var state: String
    public var waiting_for: String?
    public var last_text: String?
    public var last_kind: String?
    public var last_at: String?
    public var error: String?
}

/// GET /api/bots. src/botsapi.go:47.
public struct BotsPayload: Codable, Sendable, Equatable {
    public var bots: [BotView]
}
