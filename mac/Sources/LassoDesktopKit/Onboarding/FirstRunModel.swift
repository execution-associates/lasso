import Combine
import Foundation

/// The first-run panel: connect this Mac to a lasso, sign in when it asks,
/// then a few words on where things are. It shows on the first launch, and
/// again on any launch while this Mac has never reached a lasso and the
/// configured one does not answer. Done (or Open Settings) ends it for good;
/// esc only puts it away, and the inbox brings it back while nothing answers.
@MainActor
public final class FirstRunModel: ObservableObject {
    public enum Step: Equatable, Sendable {
        /// Asking the configured lasso, and this Mac's own, before showing a field.
        case checking
        /// The address field, with examples.
        case connect
        /// The lasso asks for UI_AUTH's user and password.
        case password
        /// The browser sign-in is open.
        case signingIn
        /// Connected: which lasso, and three tips.
        case connected
    }

    public enum Field: Hashable, Sendable { case address, user, password }

    /// The lasso this Mac now uses, as step 3 names it.
    public struct Connected: Equatable, Sendable {
        /// The machine it runs on ("titan").
        public var name: String
        /// Its release ("lasso 6.4.0"), when it said.
        public var version: String?
        /// Where this Mac reaches it ("127.0.0.1:18090").
        public var address: String

        public init(name: String, version: String?, address: String) {
            self.name = name
            self.version = version
            self.address = address
        }
    }

    /// A kind of place a lasso runs, with an address to start from.
    public struct Example: Identifiable, Equatable, Sendable {
        public var id: String
        public var symbol: String
        public var title: String
        public var detail: String
        public var address: String
    }

    /// One thing worth knowing once connected, with its live shortcut.
    public struct Tip: Identifiable, Equatable, Sendable {
        public var id: String
        public var symbol: String
        public var title: String
        public var detail: String
        public var keys: [String]
    }

    /// UserDefaults: the panel was finished (Done or Open Settings).
    public static let doneKey = "onboarded"
    /// UserDefaults: this Mac has had an answer from a lasso (FleetSync).
    public static let connectedKey = "connectedOnce"
    /// lasso's own port on this Mac, asked before showing the field.
    public static let localLasso = URL(string: "http://127.0.0.1:8090")!

    @Published public var step: Step = .checking
    @Published public var address = ""
    @Published public var user = ""
    @Published public var password = ""
    /// Which field has the keyboard (the fields follow it).
    @Published public var focus: Field? = .address
    /// What went wrong, in a sentence, under the field.
    @Published public private(set) var note: String?
    @Published public private(set) var busy = false
    @Published public internal(set) var connected: Connected?
    /// The lasso asking for a password, or being signed in to.
    @Published public private(set) var pending: URL?
    /// The example row under the pointer.
    @Published public var hoveredExample: String?

    let connector: LassoConnector
    let defaults: UserDefaults
    /// The lasso the app is set to use.
    public var configured: () -> URL = { LassoClient.configuredURL }
    /// Whether the lasso in use answers now, with this Mac's credentials.
    public var answers: @MainActor () async -> Bool = { false }
    /// Step 3's words for the lasso at this address, once it answers.
    public var describe: @MainActor (URL) async -> Connected = { Connected(name: "lasso", version: nil, address: LassoConnector.shown($0)) }
    /// A shortcut as set now (Settings › Shortcuts).
    public var shortcut: (ShortcutSlot) -> InboxShortcutChoice = { InboxShortcutChoice.stored($0) }
    /// Put the panel away; open Settings.
    public var onClose: () -> Void = {}
    public var onOpenSettings: () -> Void = {}
    /// Onboarding is over for good (Done, or Open Settings).
    public var onFinished: () -> Void = {}
    /// The header was dragged (true: the drag ended).
    public var onDrag: (Bool) -> Void = { _ in }

    public init(connector: LassoConnector, defaults: UserDefaults = .standard) {
        self.connector = connector
        self.defaults = defaults
    }

    /// Finished, by Done or Open Settings.
    public var finished: Bool { defaults.bool(forKey: Self.doneKey) }
    /// This Mac has reached a lasso at some point.
    public var everConnected: Bool { defaults.bool(forKey: Self.connectedKey) }

    public static let examples: [Example] = [
        Example(
            id: "tailnet", symbol: "point.3.connected.trianglepath.dotted", title: "On your tailnet",
            detail: "No sign-in: the tailnet vouches for this Mac.", address: "titan.your-tailnet.ts.net"),
        Example(
            id: "access", symbol: "lock.shield", title: "Behind Cloudflare Access",
            detail: "You'll sign in in your browser.", address: "lasso.example.com"),
        Example(
            id: "local", symbol: "desktopcomputer", title: "On this Mac",
            detail: "lasso running right here.", address: "127.0.0.1:8090"),
    ]

    /// Where the rail's controls are, with the shortcuts as set now.
    public var tips: [Tip] {
        let inbox = shortcut(.inbox).keys
        let agent = shortcut(.newAgent).keys
        let scratch = shortcut(.newScratch).keys
        return [
            Tip(
                id: "inbox", symbol: "tray", title: "Your inbox",
                detail: "Rest the pointer on the tab at your screen's right edge, then on the tray: what waits on you.",
                keys: inbox.map { [$0] } ?? []),
            Tip(
                id: "new", symbol: "plus", title: "Start an agent",
                detail: (agent == nil ? "Click + on the rail." : "Click + on the rail, or press the keys from any app.") + (scratch.map { " \($0) starts a scratch one." } ?? ""),
                keys: agent.map { [$0] } ?? []),
            Tip(
                id: "settings", symbol: "ellipsis", title: "Settings",
                detail: "The ellipsis at the rail's foot: shortcuts, sounds and this connection.", keys: []),
        ]
    }

    // MARK: steps

    /// Before showing the field, ask quietly: the configured lasso (with
    /// this Mac's credentials), then lasso's own port on this Mac. One that
    /// answers skips straight to step 3.
    public func begin() async {
        step = .checking
        if await answers() {
            await show(configured())
            return
        }
        if configured() != Self.localLasso, case .open = await connector.probe(Self.localLasso, nil) {
            connector.onUse(Self.localLasso)
            await show(Self.localLasso)
            return
        }
        step = .connect
        focus = .address
    }

    /// Connect to the address in the field.
    public func connect() {
        guard !busy, step == .connect else { return }
        busy = true
        note = nil
        let text = address
        Task {
            let r = await connector.connect(text) { [weak self] url in
                self?.pending = url
                self?.step = .signingIn
            }
            await land(r)
        }
    }

    /// Check UI_AUTH's user and password.
    public func submitPassword() {
        guard !busy, step == .password, let url = pending else { return }
        busy = true
        note = nil
        Task { await land(await connector.password(url, user: user, password: password)) }
    }

    /// Stop the browser sign-in and go back to the address.
    public func cancelSignIn() {
        connector.cancel()
        busy = false
        step = .connect
        note = ConnectOutcome.cancelled.message
        focus = .address
    }

    /// From the password back to the address.
    public func back() {
        step = .connect
        note = nil
        password = ""
        focus = .address
    }

    /// A tapped example fills the field, ready to edit.
    public func use(_ e: Example) {
        address = e.address
        note = nil
        focus = .address
    }

    /// ⏎ (from the field or anywhere in the panel): the step's main button.
    public func primary() {
        switch step {
        case .connect: connect()
        case .password:
            // ⏎ in the user field moves on to the password, as a login form does.
            if focus == .user, password.isEmpty { focus = .password } else { submitPassword() }
        case .connected: done()
        case .checking, .signingIn: break
        }
    }

    /// Done: finished for good.
    public func done() {
        defaults.set(true, forKey: Self.doneKey)
        onFinished()
        onClose()
    }

    /// Open Settings: finished too, and Settings takes over.
    public func openSettings() {
        defaults.set(true, forKey: Self.doneKey)
        onFinished()
        onClose()
        onOpenSettings()
    }

    /// esc or ✕: once connected that is Done; before, only put away (it
    /// comes back from the inbox while nothing answers).
    public func dismiss() {
        if step == .connected { return done() }
        if step == .signingIn { connector.cancel() }
        busy = false
        if step == .signingIn { step = .connect }
        onClose()
    }

    private func land(_ r: ConnectOutcome) async {
        defer { busy = false }
        switch r {
        case .connected(let url, _):
            pending = nil
            password = ""
            await show(url)
        case .needsPassword(let url):
            pending = url
            note = nil
            step = .password
            focus = .user
        case .cancelled:
            // cancelSignIn already said so; a late answer changes nothing.
            if step == .signingIn { step = .connect }
        case .failed(let why):
            note = why
            if step == .signingIn { step = .connect }
            if step == .connect { focus = .address }
        }
    }

    private func show(_ url: URL) async {
        connected = await describe(url)
        defaults.set(true, forKey: Self.connectedKey)
        note = nil
        step = .connected
        focus = nil
    }
}
