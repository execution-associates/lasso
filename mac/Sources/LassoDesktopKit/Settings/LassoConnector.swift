import Foundation

/// What connecting to a lasso came to. Settings › Info and the first-run
/// panel both show `message`, so they say the same thing.
public enum ConnectOutcome: Equatable, Sendable {
    /// It answered, and the app uses it from now on.
    case connected(URL, How)
    /// It asks for UI_AUTH's user and password.
    case needsPassword(URL)
    /// The user gave up on the sign-in.
    case cancelled
    /// No use, and why, in a sentence.
    case failed(String)

    public enum How: Sendable, Equatable {
        /// Nothing to send: loopback, or the tailnet's whois.
        case open
        /// A browser sign-in gave it tokens.
        case signedIn
        /// UI_AUTH's user and password, kept in the Keychain.
        case password
    }

    public var message: String {
        switch self {
        case .connected(_, .open): "Connected. This lasso needs no sign-in from here."
        case .connected(_, .signedIn): "Signed in."
        case .connected(_, .password): "Signed in with its password."
        case .needsPassword: "This lasso asks for a user and password."
        case .cancelled: "Sign-in was cancelled."
        case .failed(let why): why
        }
    }
}

/// Connecting this Mac to a lasso by its address: ask it what it needs, then
/// use it, signing in first (in the browser, or with UI_AUTH's password) when
/// it asks for that. Settings › Info's Change… and the first-run panel share
/// it, so the two cannot drift. The network and the Keychain are injected:
/// the app passes the live ones (`LassoConnector.live`), tests canned ones.
@MainActor
public final class LassoConnector {
    /// The anonymous probe, or one with UI_AUTH's "user:pass".
    public typealias Probe = @MainActor (_ base: URL, _ basicAuth: String?) async -> LassoAccess
    /// The browser sign-in, from the lasso's protected-resource metadata.
    public typealias SignIn = @MainActor (_ base: URL, _ metadata: URL) async throws -> OAuthGrant

    let probe: Probe
    let signIn: SignIn
    /// Stops a sign-in in progress (its `signIn` throws `cancelled`).
    public var cancelSignIn: () -> Void = {}
    /// Where a sign-in's tokens and a password are kept (the Keychain).
    public var keepGrant: (OAuthGrant, URL) -> Void = { TokenStore.write($0, for: $1) }
    public var keepPassword: (String) -> Void = { Keychain.write(.uiAuth, $0) }
    /// Use this lasso from now on, with nothing to send.
    public var onUse: (URL) -> Void = { _ in }
    /// A sign-in finished for this lasso: use it, with its tokens.
    public var onSignedIn: (URL, OAuthGrant) -> Void = { _, _ in }
    /// A password worked for this lasso: send it from now on, and use it.
    public var onPassword: (URL, String) -> Void = { _, _ in }
    /// Counts connects, so one the user walked away from cannot land late.
    private var attempt = 0

    public init(probe: @escaping Probe, signIn: @escaping SignIn) {
        self.probe = probe
        self.signIn = signIn
    }

    /// The address typed, as a URL: a bare hostname means https, loopback
    /// and bare IP addresses (a tailnet's 100.x) mean http. nil: not one.
    nonisolated public static func address(_ text: String) -> (url: URL, guessed: Bool)? {
        var t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        while t.hasSuffix("/") { t.removeLast() }
        guard !t.isEmpty, !t.contains(" ") else { return nil }
        var guessed = false
        if !t.contains("://") {
            let host = t.split(separator: ":").first.map(String.init) ?? t
            let ip = host.split(separator: ".").count == 4 && host.allSatisfy { $0.isNumber || $0 == "." }
            let plain = host == "localhost" || host.hasPrefix("127.") || ip
            t = (plain ? "http://" : "https://") + t
            guessed = !plain
        }
        guard let url = URL(string: t), let scheme = url.scheme, ["http", "https"].contains(scheme), url.host?.isEmpty == false
        else { return nil }
        return (url, guessed)
    }

    /// The host (and port) an address names, for sentences.
    nonisolated public static func shown(_ url: URL) -> String {
        guard let h = url.host else { return url.absoluteString }
        return url.port.map { "\(h):\($0)" } ?? h
    }

    /// Connect to the lasso at the typed address. `signingIn` is told when
    /// the browser sign-in starts, so the caller can say so.
    public func connect(_ text: String, signingIn: (URL) -> Void = { _ in }) async -> ConnectOutcome {
        guard let a = Self.address(text) else {
            return .failed("That is not an address. Try a hostname, like lasso.example.com.")
        }
        let (url, guessed) = (a.url, a.guessed)
        attempt += 1
        let mine = attempt
        var access = await probe(url, nil)
        var target = url
        // A bare name was given https. A tailnet listener often speaks plain
        // http, so ask that once before giving up.
        if case .unreachable = access, guessed, var c = URLComponents(url: url, resolvingAgainstBaseURL: false) {
            c.scheme = "http"
            if let plain = c.url {
                let second = await probe(plain, nil)
                if case .unreachable = second {} else {
                    access = second
                    target = plain
                }
            }
        }
        guard mine == attempt else { return .cancelled }
        return await follow(access, target, signingIn: signingIn, attempt: mine)
    }

    /// The lasso in use stopped taking its token: sign in to it again.
    public func signInAgain(_ url: URL, signingIn: (URL) -> Void = { _ in }) async -> ConnectOutcome {
        attempt += 1
        let mine = attempt
        guard case .signIn(let metadata) = await probe(url, nil) else {
            AuthState.shared.signedIn()
            return .failed("This lasso no longer asks for a sign-in.")
        }
        guard mine == attempt else { return .cancelled }
        signingIn(url)
        return await runSignIn(url, metadata, attempt: mine)
    }

    /// Check UI_AUTH's user and password against the lasso, and keep them
    /// (Keychain) and send them from now on only when it lets them in.
    public func password(_ url: URL, user: String, password: String) async -> ConnectOutcome {
        let u = user.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !u.isEmpty, !password.isEmpty else { return .failed("Enter the user and the password.") }
        guard !u.contains(":") else { return .failed("A UI_AUTH user has no colon in it.") }
        attempt += 1
        let mine = attempt
        let pair = "\(u):\(password)"
        let access = await probe(url, pair)
        guard mine == attempt else { return .cancelled }
        switch access {
        case .open:
            keepPassword(pair)
            onPassword(url, pair)
            return .connected(url, .password)
        case .password:
            return .failed("That user and password did not work.")
        case .signIn, .unreachable:
            return Self.failure(access, url)
        }
    }

    /// Give up on what is in progress: a sign-in's window closes, and a
    /// probe still out is ignored when it answers.
    public func cancel() {
        attempt += 1
        cancelSignIn()
    }

    private func follow(_ access: LassoAccess, _ url: URL, signingIn: (URL) -> Void, attempt mine: Int) async -> ConnectOutcome {
        switch access {
        case .open:
            onUse(url)
            return .connected(url, .open)
        case .signIn(let metadata):
            signingIn(url)
            return await runSignIn(url, metadata, attempt: mine)
        case .password:
            return .needsPassword(url)
        case .unreachable:
            return Self.failure(access, url)
        }
    }

    private func runSignIn(_ url: URL, _ metadata: URL, attempt mine: Int) async -> ConnectOutcome {
        do {
            let g = try await signIn(url, metadata)
            guard mine == attempt else { return .cancelled }
            keepGrant(g, url)
            onSignedIn(url, g)
            AuthState.shared.signedIn()
            return .connected(url, .signedIn)
        } catch OAuthError.cancelled {
            return .cancelled
        } catch {
            guard mine == attempt else { return .cancelled }
            return .failed("Signing in did not finish: \(error).")
        }
    }

    /// A lasso that did not let this Mac in, in a sentence.
    static func failure(_ access: LassoAccess, _ url: URL) -> ConnectOutcome {
        let host = shown(url)
        switch access {
        case .unreachable(let why) where why.hasPrefix("HTTP"):
            return .failed("Something answered at \(host), but not as lasso does (\(why)). Check the address.")
        case .unreachable(let why):
            let reason = why.hasSuffix(".") ? why : why + "."
            return .failed("No lasso answered at \(host). \(reason)")
        case .signIn:
            return .failed("\(host) asks for a sign-in instead.")
        case .password, .open:
            return .failed("\(host) did not let this Mac in.")
        }
    }
}
