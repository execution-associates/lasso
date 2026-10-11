import Foundation

/// lasso's HTTP API, and only that (PLAN 3). /api/* takes UI_AUTH basic auth
/// and nothing else (src/main.go:3285); a lasso behind Cloudflare Access also
/// needs an Access service token. Both come from the Keychain.
///
/// A reference: every live action shares one client, so pointing it at
/// another lasso (Settings' picker, `retarget`) moves them all at once.
public final class LassoClient: @unchecked Sendable {
    public static let defaultBaseURL = URL(string: "http://127.0.0.1:8090")!
    public static let baseURLDefaultsKey = "baseURL"
    /// The lassos Settings offers: UserDefaults `lassoCandidates` (dev.sh sets
    /// a Mac's tunnel ports), else lasso's own defaults, production's 8090 and
    /// the 8190-8199 a `mise run dev` lasso binds.
    public static let candidatesDefaultsKey = "lassoCandidates"

    private let lock = NSLock()
    private var url: URL
    private let headers: [String: String]
    private let session: URLSession

    public var baseURL: URL { lock.withLock { url } }

    public convenience init(baseURL: URL, basicAuth: String? = nil, accessID: String? = nil, accessSecret: String? = nil) {
        var h: [String: String] = ["Accept": "application/json"]
        if let b = basicAuth, !b.isEmpty { h["Authorization"] = "Basic " + Data(b.utf8).base64EncodedString() }
        if let id = accessID, let secret = accessSecret, !id.isEmpty, !secret.isEmpty {
            h["CF-Access-Client-Id"] = id
            h["CF-Access-Client-Secret"] = secret
        }
        self.init(baseURL: baseURL, headers: h, timeout: 15)
    }

    private init(baseURL: URL, headers: [String: String], timeout: TimeInterval) {
        url = baseURL
        self.headers = headers
        let cfg = URLSessionConfiguration.ephemeral
        cfg.timeoutIntervalForRequest = timeout
        cfg.httpCookieStorage = nil
        cfg.urlCache = nil
        session = URLSession(configuration: cfg)
    }

    /// The configured lasso: UserDefaults baseURL (dev.sh points minime at
    /// its own tunnel) and the Keychain's credentials.
    public static func configured() -> LassoClient {
        LassoClient(
            baseURL: configuredURL, basicAuth: Keychain.read(.uiAuth),
            accessID: Keychain.read(.cfAccessID), accessSecret: Keychain.read(.cfAccessSecret))
    }

    static var configuredURL: URL {
        // LASSO_BASE_URL (scripts, smoke) beats the stored setting.
        (ProcessInfo.processInfo.environment["LASSO_BASE_URL"] ?? UserDefaults.standard.string(forKey: baseURLDefaultsKey))
            .flatMap(URL.init(string:)) ?? defaultBaseURL
    }

    public static var candidates: [URL] {
        if let list = UserDefaults.standard.stringArray(forKey: candidatesDefaultsKey) {
            return list.compactMap(URL.init(string:))
        }
        return ([8090] + Array(8190...8199)).map { URL(string: "http://127.0.0.1:\($0)")! }
    }

    /// Point this client, and so everything holding it, at another lasso,
    /// and remember the choice.
    public func retarget(_ to: URL, remember: Bool = true) {
        lock.withLock { url = to }
        if remember { UserDefaults.standard.set(to.absoluteString, forKey: Self.baseURLDefaultsKey) }
    }

    /// A short-lived client for probing another lasso with these credentials.
    public func probe(_ at: URL) -> LassoClient { LassoClient(baseURL: at, headers: headers, timeout: 2) }

    public func request(_ path: String, query: [String: String] = [:], method: String = "GET", body: Data? = nil)
        -> URLRequest
    {
        var c = URLComponents(url: baseURL.appendingPathComponent(path), resolvingAgainstBaseURL: false)!
        if !query.isEmpty { c.queryItems = query.sorted { $0.key < $1.key }.map { URLQueryItem(name: $0.key, value: $0.value) } }
        var r = URLRequest(url: c.url!)
        r.httpMethod = method
        for (k, v) in headers { r.setValue(v, forHTTPHeaderField: k) }
        if let body {
            r.httpBody = body
            r.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        return r
    }

    public func get<T: Decodable>(_ path: String, query: [String: String] = [:], as: T.Type = T.self) async throws -> T {
        try await send(request(path, query: query))
    }

    public func post<T: Decodable>(_ path: String, query: [String: String] = [:], json: some Encodable, as: T.Type = T.self)
        async throws -> T
    {
        try await send(request(path, query: query, method: "POST", body: JSONEncoder().encode(json)))
    }

    public func send<T: Decodable>(_ r: URLRequest) async throws -> T {
        let (data, resp) = try await session.data(for: r)
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
