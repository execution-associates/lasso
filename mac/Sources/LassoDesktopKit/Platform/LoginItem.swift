import Foundation
import ServiceManagement

/// macOS's record of the app as a login item (SMAppService.mainApp).
public enum LoginItemStatus: Sendable, Equatable {
    case enabled, notRegistered, requiresApproval, notFound
}

/// Registering the app to open at login: SMAppService, or a fake in tests
/// and the parity harness.
@MainActor
public protocol LoginItemService: AnyObject {
    var status: LoginItemStatus { get }
    func register() throws
    func unregister() throws
    /// System Settings › General › Login Items.
    func openSystemSettings()
}

@MainActor
public final class SystemLoginItem: LoginItemService {
    public init() {}

    public var status: LoginItemStatus {
        switch SMAppService.mainApp.status {
        case .enabled: .enabled
        case .requiresApproval: .requiresApproval
        case .notFound: .notFound
        default: .notRegistered
        }
    }

    public func register() throws { try SMAppService.mainApp.register() }
    public func unregister() throws { try SMAppService.mainApp.unregister() }
    public func openSystemSettings() { SMAppService.openSystemSettingsLoginItems() }
}

/// A fixed answer, for the parity harness.
@MainActor
public final class FixedLoginItem: LoginItemService {
    public var status: LoginItemStatus
    public init(_ status: LoginItemStatus) { self.status = status }
    public func register() throws { status = .enabled }
    public func unregister() throws { status = .notRegistered }
    public func openSystemSettings() {}
}

/// What Settings › Info's Start at login row shows.
public enum LoginItemState: Sendable, Equatable {
    case on, off
    /// Registered, waiting for the user to allow it in System Settings.
    case needsApproval
    /// A development build: the dev LaunchAgent (Scripts/agent.sh) starts it,
    /// so it never registers itself and minime never runs two.
    case devAgent

    /// The app comes back at the next login.
    public var startsAtLogin: Bool { self != .off }
}

/// Start at login. Read from macOS each time Settings shows (the user can
/// change it in System Settings › General › Login Items too), never cached.
/// The app registers itself once, when onboarding finishes, since the rail
/// is meant to be always there; after that only the switch changes it, so
/// turning it off sticks.
@MainActor
public final class LoginItems {
    /// The one automatic registration has happened (or the user chose by hand).
    public static let autoKey = "loginItemAutoRegistered"

    let service: LoginItemService
    let defaults: UserDefaults
    public let devBuild: Bool

    public init(service: LoginItemService, defaults: UserDefaults = .standard, devBuild: Bool) {
        self.service = service
        self.defaults = defaults
        self.devBuild = devBuild
    }

    /// The app as it runs: SMAppService, and a dev build when Info.plist names no update feed.
    public static func live() -> LoginItems { LoginItems(service: SystemLoginItem(), devBuild: !AppUpdates.configured) }

    public var state: LoginItemState {
        if devBuild { return .devAgent }
        switch service.status {
        case .enabled: return .on
        case .requiresApproval: return .needsApproval
        case .notRegistered, .notFound: return .off
        }
    }

    /// Onboarding finished: register once. Answers a notice when it failed.
    @discardableResult
    public func autoRegisterOnce() -> String? {
        guard !devBuild, !defaults.bool(forKey: Self.autoKey) else { return nil }
        defaults.set(true, forKey: Self.autoKey)
        guard service.status != .enabled else { return nil }
        do {
            try service.register()
            return nil
        } catch {
            return "Could not add Lasso to your login items: \(error.localizedDescription)"
        }
    }

    /// The switch. Answers a notice when macOS refused.
    @discardableResult
    public func set(_ on: Bool) -> String? {
        guard !devBuild else { return nil }
        defaults.set(true, forKey: Self.autoKey)
        do {
            if on { try service.register() } else { try service.unregister() }
            return nil
        } catch {
            return "Could not turn Start at login \(on ? "on" : "off"): \(error.localizedDescription)"
        }
    }

    public func openSystemSettings() { service.openSystemSettings() }
}
