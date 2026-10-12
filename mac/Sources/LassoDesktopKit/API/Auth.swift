import AppKit
import AuthenticationServices
import CryptoKit
import Foundation
import Network

// Signing in to a lasso (PLAN 18): the probe says what a lasso wants, and a
// lasso that answers with an OAuth challenge gets the standard native-app
// flow MCP clients use. Behind Cloudflare Access the authorization server is
// Access's Managed OAuth; on a lasso with MCP_OAUTH and no edge it is lasso's
// own. On the tailnet and on loopback the peer is the identity: nothing to send.

/// What a lasso asks of this Mac, from an anonymous GET /api/version.
public enum LassoAccess: Equatable, Sendable {
    /// It answered: nothing to send (loopback, or the tailnet's whois).
    case open
    /// An OAuth challenge naming its protected-resource metadata (RFC 9728).
    case signIn(resourceMetadata: URL)
    /// UI_AUTH basic: a password from the Keychain.
    case password
    /// No answer, or one this app cannot use.
    case unreachable(String)
}

/// The tokens one sign-in gave, kept in the Keychain per lasso.
public struct OAuthGrant: Codable, Equatable, Sendable {
    public var clientID: String
    public var tokenEndpoint: URL
    /// The lasso origin the tokens are for (RFC 8707).
    public var resource: String
    public var accessToken: String
    public var refreshToken: String?
    public var expiresAt: Date?

    /// Spent, or within a minute of it: refresh before using.
    public func stale(_ now: Date = Date()) -> Bool { expiresAt.map { $0.timeIntervalSince(now) < 60 } ?? false }
}

public enum OAuthError: Error, CustomStringConvertible, Equatable {
    case discovery(String)
    case registration(String)
    case authorization(String)
    case token(String)
    case cancelled

    public var description: String {
        switch self {
        case .discovery(let s): "could not find the sign-in server: \(s)"
        case .registration(let s): "the sign-in server refused this app: \(s)"
        case .authorization(let s): "sign-in did not finish: \(s)"
        case .token(let s): "the sign-in server gave no token: \(s)"
        case .cancelled: "sign-in was cancelled"
        }
    }
}

public enum LassoAuth {
    /// The origin a lasso's tokens are keyed by and issued for.
    public static func origin(_ base: URL) -> String {
        var c = URLComponents()
        c.scheme = base.scheme
        c.host = base.host
        c.port = base.port
        return c.string ?? base.absoluteString
    }

    /// Ask the lasso at `base` what it needs, sending no credential (or only
    /// UI_AUTH's "user:pass", to check a password before keeping it). The
    /// X-Requested-With header is Cloudflare's documented way to get a clean
    /// 401 from Access rather than a redirect to its login page.
    public static func probe(_ base: URL, basicAuth: String? = nil, session: URLSession = .shared) async -> LassoAccess {
        var r = URLRequest(url: base.appendingPathComponent("/api/version"))
        r.setValue("XMLHttpRequest", forHTTPHeaderField: "X-Requested-With")
        r.setValue("application/json", forHTTPHeaderField: "Accept")
        if let b = basicAuth { r.setValue("Basic " + Data(b.utf8).base64EncodedString(), forHTTPHeaderField: "Authorization") }
        r.timeoutInterval = 10
        do {
            let (data, resp) = try await session.data(for: r)
            guard let http = resp as? HTTPURLResponse else { return .unreachable("no HTTP answer") }
            switch http.statusCode {
            case 200 where (try? JSONDecoder().decode(VersionInfo.self, from: data)) != nil:
                return .open
            case 401, 403:
                let challenge = http.value(forHTTPHeaderField: "WWW-Authenticate") ?? ""
                if let m = resourceMetadata(challenge) { return .signIn(resourceMetadata: m) }
                if challenge.lowercased().hasPrefix("basic") { return .password }
                return .unreachable("HTTP \(http.statusCode) with no sign-in challenge")
            default:
                return .unreachable("HTTP \(http.statusCode)")
            }
        } catch {
            return .unreachable(error.localizedDescription)
        }
    }

    /// The resource_metadata URL of a `Bearer` challenge (RFC 9728 §5.1).
    public static func resourceMetadata(_ challenge: String) -> URL? {
        guard challenge.range(of: "bearer", options: [.caseInsensitive, .anchored]) != nil,
            let r = challenge.range(of: #"resource_metadata="([^"]+)""#, options: .regularExpression)
        else { return nil }
        let quoted = challenge[r].dropFirst("resource_metadata=\"".count).dropLast()
        return URL(string: String(quoted))
    }

    /// Where an authorization server publishes its metadata (RFC 8414 §3.1):
    /// the well-known segment goes between the host and any path.
    public static func serverMetadataURL(_ issuer: URL) -> URL? {
        guard var c = URLComponents(url: issuer, resolvingAgainstBaseURL: false) else { return nil }
        let path = c.path == "/" ? "" : c.path
        c.path = "/.well-known/oauth-authorization-server" + path
        c.query = nil
        return c.url
    }

    /// PKCE (RFC 7636): a verifier and its S256 challenge.
    public static func pkce() -> (verifier: String, challenge: String) {
        let verifier = randomURLSafe(32)
        return (verifier, challenge(for: verifier))
    }

    public static func challenge(for verifier: String) -> String {
        base64url(Data(SHA256.hash(data: Data(verifier.utf8))))
    }

    static func randomURLSafe(_ bytes: Int) -> String {
        var b = [UInt8](repeating: 0, count: bytes)
        _ = SecRandomCopyBytes(kSecRandomDefault, bytes, &b)
        return base64url(Data(b))
    }

    static func base64url(_ d: Data) -> String {
        d.base64EncodedString().replacingOccurrences(of: "+", with: "-").replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }

    static func form(_ fields: [String: String]) -> Data {
        var allowed = CharacterSet.urlQueryAllowed
        allowed.remove(charactersIn: "+&=")
        return Data(
            fields.sorted { $0.key < $1.key }
                .map { "\($0.key)=\($0.value.addingPercentEncoding(withAllowedCharacters: allowed) ?? $0.value)" }
                .joined(separator: "&").utf8)
    }

    struct TokenReply: Decodable {
        var access_token: String
        var refresh_token: String?
        var expires_in: Double?
    }

    /// A token endpoint's answer, as a grant (keeping the old refresh token
    /// when a refresh does not rotate it).
    static func grant(_ data: Data, clientID: String, tokenEndpoint: URL, resource: String, keepRefresh: String?) throws
        -> OAuthGrant
    {
        guard let t = try? JSONDecoder().decode(TokenReply.self, from: data) else {
            throw OAuthError.token(String(decoding: data.prefix(200), as: UTF8.self))
        }
        return OAuthGrant(
            clientID: clientID, tokenEndpoint: tokenEndpoint, resource: resource, accessToken: t.access_token,
            refreshToken: t.refresh_token ?? keepRefresh, expiresAt: t.expires_in.map { Date().addingTimeInterval($0) })
    }

    /// Trade the refresh token for a fresh access token.
    public static func refresh(_ g: OAuthGrant, session: URLSession = .shared) async throws -> OAuthGrant {
        guard let rt = g.refreshToken else { throw OAuthError.token("no refresh token") }
        var r = URLRequest(url: g.tokenEndpoint)
        r.httpMethod = "POST"
        r.setValue("application/x-www-form-urlencoded", forHTTPHeaderField: "Content-Type")
        r.setValue("application/json", forHTTPHeaderField: "Accept")
        r.httpBody = form(["grant_type": "refresh_token", "refresh_token": rt, "client_id": g.clientID, "resource": g.resource])
        let (data, resp) = try await session.data(for: r)
        guard (resp as? HTTPURLResponse)?.statusCode == 200 else {
            throw OAuthError.token("refresh refused (HTTP \((resp as? HTTPURLResponse)?.statusCode ?? 0))")
        }
        return try grant(data, clientID: g.clientID, tokenEndpoint: g.tokenEndpoint, resource: g.resource, keepRefresh: rt)
    }
}

/// One lasso's tokens in the Keychain, keyed by its origin.
public enum TokenStore {
    static func account(_ base: URL) -> String { "oauth:" + LassoAuth.origin(base) }

    public static func read(_ base: URL) -> OAuthGrant? {
        Keychain.read(account: account(base)).flatMap { try? JSONDecoder().decode(OAuthGrant.self, from: Data($0.utf8)) }
    }

    public static func write(_ g: OAuthGrant, for base: URL) {
        guard let d = try? JSONEncoder().encode(g) else { return }
        Keychain.write(account: account(base), String(decoding: d, as: UTF8.self))
    }

    public static func delete(_ base: URL) { Keychain.delete(account: account(base)) }
}

/// The interactive sign-in: discovery, dynamic client registration with a
/// loopback redirect (RFC 8252 §7.3), authorization code with PKCE in an
/// ASWebAuthenticationSession, and the token exchange.
@MainActor
public final class OAuthSignIn: NSObject, ASWebAuthenticationPresentationContextProviding {
    let base: URL
    let metadata: URL
    let session: URLSession
    weak var anchor: NSWindow?
    private var web: ASWebAuthenticationSession?
    private var callback: LoopbackCallback?
    private var cancelled = false

    public init(base: URL, metadata: URL, anchor: NSWindow?, session: URLSession = .shared) {
        self.base = base
        self.metadata = metadata
        self.anchor = anchor
        self.session = session
    }

    struct ResourceMetadata: Decodable {
        var authorization_servers: [String]?
    }

    struct ServerMetadata: Decodable {
        var authorization_endpoint: String
        var token_endpoint: String
        var registration_endpoint: String?
    }

    struct Registration: Decodable { var client_id: String }

    private func json<T: Decodable>(_ url: URL, as: T.Type) async throws -> T {
        var r = URLRequest(url: url)
        r.setValue("application/json", forHTTPHeaderField: "Accept")
        let (data, resp) = try await session.data(for: r)
        guard (resp as? HTTPURLResponse)?.statusCode == 200, let v = try? JSONDecoder().decode(T.self, from: data) else {
            throw OAuthError.discovery("\(url.host ?? "") answered HTTP \((resp as? HTTPURLResponse)?.statusCode ?? 0)")
        }
        return v
    }

    public func run() async throws -> OAuthGrant {
        let rm = try await json(metadata, as: ResourceMetadata.self)
        guard let issuer = rm.authorization_servers?.first.flatMap(URL.init(string:)),
            let smURL = LassoAuth.serverMetadataURL(issuer)
        else { throw OAuthError.discovery("the lasso names no authorization server") }
        let sm = try await json(smURL, as: ServerMetadata.self)
        guard let authorize = URL(string: sm.authorization_endpoint), let token = URL(string: sm.token_endpoint) else {
            throw OAuthError.discovery("the sign-in server's metadata has no endpoints")
        }
        // The whole lasso, not the path that was probed: Access names the
        // probed path (/api/version) as the resource in its metadata.
        let resource = LassoAuth.origin(base)

        let callback = try await LoopbackCallback.start()
        self.callback = callback
        defer {
            callback.stop()
            self.callback = nil
        }
        if cancelled { throw OAuthError.cancelled }
        let redirect = "http://127.0.0.1:\(callback.port)/callback"
        let clientID = try await register(sm.registration_endpoint, redirect: redirect)
        if cancelled { throw OAuthError.cancelled }

        let (verifier, challenge) = LassoAuth.pkce()
        let state = LassoAuth.randomURLSafe(16)
        var c = URLComponents(url: authorize, resolvingAgainstBaseURL: false)!
        c.queryItems = (c.queryItems ?? []) + [
            .init(name: "response_type", value: "code"), .init(name: "client_id", value: clientID),
            .init(name: "redirect_uri", value: redirect), .init(name: "code_challenge", value: challenge),
            .init(name: "code_challenge_method", value: "S256"), .init(name: "state", value: state),
            .init(name: "resource", value: resource),
        ]
        guard let url = c.url else { throw OAuthError.authorization("bad authorization URL") }

        // The browser lands on the loopback listener; the session is only the
        // window it happens in, and closes once the code arrives.
        let web = ASWebAuthenticationSession(url: url, callbackURLScheme: "lassodesktop") { [weak callback] _, err in
            if let e = err as? ASWebAuthenticationSessionError, e.code == .canceledLogin { callback?.cancel() }
        }
        web.presentationContextProvider = self
        web.prefersEphemeralWebBrowserSession = false
        self.web = web
        NSApp.activate(ignoringOtherApps: true)
        guard web.start() else { throw OAuthError.authorization("could not open the sign-in window") }
        defer {
            web.cancel()
            self.web = nil
        }
        let got = try await callback.wait()
        guard got.state == state else { throw OAuthError.authorization("the answer did not match this sign-in") }
        if let e = got.error { throw OAuthError.authorization(e) }
        guard let code = got.code else { throw OAuthError.authorization("no code came back") }

        var r = URLRequest(url: token)
        r.httpMethod = "POST"
        r.setValue("application/x-www-form-urlencoded", forHTTPHeaderField: "Content-Type")
        r.setValue("application/json", forHTTPHeaderField: "Accept")
        r.httpBody = LassoAuth.form([
            "grant_type": "authorization_code", "code": code, "redirect_uri": redirect, "client_id": clientID,
            "code_verifier": verifier, "resource": resource,
        ])
        let (data, resp) = try await session.data(for: r)
        guard (resp as? HTTPURLResponse)?.statusCode == 200 else {
            throw OAuthError.token("HTTP \((resp as? HTTPURLResponse)?.statusCode ?? 0)")
        }
        return try LassoAuth.grant(data, clientID: clientID, tokenEndpoint: token, resource: resource, keepRefresh: nil)
    }

    /// Give up on this sign-in: its window closes and `run` throws `cancelled`.
    public func cancel() {
        cancelled = true
        web?.cancel()
        callback?.cancel()
    }

    /// Dynamic client registration (RFC 7591) as a public native client.
    private func register(_ endpoint: String?, redirect: String) async throws -> String {
        guard let e = endpoint.flatMap(URL.init(string:)) else {
            throw OAuthError.registration("the sign-in server takes no registrations")
        }
        struct Body: Encodable {
            var client_name = "LassoDesktop"
            var redirect_uris: [String]
            var grant_types = ["authorization_code", "refresh_token"]
            var response_types = ["code"]
            var token_endpoint_auth_method = "none"
            var application_type = "native"
        }
        var r = URLRequest(url: e)
        r.httpMethod = "POST"
        r.setValue("application/json", forHTTPHeaderField: "Content-Type")
        r.setValue("application/json", forHTTPHeaderField: "Accept")
        r.httpBody = try JSONEncoder().encode(Body(redirect_uris: [redirect]))
        let (data, resp) = try await session.data(for: r)
        let code = (resp as? HTTPURLResponse)?.statusCode ?? 0
        guard (200..<300).contains(code), let reg = try? JSONDecoder().decode(Registration.self, from: data) else {
            throw OAuthError.registration("HTTP \(code): \(String(decoding: data.prefix(200), as: UTF8.self))")
        }
        return reg.client_id
    }

    public nonisolated func presentationAnchor(for session: ASWebAuthenticationSession) -> ASPresentationAnchor {
        MainActor.assumeIsolated { anchor ?? NSApp.keyWindow ?? NSWindow() }
    }
}

/// The redirect's landing place: a one-shot HTTP listener on 127.0.0.1 only
/// (never all interfaces), on a port the system picks.
final class LoopbackCallback: @unchecked Sendable {
    struct Answer: Sendable {
        var code: String?
        var state: String?
        var error: String?
    }

    private let listener: NWListener
    private let queue = DispatchQueue(label: "lasso.oauth.callback")
    private let lock = NSLock()
    private var waiter: CheckedContinuation<Answer, Error>?
    private var answer: Result<Answer, Error>?
    private(set) var port: UInt16 = 0

    private init(_ l: NWListener) { listener = l }

    static func start() async throws -> LoopbackCallback {
        let params = NWParameters.tcp
        params.requiredLocalEndpoint = .hostPort(host: "127.0.0.1", port: .any)
        let cb = LoopbackCallback(try NWListener(using: params))
        try await withCheckedThrowingContinuation { (k: CheckedContinuation<Void, Error>) in
            let once = Once()
            cb.listener.stateUpdateHandler = { s in
                switch s {
                case .ready:
                    cb.port = cb.listener.port?.rawValue ?? 0
                    if once.take() { k.resume() }
                case .failed(let e):
                    if once.take() { k.resume(throwing: e) }
                default: break
                }
            }
            cb.listener.newConnectionHandler = { conn in cb.serve(conn) }
            cb.listener.start(queue: cb.queue)
        }
        return cb
    }

    func wait() async throws -> Answer {
        try await withCheckedThrowingContinuation { k in
            lock.lock()
            if let a = answer {
                lock.unlock()
                k.resume(with: a)
            } else {
                waiter = k
                lock.unlock()
            }
        }
    }

    func cancel() { finish(.failure(OAuthError.cancelled)) }

    func stop() { listener.cancel() }

    private func finish(_ r: Result<Answer, Error>) {
        lock.lock()
        guard answer == nil else {
            lock.unlock()
            return
        }
        answer = r
        let k = waiter
        waiter = nil
        lock.unlock()
        k?.resume(with: r)
    }

    private func serve(_ conn: NWConnection) {
        conn.start(queue: queue)
        conn.receive(minimumIncompleteLength: 1, maximumLength: 16384) { [weak self] data, _, _, _ in
            let head = data.map { String(decoding: $0, as: UTF8.self) } ?? ""
            let target = head.split(separator: "\r\n").first?.split(separator: " ").dropFirst().first.map(String.init) ?? ""
            let items = URLComponents(string: "http://127.0.0.1" + target)
            let isCallback = items?.path == "/callback"
            let q = Dictionary((items?.queryItems ?? []).map { ($0.name, $0.value ?? "") }) { a, _ in a }
            let page = isCallback
                ? "<!doctype html><meta charset=utf-8><title>lasso</title><body style=\"font:15px -apple-system;padding:3em\">Signed in to lasso. You can close this window.</body>"
                : "not found"
            let reply = "HTTP/1.1 \(isCallback ? "200 OK" : "404 Not Found")\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: \(page.utf8.count)\r\nConnection: close\r\n\r\n\(page)"
            conn.send(content: Data(reply.utf8), completion: .contentProcessed { _ in conn.cancel() })
            if isCallback {
                self?.finish(.success(Answer(code: q["code"], state: q["state"], error: q["error_description"] ?? q["error"])))
            }
        }
    }
}

/// Resumes a continuation once, whichever state update comes first.
private final class Once: @unchecked Sendable {
    private let lock = NSLock()
    private var done = false
    func take() -> Bool {
        lock.lock()
        defer { lock.unlock() }
        if done { return false }
        done = true
        return true
    }
}

extension LassoConnector {
    /// The app's connector: the real probe, and the browser sign-in hung
    /// from `anchor`'s window, cancellable while it waits.
    public static func live(anchor: @escaping () -> NSWindow?) -> LassoConnector {
        let current = SignInHolder()
        let c = LassoConnector(
            probe: { await LassoAuth.probe($0, basicAuth: $1) },
            signIn: { base, metadata in
                let s = OAuthSignIn(base: base, metadata: metadata, anchor: anchor())
                current.signIn = s
                defer { if current.signIn === s { current.signIn = nil } }
                return try await s.run()
            })
        c.cancelSignIn = { current.signIn?.cancel() }
        return c
    }
}

/// The sign-in in progress, so Cancel can reach it.
@MainActor
private final class SignInHolder {
    var signIn: OAuthSignIn?
}
