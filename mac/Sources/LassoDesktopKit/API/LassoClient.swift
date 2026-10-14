import Foundation

/// lasso's HTTP API, and only that (PLAN 3). What it sends depends on the
/// lasso (PLAN 18): nothing on loopback or the tailnet, a signed-in lasso's
/// bearer token, UI_AUTH basic for an older install, and an Access service
/// token for an unattended Mac. All of them come from the Keychain.
///
/// A reference: every live action shares one client, so pointing it at
/// another lasso (Settings' picker, `retarget`) moves them all at once.
public final class LassoClient: @unchecked Sendable {
    public static let defaultBaseURL = URL(string: "http://127.0.0.1:8090")!
    public static let baseURLDefaultsKey = "baseURL"

    private let lock = NSLock()
    private var url: URL
    private var tokens: TokenAuth?
    /// UI_AUTH's "user:pass", sent as Basic (an older install); settable,
    /// since the first-run panel asks for it after the client exists.
    private var basic: String?
    private let headers: [String: String]
    private let session: URLSession

    public var baseURL: URL { lock.withLock { url } }
    /// The signed-in lasso's tokens, if it has any.
    public var auth: TokenAuth? { lock.withLock { tokens } }

    public convenience init(
        baseURL: URL, basicAuth: String? = nil, accessID: String? = nil, accessSecret: String? = nil, grant: OAuthGrant? = nil,
        protocolClasses: [AnyClass]? = nil, persist: (@Sendable (OAuthGrant, URL) -> Void)? = nil
    ) {
        // X-Requested-With gets a clean 401 from Cloudflare Access when its
        // session has ended, not a redirect to its login page.
        var h: [String: String] = ["Accept": "application/json", "X-Requested-With": "XMLHttpRequest"]
        if let id = accessID, let secret = accessSecret, !id.isEmpty, !secret.isEmpty {
            h["CF-Access-Client-Id"] = id
            h["CF-Access-Client-Secret"] = secret
        }
        self.init(baseURL: baseURL, headers: h, timeout: 15, protocolClasses: protocolClasses)
        basic = basicAuth.flatMap { $0.isEmpty ? nil : $0 }
        tokens = grant.map {
            TokenAuth(
                base: baseURL, grant: $0, session: session, serviceToken: h["CF-Access-Client-Id"] != nil,
                persist: persist ?? { TokenStore.write($0, for: $1) })
        }
    }

    private init(baseURL: URL, headers: [String: String], timeout: TimeInterval, protocolClasses: [AnyClass]? = nil) {
        url = baseURL
        self.headers = headers
        let cfg = URLSessionConfiguration.ephemeral
        cfg.timeoutIntervalForRequest = timeout
        cfg.httpCookieStorage = nil
        cfg.urlCache = nil
        if let p = protocolClasses { cfg.protocolClasses = p }
        session = URLSession(configuration: cfg)
    }

    /// The session requests go out on (the event stream's too).
    var urlSession: URLSession { session }

    /// The configured lasso: UserDefaults baseURL (dev.sh points minime at
    /// its own tunnel) and the Keychain's credentials.
    public static func configured() -> LassoClient {
        LassoClient(
            baseURL: configuredURL, basicAuth: Keychain.read(.uiAuth),
            accessID: Keychain.read(.cfAccessID), accessSecret: Keychain.read(.cfAccessSecret),
            grant: TokenStore.read(configuredURL))
    }

    static var configuredURL: URL {
        // LASSO_BASE_URL (scripts, smoke) beats the stored setting.
        (ProcessInfo.processInfo.environment["LASSO_BASE_URL"] ?? UserDefaults.standard.string(forKey: baseURLDefaultsKey))
            .flatMap(URL.init(string:)) ?? defaultBaseURL
    }

    /// Point this client, and so everything holding it, at another lasso,
    /// and remember the choice.
    public func retarget(_ to: URL, remember: Bool = true) {
        let serviceToken = headers["CF-Access-Client-Id"] != nil
        let t = TokenStore.read(to).map { TokenAuth(base: to, grant: $0, session: session, serviceToken: serviceToken) }
        lock.withLock {
            url = to
            tokens = t
        }
        if remember { UserDefaults.standard.set(to.absoluteString, forKey: Self.baseURLDefaultsKey) }
    }

    /// A sign-in finished: use its tokens from now on (they are already in
    /// the Keychain).
    public func signedIn(_ g: OAuthGrant) {
        let t = TokenAuth(base: baseURL, grant: g, session: session, serviceToken: headers["CF-Access-Client-Id"] != nil)
        lock.withLock { tokens = t }
    }

    /// Forget this lasso's tokens.
    public func signOut() {
        TokenStore.delete(baseURL)
        lock.withLock { tokens = nil }
    }

    /// Send UI_AUTH's "user:pass" as Basic from now on (nil stops it).
    public func setBasicAuth(_ userPass: String?) {
        lock.withLock { basic = userPass.flatMap { $0.isEmpty ? nil : $0 } }
    }

    /// The Basic credential, if one is set.
    public var basicAuth: String? { lock.withLock { basic } }

    /// A short-lived client for probing another lasso with these credentials.
    public func probe(_ at: URL) -> LassoClient {
        let c = LassoClient(baseURL: at, headers: headers, timeout: 2)
        c.setBasicAuth(basicAuth)
        return c
    }

    public func request(_ path: String, query: [String: String] = [:], method: String = "GET", body: Data? = nil)
        -> URLRequest
    {
        var c = URLComponents(url: baseURL.appendingPathComponent(path), resolvingAgainstBaseURL: false)!
        if !query.isEmpty { c.queryItems = query.sorted { $0.key < $1.key }.map { URLQueryItem(name: $0.key, value: $0.value) } }
        var r = URLRequest(url: c.url!)
        r.httpMethod = method
        authorize(&r)
        if let body {
            r.httpBody = body
            r.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        return r
    }

    /// A request for `pathAndQuery` exactly as spelled (a trailing slash, an
    /// encoded query), on this lasso with its credentials.
    func request(raw pathAndQuery: String) -> URLRequest {
        let base = baseURL.absoluteString.hasSuffix("/") ? String(baseURL.absoluteString.dropLast()) : baseURL.absoluteString
        var r = URLRequest(url: URL(string: base + pathAndQuery) ?? baseURL)
        authorize(&r)
        return r
    }

    /// What every request to this lasso carries: the fixed headers (an Access
    /// service token among them), UI_AUTH's Basic, and the signed-in token.
    func authorize(_ r: inout URLRequest) {
        for (k, v) in headers { r.setValue(v, forHTTPHeaderField: k) }
        if let b = basicAuth { r.setValue("Basic " + Data(b.utf8).base64EncodedString(), forHTTPHeaderField: "Authorization") }
        auth?.apply(&r)
    }

    public func get<T: Decodable>(_ path: String, query: [String: String] = [:], as: T.Type = T.self) async throws -> T {
        try await send(request(path, query: query))
    }

    public func post<T: Decodable>(_ path: String, query: [String: String] = [:], json: some Encodable, as: T.Type = T.self)
        async throws -> T
    {
        try await send(request(path, query: query, method: "POST", body: JSONEncoder().encode(json)))
    }

    /// Send a file as the request body, streamed from disk rather than read
    /// into memory, and decode the JSON answer.
    public func upload<T: Decodable>(_ r: URLRequest, fromFile file: URL) async throws -> T {
        var r = r
        r.timeoutInterval = 600
        let (data, resp) = try await perform(r) { try await self.session.upload(for: $0, fromFile: file) }
        return try decodeUpload(r, data, resp)
    }

    /// Send `body` (one part of a bigger file) as the request body and decode
    /// the JSON answer.
    public func upload<T: Decodable>(_ r: URLRequest, from body: Data) async throws -> T {
        var r = r
        r.timeoutInterval = 600
        let (data, resp) = try await perform(r) { try await self.session.upload(for: $0, from: body) }
        return try decodeUpload(r, data, resp)
    }

    private func decodeUpload<T: Decodable>(_ r: URLRequest, _ data: Data, _ resp: URLResponse) throws -> T {
        let code = (resp as? HTTPURLResponse)?.statusCode ?? 0
        guard (200..<300).contains(code) else {
            throw LassoError.http(code, String(decoding: data.prefix(300), as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines))
        }
        do { return try JSONDecoder().decode(T.self, from: data) } catch {
            throw LassoError.decode(r.url?.path ?? "", String(describing: error))
        }
    }

    /// A file's bytes straight to `dest`, never held in memory (a video can
    /// be gigabytes), with the same refresh-and-retry on a 401 as every
    /// other request. `progress` hears bytes so far and the expected total
    /// (-1 when lasso did not say). An error status throws, its first words
    /// kept, and leaves `dest` as it was.
    public func download(_ r: URLRequest, to dest: URL, progress: (@Sendable (Int64, Int64) -> Void)? = nil) async throws {
        var r = r
        r.timeoutInterval = 600
        let (tmp, resp) = try await perform(r) { req -> (URL, URLResponse) in
            let (file, resp) = try await DownloadTask.run(self.session, req, progress: progress)
            // A refused first try's body is not the file: drop it before the retry.
            if (resp as? HTTPURLResponse)?.statusCode == 401 { try? FileManager.default.removeItem(at: file) }
            return (file, resp)
        }
        defer { try? FileManager.default.removeItem(at: tmp) }
        let code = (resp as? HTTPURLResponse)?.statusCode ?? 0
        guard (200..<300).contains(code) else {
            let head = (try? Data(contentsOf: tmp, options: .mappedIfSafe).prefix(300)) ?? Data()
            throw LassoError.http(code, String(decoding: head, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines))
        }
        let fm = FileManager.default
        try fm.createDirectory(at: dest.deletingLastPathComponent(), withIntermediateDirectories: true)
        if fm.fileExists(atPath: dest.path) { try fm.removeItem(at: dest) }
        try fm.moveItem(at: tmp, to: dest)
    }

    /// A request whose answer is not JSON (a file): its body and status.
    public func raw(_ r: URLRequest) async throws -> (Data, Int) {
        let (data, resp) = try await perform(r) { try await self.session.data(for: $0) }
        return (data, (resp as? HTTPURLResponse)?.statusCode ?? 0)
    }

    /// A request's whole answer, its status and headers too. With
    /// `redirects` false a 3xx is the answer rather than followed.
    func response(_ r: URLRequest, redirects: Bool = true) async throws -> (Data, HTTPURLResponse?) {
        let (data, resp) = try await perform(r) { req in
            redirects ? try await self.session.data(for: req) : try await self.session.data(for: req, delegate: NoRedirect())
        }
        return (data, resp as? HTTPURLResponse)
    }

    /// Send with the freshest token; a 401 refreshes once and retries, and a
    /// refresh that fails leaves the user a "Sign in again" (AuthState).
    func perform<T>(_ r: URLRequest, _ go: (URLRequest) async throws -> (T, URLResponse)) async throws -> (T, URLResponse) {
        guard let t = auth else { return try await go(r) }
        var r = r
        await t.freshen()
        t.apply(&r)
        let first = try await go(r)
        guard (first.1 as? HTTPURLResponse)?.statusCode == 401, await t.refreshAfterRefusal() else { return first }
        t.apply(&r)
        return try await go(r)
    }

    public func send<T: Decodable>(_ r: URLRequest) async throws -> T {
        let (data, resp) = try await perform(r) { try await self.session.data(for: $0) }
        let code = (resp as? HTTPURLResponse)?.statusCode ?? 0
        // lasso serves its web app for any path it has no route for, with a
        // 200: an HTML answer to an API call means the route does not exist
        // on this lasso (older than the feature).
        let type = (resp as? HTTPURLResponse)?.value(forHTTPHeaderField: "Content-Type") ?? ""
        if (200..<300).contains(code), type.hasPrefix("text/html") {
            throw LassoError.http(404, "no such route on this lasso")
        }
        guard (200..<300).contains(code) else {
            // lasso's errors are plain text (http.Error).
            throw LassoError.http(code, String(decoding: data.prefix(300), as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines))
        }
        do {
            return try JSONDecoder().decode(T.self, from: data)
        } catch {
            throw LassoError.decode(r.url?.path ?? "", String(describing: error))
        }
    }

    /// A long-lived request for the event stream.
    func streamRequest(_ path: String, query: [String: String]) -> URLRequest {
        var r = request(path, query: query)
        r.setValue("text/event-stream", forHTTPHeaderField: "Accept")
        r.timeoutInterval = 90
        return r
    }
}

/// One download task, its file moved somewhere it stays before URLSession
/// deletes it, and its progress reported as it goes.
private final class DownloadTask: NSObject, URLSessionDownloadDelegate, @unchecked Sendable {
    private let lock = NSLock()
    private var continuation: CheckedContinuation<(URL, URLResponse), Error>?
    private var file: URL?
    private let progress: (@Sendable (Int64, Int64) -> Void)?

    private init(progress: (@Sendable (Int64, Int64) -> Void)?) { self.progress = progress }

    static func run(_ session: URLSession, _ r: URLRequest, progress: (@Sendable (Int64, Int64) -> Void)?) async throws -> (URL, URLResponse) {
        let d = DownloadTask(progress: progress)
        let task = session.downloadTask(with: r)
        task.delegate = d
        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { c in
                d.lock.withLock { d.continuation = c }
                task.resume()
            }
        } onCancel: {
            task.cancel()
        }
    }

    func urlSession(
        _ session: URLSession, downloadTask: URLSessionDownloadTask, didWriteData _: Int64, totalBytesWritten: Int64,
        totalBytesExpectedToWrite: Int64
    ) {
        progress?(totalBytesWritten, totalBytesExpectedToWrite)
    }

    func urlSession(_ session: URLSession, downloadTask: URLSessionDownloadTask, didFinishDownloadingTo location: URL) {
        // URLSession deletes `location` when this returns.
        let keep = FileManager.default.temporaryDirectory.appendingPathComponent("lasso-download-" + UUID().uuidString)
        if (try? FileManager.default.moveItem(at: location, to: keep)) != nil { lock.withLock { file = keep } }
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: (any Error)?) {
        let (c, f) = lock.withLock { () -> (CheckedContinuation<(URL, URLResponse), Error>?, URL?) in
            defer { continuation = nil }
            return (continuation, file)
        }
        if let error {
            if let f { try? FileManager.default.removeItem(at: f) }
            c?.resume(throwing: error)
        } else if let f, let resp = task.response {
            c?.resume(returning: (f, resp))
        } else {
            c?.resume(throwing: URLError(.cannotCreateFile))
        }
    }
}

/// Answers a redirect with itself instead of following it.
private final class NoRedirect: NSObject, URLSessionTaskDelegate, Sendable {
    func urlSession(
        _ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest
    ) async -> URLRequest? { nil }
}

/// A signed-in lasso's tokens: on every request, refreshed when stale, and
/// once more after a 401. One refresh at a time; a failed one marks the app
/// signed out (AuthState) and keeps everything else as it was.
public final class TokenAuth: @unchecked Sendable {
    let base: URL
    let session: URLSession
    /// An Access service token holds Authorization's place at the edge, so
    /// lasso's own token rides in X-Lasso-Token.
    let header: String
    private let lock = NSLock()
    private var grant: OAuthGrant
    private var inflight: Task<Bool, Never>?
    /// Where a refreshed grant is kept (the Keychain; tests keep it in memory).
    let persist: @Sendable (OAuthGrant, URL) -> Void

    init(
        base: URL, grant: OAuthGrant, session: URLSession, serviceToken: Bool,
        persist: @escaping @Sendable (OAuthGrant, URL) -> Void = { TokenStore.write($0, for: $1) }
    ) {
        self.base = base
        self.grant = grant
        self.session = session
        self.persist = persist
        header = serviceToken ? "X-Lasso-Token" : "Authorization"
    }

    public var current: OAuthGrant { lock.withLock { grant } }

    func apply(_ r: inout URLRequest) {
        let token = current.accessToken
        if header == "Authorization" {
            r.setValue("Bearer " + token, forHTTPHeaderField: "Authorization")
        } else {
            r.setValue(token, forHTTPHeaderField: header)
        }
    }

    /// Refresh first when the token is spent or about to be.
    func freshen() async {
        if current.stale() { _ = await refresh() }
    }

    /// lasso refused the token: refresh once. False: not refreshed (signed
    /// out only when the sign-in server refused the grant).
    func refreshAfterRefusal() async -> Bool { await refresh() }

    private func refresh() async -> Bool {
        let task: Task<Bool, Never> = lock.withLock {
            if let t = inflight { return t }
            let g = grant
            let t = Task { [session, base] () -> Bool in
                do {
                    let fresh = try await LassoAuth.refresh(g, session: session)
                    self.persist(fresh, base)
                    self.lock.withLock { self.grant = fresh }
                    return true
                } catch OAuthError.refused {
                    await AuthState.shared.signedOut(base)
                    return false
                } catch {
                    return false
                }
            }
            inflight = t
            return t
        }
        let ok = await task.value
        lock.withLock { if inflight == task { inflight = nil } }
        return ok
    }
}

/// Whether the lasso in use needs the user to sign in again: a banner in
/// Settings, and the app keeps its state until they do.
@MainActor
public final class AuthState: ObservableObject {
    public static let shared = AuthState()
    /// The lasso whose tokens stopped working.
    @Published public private(set) var expired: URL?
    /// Asked to show the sign-in (the coordinator opens Settings).
    public var onExpired: () -> Void = {}

    func signedOut(_ base: URL) {
        guard expired == nil else { return }
        expired = base
        onExpired()
    }

    public func signedIn() { expired = nil }
}

public enum LassoError: Error, CustomStringConvertible, Equatable {
    case http(Int, String)
    case decode(String, String)

    public var description: String {
        switch self {
        case .http(let code, let body): "HTTP \(code): \(body)"
        case .decode(let path, let err): "could not read \(path): \(err)"
        }
    }
}
