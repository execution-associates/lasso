import AppKit
import Combine
import Foundation

/// Settings (the rail's ellipsis, and the first launch): which lasso, the
/// default bot, and the permissions the app asks for.
@MainActor
public final class SettingsModel: ObservableObject {
    /// Bumped to re-read the permission states after a request.
    @Published public private(set) var tick = 0

    public let permissions: Permissions
    public let setup: BotSetup
    public let baseURL: String
    public var version: () -> VersionInfo?
    public var usableHosts: () -> (usable: Int, total: Int)?
    public var onClose: () -> Void = {}

    static let seenKey = "onboarded"
    /// Settings open by themselves on the first launch only.
    public static var seen: Bool { UserDefaults.standard.bool(forKey: seenKey) }

    public init(
        permissions: Permissions, setup: BotSetup, baseURL: String,
        version: @escaping () -> VersionInfo? = { nil }, usableHosts: @escaping () -> (usable: Int, total: Int)? = { nil }
    ) {
        self.permissions = permissions
        self.setup = setup
        self.baseURL = baseURL
        self.version = version
        self.usableHosts = usableHosts
    }

    public func refresh() { tick += 1 }

    public func allowScreenRecording() {
        permissions.requestScreenRecording()
        tick += 1
    }

    public func close() {
        UserDefaults.standard.set(true, forKey: Self.seenKey)
        onClose()
    }
}
