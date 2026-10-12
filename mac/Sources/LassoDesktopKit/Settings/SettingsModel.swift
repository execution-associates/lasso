import UniformTypeIdentifiers
import AppKit
import Combine
import Foundation

/// The lasso this Mac uses, and what it said when asked.
public struct LassoChoice: Identifiable, Equatable, Sendable, Codable {
    public var url: String
    public var version: VersionInfo?
    /// The machine lasso runs on ("titan"), when known.
    public var name: String?
    public var id: String { url }

    public init(url: String, version: VersionInfo?, name: String? = nil) {
        self.url = url
        self.version = version
        self.name = name
    }

    /// The machine it runs on, else plain "lasso".
    public var title: String { name ?? "lasso" }

    /// Its version (the release number, never a build's "(dev)" tag) and
    /// where this Mac reaches it; "Not answering" when it did not.
    public var detail: String {
        let here = URL(string: url).map { u in u.host.map { h in u.port.map { "\(h):\($0)" } ?? h } ?? url } ?? url
        guard let v = version else { return "Not answering · \(here)" }
        let release = v.lasso_version.range(of: #"^\d+\.\d+\.\d+"#, options: .regularExpression).map { String(v.lasso_version[$0]) }
        return "lasso \(release ?? v.lasso_version) · \(here)"
    }
}

/// Settings' tabs.
public enum SettingsTab: String, CaseIterable, Sendable {
    case notifications, shortcuts, info

    public var title: String {
        switch self {
        case .notifications: "Notifications"
        case .shortcuts: "Shortcuts"
        case .info: "Info"
        }
    }
}

/// Settings (the rail's ellipsis, and the first launch): the lasso this Mac
/// uses (one; a dev lasso is `dev.sh use`, not a choice here), and the
/// permissions the app asks for.
@MainActor
public final class SettingsModel: ObservableObject {
    /// Bumped to re-read the permission states after a request.
    @Published public private(set) var tick = 0

    /// Each shortcut, and which one the next key press records.
    @Published public private(set) var shortcuts: [ShortcutSlot: InboxShortcutChoice] =
        Dictionary(uniqueKeysWithValues: ShortcutSlot.allCases.map { ($0, InboxShortcutChoice.stored($0)) })
    @Published public private(set) var recordingSlot: ShortcutSlot?
    public var shortcut: InboxShortcutChoice { shortcuts[.inbox] ?? ShortcutSlot.inbox.fallback }
    public var recording: Bool { recordingSlot == .inbox }
    /// The arrival sound's id, or InboxSound.none.
    @Published public private(set) var sound = InboxSound.current

    public let permissions: Permissions
    public var baseURL: () -> String
    /// A shortcut changed: watch for the new one.
    public var onShortcut: (ShortcutSlot, InboxShortcutChoice) -> Void = { _, _ in }
    public var version: () -> VersionInfo?
    public var usableHosts: () -> (usable: Int, total: Int)?
    public var onClose: () -> Void = {}
    /// lasso's notification kinds and this Mac's choices (nil: lasso has none).
    public var notify: () -> NotifyPayload? = { nil }
    /// Choose whether a kind may interrupt this Mac.
    public var chooseNotify: ((String, Bool) async throws -> Void)?
    /// Switch the app to another lasso.
    public var onUse: (URL) -> Void = { _ in }
    /// A sign-in finished for this lasso: use it, with its tokens.
    public var onSignedIn: (URL, OAuthGrant) -> Void = { _, _ in }
    /// Forget the lasso in use's tokens.
    public var onSignOut: () -> Void = {}
    /// The window the sign-in sheet hangs from.
    public var signInAnchor: () -> NSWindow? = { nil }

    @Published public var tab: SettingsTab = .notifications
    /// The address field is open (Change…, or a failed connect).
    @Published public var changingLasso = false
    /// The machine lasso runs on, for the connection row.
    public var lassoName: () -> String? = { nil }

    /// The URL field: any lasso by its address (a tailnet name, a
    /// Cloudflare hostname, a loopback port).
    @Published public var urlDraft = ""
    /// What the last Connect or sign-in found, in a sentence.
    @Published public private(set) var connectNote: String?
    @Published public private(set) var connecting = false

    /// The lasso in use holds sign-in tokens on this Mac.
    public var signedIn: Bool { _ = tick; return URL(string: baseURL()).flatMap(TokenStore.read) != nil }

    /// Connect to the lasso in the URL field: ask it what it needs, then use
    /// it, signing in first when it asks for that.
    public func connect() {
        var text = urlDraft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty, !connecting else { return }
        if !text.contains("://") { text = (text.hasPrefix("127.") || text.hasPrefix("localhost") ? "http://" : "https://") + text }
        guard let url = URL(string: text), url.host != nil else {
            connectNote = "That is not an address."
            return
        }
        connecting = true
        connectNote = nil
        Task {
            defer { connecting = false }
            switch await LassoAuth.probe(url) {
            case .open:
                onUse(url)
                connectNote = "Connected. This lasso needs no sign-in from here."
                urlDraft = ""
                changingLasso = false
                tick += 1
            case .signIn(let metadata):
                await signIn(url, metadata)
            case .password:
                connectNote = "This lasso asks for its UI_AUTH password (Keychain item ui-auth)."
            case .unreachable(let why):
                connectNote = "No answer from \(url.host ?? text): \(why)"
            }
        }
    }

    /// Show the lasso connection (Info), as a sign-out does.
    public func showConnection() { tab = .info }

    /// The lasso in use stopped taking its token: sign in to it again.
    public func signInAgain() {
        guard !connecting, let url = AuthState.shared.expired ?? URL(string: baseURL()) else { return }
        connecting = true
        Task {
            defer { connecting = false }
            guard case .signIn(let metadata) = await LassoAuth.probe(url) else {
                connectNote = "This lasso no longer asks for a sign-in."
                AuthState.shared.signedIn()
                return
            }
            await signIn(url, metadata)
        }
    }

    private func signIn(_ url: URL, _ metadata: URL) async {
        connectNote = "Finish signing in in the window that opened."
        do {
            let g = try await OAuthSignIn(base: url, metadata: metadata, anchor: signInAnchor()).run()
            TokenStore.write(g, for: url)
            onSignedIn(url, g)
            AuthState.shared.signedIn()
            connectNote = "Signed in."
            urlDraft = ""
            changingLasso = false
        } catch {
            connectNote = String(describing: error)
        }
        tick += 1
    }

    public func signOut() {
        onSignOut()
        connectNote = "Signed out. This Mac holds no token for this lasso."
        tick += 1
    }


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

    public func refresh() { tick += 1 }

    /// Set only when the user has just picked a shortcut that needs
    /// Accessibility and it is not granted: Settings says so then, and only then.
    @Published public private(set) var needsAccessibility = false

    public func choose(_ c: InboxShortcutChoice, for slot: ShortcutSlot = .inbox) {
        recordingSlot = nil
        shortcuts[slot] = c
        InboxShortcutChoice.store(c, slot)
        onShortcut(slot, c)
        needsAccessibility = c.needsAccessibility && permissions.accessibility != .granted
    }

    public func startRecording(_ slot: ShortcutSlot = .inbox) { recordingSlot = slot }

    /// The settings panel's key handler, while recording: the next
    /// combination becomes that shortcut, esc gives up. True when it took the key.
    public func record(_ e: NSEvent) -> Bool {
        guard let slot = recordingSlot else { return false }
        if Keys.esc.matches(e) {
            recordingSlot = nil
        } else if let c = InboxShortcutChoice.combo(from: e) {
            choose(c, for: slot)
        }
        return true
    }

    /// The sound uploaded to lasso, if any, and how to change it (nil when
    /// the app runs from a fixture).
    public var customSound: () -> DesktopSound? = { nil }
    public var upload: ((URL) async throws -> Void)?
    public var removeUpload: (() async throws -> Void)?
    @Published public private(set) var soundNotice: String?

    /// Choose an audio file and upload it as lasso's arrival sound: every Mac
    /// on this lasso gets it, cached, and this one plays it from now on.
    public func pickSound() {
        guard let upload else { return }
        let panel = NSOpenPanel()
        panel.allowedContentTypes = [.audio]
        panel.allowsMultipleSelection = false
        panel.message = "Choose a short sound (under 1 MB) to play when something reaches the inbox"
        NSApp.activate(ignoringOtherApps: true)
        guard panel.runModal() == .OK, let url = panel.url else { return }
        soundNotice = "Uploading…"
        Task {
            do {
                try await upload(url)
                soundNotice = nil
                choose(sound: InboxSound.custom)
            } catch {
                soundNotice = "Upload failed: \(error)"
            }
        }
    }

    public func removeSound() {
        guard let removeUpload else { return }
        Task {
            do {
                try await removeUpload()
                if sound == InboxSound.custom { choose(sound: InboxSound.fallback) }
                soundNotice = nil
            } catch {
                soundNotice = "Could not remove it: \(error)"
            }
        }
    }

    public func setNotify(_ kind: String, _ on: Bool) {
        guard let chooseNotify else { return }
        Task {
            do {
                try await chooseNotify(kind, on)
                tick += 1
            } catch {
                soundNotice = "Could not save that: \(error)"
            }
        }
    }

    /// Pick the arrival sound, and play it so the choice is heard.
    public func choose(sound id: String) {
        sound = id
        InboxSound.current = id
        InboxSound.play(id, force: true)
    }

    public func close() {
        UserDefaults.standard.set(true, forKey: Self.seenKey)
        onClose()
    }
}
