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

/// Screen recording and accessibility: checked before any use (constraint
/// 3), shown in settings, and asked for only from a click.
@MainActor
public protocol Permissions: AnyObject {
    var screenRecording: PermissionState { get }
    var accessibility: PermissionState { get }
    func requestScreenRecording()
    func openAccessibilitySettings()
}

@MainActor
public final class SystemPermissions: Permissions {
    public init() {}

    /// macOS does not say "never asked" for screen recording; not granted
    /// reads as off.
    public var screenRecording: PermissionState { CGPreflightScreenCaptureAccess() ? .granted : .denied }

    /// Nothing uses it yet; shown so the state is never a surprise.
    public var accessibility: PermissionState { AXIsProcessTrusted() ? .granted : .notDetermined }

    public func requestScreenRecording() {
        if !CGPreflightScreenCaptureAccess() { _ = CGRequestScreenCaptureAccess() }
    }

    public func openAccessibilitySettings() {
        if let url = URL(string: "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility") {
            NSWorkspace.shared.open(url)
        }
    }
}

/// The fixtures' permissions: fixed states, and requests do nothing.
@MainActor
public final class FixedPermissions: Permissions {
    public let screenRecording: PermissionState
    public let accessibility: PermissionState

    public init(_ d: [String: String]) {
        func parse(_ s: String?) -> PermissionState {
            s == "authorized" ? .granted : PermissionState(rawValue: s ?? "") ?? .notDetermined
        }
        screenRecording = parse(d["screen_recording"])
        accessibility = parse(d["accessibility"])
    }

    public func requestScreenRecording() {}
    public func openAccessibilitySettings() {}
}
