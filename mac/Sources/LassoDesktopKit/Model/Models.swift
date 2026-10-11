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
    /// The agent's custom label (`herdr agent rename`).
    public var agent_label: String?
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

    /// What to call the agent: its herdr label, else its tab's name (herdr's
    /// custom agent labels, herdr.dev/docs/agents), else the pane's own label
    /// or title. Never the workspace: one workspace holds several agents, and
    /// it is what the list groups them by.
    public var displayName: String {
        for s in [agent_label ?? "", tab_label, pane_label ?? "", terminal_title ?? ""] where !s.isEmpty {
            return s
        }
        return pane_id
    }

    /// The group the agent is listed under: its workspace.
    public var group: String { workspace_label.isEmpty ? workspace_id : workspace_label }

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
    /// A `-dev` lasso (`mise run dev`), and the address it bound.
    public var dev: Bool?
    public var listen: String?
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
/// dismissed_cards is the S1 field (PLAN 3);
/// their absence is how the app knows the server lacks them.
public struct UIStateSlice: Codable, Sendable, Equatable {
    public var pinned_agents: [String]?
    public var dismissed_cards: [String: Int64]?
    /// The uploaded arrival sound, if any (src/desktopsound.go).
    public var desktop_sound: DesktopSound?
}

/// ui_state.desktop_sound: what is stored at /api/desktop/sound.
public struct DesktopSound: Codable, Sendable, Equatable {
    public var name: String
    public var sha256: String
    public var size: Int64
    public var content_type: String
    public var updated_at: Int64
}

/// One bot. src/botruntime.go:215 (botView) over src/bots.go:81 (botRecord);
/// only the fields the app uses.
public struct BotView: Codable, Sendable, Equatable, Identifiable {
    public var id: Int64
    public var host: String
    public var name: String
    public var runtime: String
    public var stopped: Bool
    public var model: String?
    public var state: String
    /// A claude bot's herdr pane, while it runs (its chat is that pane's).
    public var pane_id: String?
    public var dir_path: String?
    public var live_model: LiveModel?
    /// What a blocked omp bot waits on, the same list its chat carries.
    public var pending_ui: [ChatPayload.PendingUI]?
    public var waiting_for: String?
    public var last_text: String?
    public var last_kind: String?
    public var last_at: String?
    public var error: String?
}

public struct LiveModel: Codable, Sendable, Equatable {
    public var id: String?
    public var name: String?
}

/// GET /api/bots. src/botsapi.go:47.
public struct BotsPayload: Codable, Sendable, Equatable {
    public var bots: [BotView]
}

/// GET /api/chat and GET /api/bots/<name>/chat. src/chatview.go:190
/// (chatPayload), :64 (chatItem), :95 (chatTool); src/botomp.go:107 (pending_ui).
public struct ChatPayload: Codable, Sendable, Equatable {
    public struct Unavailable: Codable, Sendable, Equatable {
        public var reason: String
        public var checked: [String]?
        public var unanswered: [String]?
    }

    /// An omp bot's open question or approval (select/confirm/input/editor).
    public struct PendingUI: Codable, Sendable, Equatable, Identifiable {
        public var id: String
        public var method: String
        public var title: String?
        public var message: String?
        public var options: [String]?
        public var placeholder: String?
    }

    public var pane_id: String
    public var agent: String
    public var host: String
    public var title: String?
    public var model: String?
    public var cwd: String?
    public var path: String?
    public var start_offset: Int64?
    public var items: [ChatItem]
    public var tokens: Int?
    public var running: Bool?
    public var more: Bool?
    public var note: String?
    public var starting: Bool?
    public var served_by: String?
    public var unavailable: Unavailable?
    public var pending_ui: [PendingUI]?
}

public struct ChatItem: Codable, Sendable, Equatable, Identifiable {
    public var kind: String
    public var id: String
    public var at: String?
    public var text: String?
    public var thinking: Bool?
    public var marker: String?
    public var count: Int?
    public var tool: ChatTool?
    public var source: String?
    /// What the sender said about a bot message (S3): input, engine, model.
    public var meta: [String: String]?
}

public struct ChatTool: Codable, Sendable, Equatable {
    public struct DiffLine: Codable, Sendable, Equatable {
        public var kind: String
        public var text: String
    }

    public struct Ask: Codable, Sendable, Equatable {
        public var questions: [Question]
    }

    public struct Question: Codable, Sendable, Equatable {
        public struct Option: Codable, Sendable, Equatable {
            public var label: String
            public var description: String?
            public var preview: String?
        }

        public var header: String?
        public var question: String?
        public var options: [Option]?
        public var multi: Bool?
        public var recommended: Int?
        public var selected: [String]?
        public var custom: String?
    }

    public var call_id: String?
    public var name: String?
    public var title: String?
    public var family: String?
    public var group: String?
    public var subject: String?
    public var command: String?
    public var state: String?
    public var result_line: String?
    public var output: String?
    public var diff: [DiffLine]?
    public var error: String?
    public var images: Int?
    public var duration_ms: Int64?
    public var ask: Ask?
}

/// One `active` frame of /api/events (src/main.go:1069): the fields the app
/// reacts to. A changed rev means "refetch that resource".
public struct ActiveFrame: Codable, Sendable, Equatable {
    public var panes_rev: Int64?
    public var ui_state_rev: Int64?
    public var plugins_rev: Int64?
    public var herdr_up: Bool?
    public var host: String?
}
