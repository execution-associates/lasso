import AppKit
import Combine
import Foundation

/// A lasso Settings can switch to, and what it said when asked.
public struct LassoChoice: Identifiable, Equatable, Sendable, Codable {
    public var url: String
    public var version: VersionInfo?
    public var id: String { url }

    public init(url: String, version: VersionInfo?) {
        self.url = url
        self.version = version
    }

    public var title: String {
        guard let v = version else { return "Not answering" }
        return (v.dev == true ? "Dev lasso " : "lasso ") + v.lasso_version
    }

    /// Where it is: this Mac's address, and a dev lasso's own bound one.
    public var detail: String {
        let here = URL(string: url).map { "\($0.host ?? ""):\($0.port.map(String.init) ?? "")" } ?? url
        guard let listen = version?.listen, !listen.isEmpty else { return here }
        return "\(here) → \(listen)"
    }
}

/// Settings (the rail's ellipsis, and the first launch): which lasso, and
/// the permissions the app asks for.
@MainActor
public final class SettingsModel: ObservableObject {
    /// Bumped to re-read the permission states after a request.
    @Published public private(set) var tick = 0
    /// Every lasso that answered, plus the one in use whether or not it did.
    @Published public private(set) var lassos: [LassoChoice] = []

    /// The inbox shortcut, and whether the next key press records one.
    @Published public private(set) var shortcut = InboxShortcutChoice.stored
    @Published public private(set) var recording = false
    /// The arrival sound's id, or InboxSound.none.
    @Published public private(set) var sound = InboxSound.current

    public let permissions: Permissions
    public var baseURL: () -> String
    /// The shortcut changed: watch for the new one.
    public var onShortcut: (InboxShortcutChoice) -> Void = { _ in }
    public var version: () -> VersionInfo?
    public var usableHosts: () -> (usable: Int, total: Int)?
    public var onClose: () -> Void = {}
    /// Switch the app to another lasso.
    public var onUse: (URL) -> Void = { _ in }
    /// Asks a lasso for /api/version; nil when the app runs from a fixture.
    public var probe: (@Sendable (URL) async -> VersionInfo?)?

    static let seenKey = "onboarded"
    /// Settings open by themselves on the first launch only.
    public static var seen: Bool { UserDefaults.standard.bool(forKey: seenKey) }

    public init(
        permissions: Permissions, baseURL: @escaping () -> String,
        version: @escaping () -> VersionInfo? = { nil }, usableHosts: @escaping () -> (usable: Int, total: Int)? = { nil }
    ) {
        self.permissions = permissions
        self.baseURL = baseURL
        self.version = version
        self.usableHosts = usableHosts
    }

    public func refresh() {
        tick += 1
        Task { await scan() }
    }

    /// The fixtures' list, in place of a scan.
    public func show(lassos: [LassoChoice]) { self.lassos = lassos }

    /// Ask every candidate lasso (LassoClient.candidates) at once.
    public func scan() async {
        guard let probe else { return }
        let current = baseURL()
        var urls = LassoClient.candidates
        if let u = URL(string: current), !urls.contains(u) { urls.insert(u, at: 0) }
        let found = await withTaskGroup(of: (Int, LassoChoice).self) { group in
            for (i, u) in urls.enumerated() {
                group.addTask { (i, LassoChoice(url: u.absoluteString, version: await probe(u))) }
            }
            var out: [(Int, LassoChoice)] = []
            for await r in group { out.append(r) }
            return out.sorted { $0.0 < $1.0 }.map(\.1)
        }
        lassos = found.filter { $0.version != nil || $0.url == current }
    }

    public func use(_ choice: LassoChoice) {
        guard let url = URL(string: choice.url), choice.url != baseURL() else { return }
        onUse(url)
        tick += 1
        Task { await scan() }
    }

    /// Set only when the user has just picked a shortcut that needs
    /// Accessibility and it is not granted: Settings says so then, and only then.
    @Published public private(set) var needsAccessibility = false

    public func choose(_ c: InboxShortcutChoice) {
        recording = false
        shortcut = c
        InboxShortcutChoice.stored = c
        onShortcut(c)
        needsAccessibility = c.needsAccessibility && permissions.accessibility != .granted
    }

    public func startRecording() { recording = true }

    /// The settings panel's key handler, while recording: the next
    /// combination becomes the shortcut, esc gives up. True when it took the key.
    public func record(_ e: NSEvent) -> Bool {
        guard recording else { return false }
        if Keys.esc.matches(e) {
            recording = false
        } else if let c = InboxShortcutChoice.combo(from: e) {
            choose(c)
        }
        return true
    }

    /// Pick the arrival sound, and play it so the choice is heard.
    public func choose(sound id: String) {
        sound = id
        InboxSound.current = id
        InboxSound.play(id, force: true)
    }

    public func allowScreenRecording() {
        permissions.requestScreenRecording()
        tick += 1
    }

    public func close() {
        UserDefaults.standard.set(true, forKey: Self.seenKey)
        onClose()
    }
}
