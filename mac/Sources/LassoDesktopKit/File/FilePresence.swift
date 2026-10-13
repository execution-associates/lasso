import AppKit
import CoreGraphics

// Every Mac app connected to a lasso hears an agent's `open-file`, and only
// the one whose person is at it opens a window (PLAN 26): input within
// `file.presence`'s idle_s and the screen unlocked. A Mac nobody is at (an
// idle mini on the desk, a locked laptop) opens nothing.

/// Whether someone is at this Mac now.
@MainActor
public protocol UserPresence: AnyObject {
    var isPresent: Bool { get }
}

/// The Mac's own word: HID input recently, a session on the console whose
/// screen is not locked. The lock and screensaver notifications are a
/// backstop for the session dictionary, which lags a lock by a moment.
@MainActor
public final class SystemPresence: UserPresence {
    let idleLimit: TimeInterval
    private var locked = false
    private var saver = false
    nonisolated(unsafe) private var watches: [NSObjectProtocol] = []

    public init(idleLimit: TimeInterval = Theme.File.presence.number("idle_s")) {
        self.idleLimit = idleLimit
        let dnc = DistributedNotificationCenter.default()
        for name in ["com.apple.screenIsLocked", "com.apple.screenIsUnlocked", "com.apple.screensaver.didstart", "com.apple.screensaver.didstop"] {
            watches.append(
                dnc.addObserver(forName: Notification.Name(name), object: nil, queue: .main) { [weak self] n in
                    let name = n.name.rawValue
                    MainActor.assumeIsolated { self?.heard(name) }
                })
        }
    }

    private func heard(_ name: String) {
        switch name {
        case "com.apple.screenIsLocked": locked = true
        case "com.apple.screenIsUnlocked": locked = false
        case "com.apple.screensaver.didstart": saver = true
        case "com.apple.screensaver.didstop": saver = false
        default: break
        }
    }

    deinit {
        for w in watches { DistributedNotificationCenter.default().removeObserver(w) }
    }

    /// Seconds since the last keyboard, pointer or trackpad input from any
    /// app (kCGAnyInputEventType, which Swift's CGEventType has no case for).
    public static var idleSeconds: TimeInterval {
        CGEventSource.secondsSinceLastEventType(.hidSystemState, eventType: unsafeBitCast(UInt32.max, to: CGEventType.self))
    }

    /// The login session's own state: on the console, and not locked.
    public static var sessionActive: Bool {
        guard let d = CGSessionCopyCurrentDictionary() as? [String: Any] else { return false }
        let onConsole = (d[kCGSessionOnConsoleKey] as? Bool) ?? false
        let locked = (d["CGSSessionScreenIsLocked"] as? Bool) ?? false
        return onConsole && !locked
    }

    public var isPresent: Bool {
        !locked && !saver && Self.sessionActive && Self.idleSeconds < idleLimit
    }
}

/// A presence that says what it is told (tests, the harness).
@MainActor
public final class FixedPresence: UserPresence {
    public var isPresent: Bool

    public init(_ present: Bool) { isPresent = present }
}
