import Foundation

/// The app's updater, plugged in by the executable (Sparkle lives there, so
/// the kit, its tests and the parity harness never link it). Nil in a dev
/// build, whose Info.plist names no feed: it never updates itself.
@MainActor
public enum AppUpdates {
    public static var checkNow: (() -> Void)?

    /// This build's version, as Settings shows it.
    public static var version: String {
        let i = Bundle.main.infoDictionary ?? [:]
        return (i["CFBundleShortVersionString"] as? String) ?? "dev"
    }

    /// A release build: Info.plist names a feed and the key its updates are signed with.
    public static var configured: Bool {
        let i = Bundle.main.infoDictionary ?? [:]
        let feed = (i["SUFeedURL"] as? String) ?? ""
        let key = (i["SUPublicEDKey"] as? String) ?? ""
        return feed.hasPrefix("https://") && !key.isEmpty && !key.hasPrefix("@")
    }
}
