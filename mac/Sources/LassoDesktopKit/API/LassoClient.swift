import Foundation

/// lasso's HTTP API, and only that (PLAN 3). /api/* takes UI_AUTH basic auth
/// and nothing else (src/main.go:3285); a lasso behind Cloudflare Access also
/// needs an Access service token. Both come from the Keychain.
public struct LassoClient: Sendable {
    public static let defaultBaseURL = URL(string: "http://127.0.0.1:8090")!
    public static let baseURLDefaultsKey = "baseURL"

    public let baseURL: URL
    private let headers: [String: String]
    private let session: URLSession

    public init(baseURL: URL, basicAuth: String? = nil, accessID: String? = nil, accessSecret: String? = nil) {
        self.baseURL = baseURL
        var h: [String: String] = ["Accept": "application/json"]
        if let b = basicAuth, !b.isEmpty { h["Authorization"] = "Basic " + Data(b.utf8).base64EncodedString() }
        if let id = accessID, let secret = accessSecret, !id.isEmpty, !secret.isEmpty {
            h["CF-Access-Client-Id"] = id
            h["CF-Access-Client-Secret"] = secret
        }
        headers = h
        let cfg = URLSessionConfiguration.ephemeral
        cfg.timeoutIntervalForRequest = 15
        cfg.httpCookieStorage = nil
        cfg.urlCache = nil
        session = URLSession(configuration: cfg)
    }

    /// The configured lasso: UserDefaults baseURL (dev.sh points minime at
    /// its own tunnel) and the Keychain's credentials.
    public static func configured() -> LassoClient {
        let url = UserDefaults.standard.string(forKey: baseURLDefaultsKey).flatMap(URL.init(string:))
        return LassoClient(
            baseURL: url ?? defaultBaseURL, basicAuth: Keychain.read(.uiAuth),
            accessID: Keychain.read(.cfAccessID), accessSecret: Keychain.read(.cfAccessSecret))
    }

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
