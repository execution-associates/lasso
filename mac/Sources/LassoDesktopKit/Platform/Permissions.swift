@preconcurrency import AVFoundation
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

/// Microphone, screen recording and accessibility: checked before any use
/// (constraint 3), shown in onboarding, and asked for only from a click.
@MainActor
public protocol Permissions: AnyObject {
    var microphone: PermissionState { get }
    var screenRecording: PermissionState { get }
    var accessibility: PermissionState { get }
    func requestMicrophone() async -> Bool
    func requestScreenRecording()
    func openAccessibilitySettings()
}

@MainActor
public final class SystemPermissions: Permissions {
    public init() {}

    public var microphone: PermissionState {
        switch AVCaptureDevice.authorizationStatus(for: .audio) {
        case .authorized: .granted
        case .notDetermined: .notDetermined
        default: .denied
        }
    }

    /// macOS does not say "never asked" for screen recording; not granted
    /// reads as off.
    public var screenRecording: PermissionState { CGPreflightScreenCaptureAccess() ? .granted : .denied }

    /// Only a global push-to-talk would need it (PLAN 4.1); the app works without.
    public var accessibility: PermissionState { AXIsProcessTrusted() ? .granted : .notDetermined }

    public func requestMicrophone() async -> Bool {
        switch AVCaptureDevice.authorizationStatus(for: .audio) {
        case .authorized: return true
        case .notDetermined: return await AVCaptureDevice.requestAccess(for: .audio)
        default: return false
        }
    }

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
    public let microphone: PermissionState
    public let screenRecording: PermissionState
    public let accessibility: PermissionState

    public init(_ d: [String: String]) {
        func parse(_ s: String?) -> PermissionState {
            s == "authorized" ? .granted : PermissionState(rawValue: s ?? "") ?? .notDetermined
        }
        microphone = parse(d["microphone"])
        screenRecording = parse(d["screen_recording"])
        accessibility = parse(d["accessibility"])
    }

    public func requestMicrophone() async -> Bool { microphone == .granted }
    public func requestScreenRecording() {}
    public func openAccessibilitySettings() {}
}
