import AppKit
import ApplicationServices
import CoreGraphics

/// One permission's state, as onboarding shows it.
public enum PermissionState: String, Sendable {
    case granted, denied, notDetermined = "not_determined"

    public var label: String {
        switch self {
        case .granted: "Allowed"
        case .denied: "Off"
        case .notDetermined: "Not asked yet"
        }
    }
}

/// Accessibility: checked before any use (constraint 3), shown in settings,
/// and asked for only from a click.
@MainActor
public protocol Permissions: AnyObject {
    var accessibility: PermissionState { get }
    func openAccessibilitySettings()
}

@MainActor
public final class SystemPermissions: Permissions {
    public init() {}

    /// A double-tap inbox shortcut needs it (a global modifier-key monitor).
    public var accessibility: PermissionState { AXIsProcessTrusted() ? .granted : .notDetermined }

    public func openAccessibilitySettings() {
        if let url = URL(string: "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility") {
            NSWorkspace.shared.open(url)
        }
    }
}

/// The fixtures' permissions: fixed states, and requests do nothing.
@MainActor
public final class FixedPermissions: Permissions {
    public let accessibility: PermissionState

    public init(_ d: [String: String]) {
        func parse(_ s: String?) -> PermissionState {
            s == "authorized" ? .granted : PermissionState(rawValue: s ?? "") ?? .notDetermined
        }
        accessibility = parse(d["accessibility"])
    }

    public func openAccessibilitySettings() {}
}
