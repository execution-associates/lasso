import Foundation

/// A named UI state for the parity harness, read from mac/Spec/fixtures.
/// `server` holds verbatim responses in lasso's JSON shapes, decoded by the
/// same models the live client uses. A fixture may name another in
/// `extends`; its own `ui`, `permissions` and `now` then replace the base's.
public struct Fixture: Decodable, Sendable {
    public struct UI: Decodable, Sendable, Equatable {
        public var rail: String
        public var card: String?
        public var list: Bool?
        public var chat: Bool?
        public var recording: JSONValue?
        /// List panel row keys.
        public var selected: String?
        public var hovered: String?
        /// The bot the chat state talks to.
        public var bot: String?
        public var settings: Bool?
        /// Which Settings tab is open (SettingsTab's raw value).
        public var settings_tab: String?
        /// An agent's hover preview, or its own window (collapsed or not), by pane key.
        public var preview: String?
        public var window: String?
        public var collapsed: Bool?
        /// The card's agent is being killed (lasso has not answered).
        public var killing: Bool?
        /// The New Agent form (⌥⇧O, or ⌘O in a Lasso window).
        public var new_agent: Bool?
        /// The form's open chip menu.
        public var new_agent_menu: String?
        /// The form's mode (NewAgentModel.Kind: "git", "scratch", "terminal").
        public var new_agent_kind: String?
        public var inbox: Bool?
        /// The first-run panel, at a step (FirstRunModel.Step: "connect", "connected").
        public var first_run: String?
        /// No lasso answers (FleetModel.offline): the rail's grey "!".
        public var offline: Bool?
    }

    public struct Server: Decodable, Sendable {
        public var version: VersionInfo
        public var hosts: HostsPayload
        public var all_panes: AllPanes
        public var ui_state: UIStateSlice
        public var bots: BotsPayload
    }

    public struct Expect: Decodable, Sendable {
        public var badge: Int?
        public var rail_slots: [String]?
    }

    public var state: String
    public var now: Date
    public var ui: UI
    public var permissions: [String: String]
    public var server: Server
    public var expect: Expect?
    /// Chat payloads by pane key (host NUL pane_id), for the card and chat states.
    public var chats: [String: ChatPayload]
    /// What Settings' lasso picker found (the settings state).
    /// lasso's notification kinds and this Mac's choices (the settings state).
    public var notify: NotifyPayload?

    private struct Raw: Decodable {
        var state: String
        var extends: String?
        var now: Date?
        var ui: UI?
        var permissions: [String: String]?
        var server: Server?
        var expect: Expect?
        var chats: [String: ChatPayload]?
        var notify: NotifyPayload?
    }

    public static func load(_ name: String, from dir: URL) throws -> Fixture {
        let dec = JSONDecoder()
        dec.dateDecodingStrategy = .iso8601
        let raw = try dec.decode(Raw.self, from: Data(contentsOf: dir.appendingPathComponent("\(name).json")))
        var base: Fixture?
        if let parent = raw.extends {
            base = try load(parent, from: dir)
        }
        guard let ui = raw.ui ?? base?.ui, let server = raw.server ?? base?.server,
              let now = raw.now ?? base?.now
        else {
            throw FixtureError.incomplete(name)
        }
        var fx = Fixture(
            state: raw.state, now: now, ui: ui, permissions: raw.permissions ?? base?.permissions ?? [:],
            server: server, expect: raw.expect ?? base?.expect, chats: (base?.chats ?? [:]).merging(raw.chats ?? [:]) { $1 })
        fx.notify = raw.notify ?? base?.notify
        return fx
    }

    init(
        state: String, now: Date, ui: UI, permissions: [String: String], server: Server, expect: Expect?,
        chats: [String: ChatPayload]
    ) {
        self.chats = chats
        self.state = state
        self.now = now
        self.ui = ui
        self.permissions = permissions
        self.server = server
        self.expect = expect
    }
}

public enum FixtureError: Error, CustomStringConvertible {
    case incomplete(String)

    public var description: String {
        switch self {
        case .incomplete(let n): "fixture \(n) lacks ui, server or now (directly or through extends)"
        }
    }
}
