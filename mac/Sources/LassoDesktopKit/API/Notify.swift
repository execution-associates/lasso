import AppKit
import Foundation

// lasso's notification kinds, chosen per device (src/notifydevices.go,
// docs/design/notifications.md): this Mac is one device, and its choices
// decide what interrupts it here (the arrival sound), never what the inbox
// lists. A lasso without the routes answers 404 (or its web app), and the
// app then behaves as it did before kinds existed: every arrival sounds.

/// One kind a device can choose (notify.go notifKindInfo).
public struct NotifyKind: Codable, Sendable, Equatable, Identifiable {
    public var id: String
    public var label: String
    public var description: String
    public var `default`: Bool
}

/// GET /api/notify?device=: the catalog and this device's resolved choices.
public struct NotifyPayload: Codable, Sendable, Equatable {
    public struct Device: Codable, Sendable, Equatable {
        public var id: String
        public var label: String?
        public var wants: [String: Bool]
    }

    public var kinds: [NotifyKind]
    public var device: Device

    /// Whether this device takes `kind`: its choice, else the kind's default.
    public func wants(_ kind: String) -> Bool {
        device.wants[kind] ?? kinds.first { $0.id == kind }?.default ?? true
    }
}

/// The SSE `notification` event (notifylive.go liveNotification).
public struct LiveNotification: Codable, Sendable, Equatable {
    public var id: String
    public var at: Int64
    public var kind: String
    public var level: String?
    public var title: String
    public var body: String?
    public var tag: String?
    public var host: String?
    public var url: String?
    public var icon: String?
}

/// The kinds the app maps its own arrivals to.
public enum NotifyKinds {
    public static let agentBlocked = "agent_blocked"
    public static let agentFinished = "agent_finished"
    public static let agentMessage = "agent_message"
    public static let botMessage = "bot_message"
    public static let lassoNotice = "lasso_notice"
}

/// This Mac as a device: a stable id (8–64 of [A-Za-z0-9_-]) kept in
/// UserDefaults, and a label naming the machine.
public enum NotifyDevice {
    static let key = "notifyDevice"

    public static var id: String {
        if let id = UserDefaults.standard.string(forKey: key) { return id }
        let alphabet = Array("abcdefghijklmnopqrstuvwxyz0123456789")
        let id = "mac-" + String((0..<16).map { _ in alphabet.randomElement()! })
        UserDefaults.standard.set(id, forKey: key)
        return id
    }

    public static var label: String { "LassoDesktop on \(Host.current().localizedName ?? "this Mac")" }

    struct Write: Encodable {
        var id: String
        var label: String?
        var kinds: [String: Bool?]?
    }

    /// The catalog and this device's choices; nil when lasso has no kinds.
    public static func load(_ client: LassoClient) async -> NotifyPayload? {
        try? await client.get("/api/notify", query: ["device": id], as: NotifyPayload.self)
    }

    /// Name this device on lasso (so its device list says what it is).
    public static func register(_ client: LassoClient) async {
        struct Reply: Decodable {}
        let _: Reply? = try? await client.post("/api/notify/device", json: Write(id: id, label: label, kinds: nil))
    }

    /// Choose a kind for this device: true or false, or nil for the default.
    public static func choose(_ kind: String, _ on: Bool?, client: LassoClient) async throws {
        struct Reply: Decodable {}
        let _: Reply = try await client.post("/api/notify/device", json: Write(id: id, label: nil, kinds: [kind: on]))
    }
}
