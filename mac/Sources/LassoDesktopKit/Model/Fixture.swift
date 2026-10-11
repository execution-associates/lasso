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

    private struct Raw: Decodable {
        var state: String
        var extends: String?
        var now: Date?
        var ui: UI?
        var permissions: [String: String]?
        var server: Server?
        var expect: Expect?
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
        return Fixture(
            state: raw.state, now: now, ui: ui, permissions: raw.permissions ?? base?.permissions ?? [:],
            server: server, expect: raw.expect ?? base?.expect)
    }

    init(state: String, now: Date, ui: UI, permissions: [String: String], server: Server, expect: Expect?) {
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
