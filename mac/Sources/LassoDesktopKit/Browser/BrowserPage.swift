import Foundation
import WebKit

// The Browser window's page (PLAN 24): lasso's /desktop-browser.html, the web
// Browser tab's Agent view alone (src/web/src/desktop/browser-window.tsx), run
// as a lasso page (Page/LassoPage.swift) under the `lasso-browser:` scheme.
// It reaches its own files, lasso's browser status (/api/browser, which also
// starts a stopped browser) and the browsers' CDP sockets (/cdp,
// /cdp/p/<id>), and nothing else on lasso.

/// One page of a browser, as /api/browser lists it.
public struct BrowserPageInfo: Decodable, Sendable, Equatable {
    public var id: String
    public var url: String
    public var title: String

    public init(id: String, url: String, title: String) {
        self.id = id
        self.url = url
        self.title = title
    }
}

/// One of lasso's browsers (lasso stores it as a "profile").
public struct BrowserInfo: Decodable, Sendable, Equatable, Identifiable {
    public var id: String
    public var name: String
    public var cdp_url: String?
    public var isDefault: Bool
    public var running: Bool
    public var reason: String?
    public var pages: [BrowserPageInfo]?
    public var ws_path: String?

    enum CodingKeys: String, CodingKey {
        case id, name, cdp_url, running, reason, pages, ws_path
        case isDefault = "default"
    }

    public init(id: String, name: String, isDefault: Bool = false, running: Bool = false, pages: [BrowserPageInfo] = []) {
        self.id = id
        self.name = name
        self.isDefault = isDefault
        self.running = running
        self.pages = pages
    }
}

/// GET /api/browser: whether this lasso has a browser, and every browser.
public struct BrowserStatusPayload: Decodable, Sendable, Equatable {
    public var available: Bool?
    public var reason: String?
    public var running: Bool?
    public var pages: [BrowserPageInfo]?
    public var profiles: [BrowserInfo]?

    public init(available: Bool = true, reason: String? = nil, profiles: [BrowserInfo]) {
        self.available = available
        self.reason = reason
        self.profiles = profiles
    }

    /// Every browser, the default first; an older lasso lists none and has
    /// only its one default browser.
    public var browsers: [BrowserInfo] {
        if let p = profiles, !p.isEmpty { return p }
        var d = BrowserInfo(id: BrowserPage.defaultBrowser, name: "Default", isDefault: true, running: running ?? false)
        d.pages = pages ?? []
        return [d]
    }
}

/// An agent showing the human a page (open_browser_tab, show_browser_tab):
/// the `browser-open` event.
public struct BrowserOpen: Decodable, Sendable, Equatable {
    public var profile: String?
    public var tab_id: String?
    public var url: String?
    public var from: String?
    public var mode: String?

    public init(profile: String?, tab_id: String?, url: String?, from: String?) {
        self.profile = profile
        self.tab_id = tab_id
        self.url = url
        self.from = from
    }

    /// The browser it is in (lasso's default when it names none).
    public var browser: String { (profile?.isEmpty ?? true) ? BrowserPage.defaultBrowser : profile! }
    /// Who opened it, as a sentence starts.
    public var who: String { (from?.isEmpty ?? true) ? "An agent" : from! }

    /// The page's address as the banner shows it: host and path, no scheme.
    public var shownURL: String {
        guard let s = url, !s.isEmpty, s != "about:blank" else { return "a new page" }
        guard let c = URLComponents(string: s), let host = c.host else { return s }
        let path = c.percentEncodedPath == "/" ? "" : c.percentEncodedPath
        return host + path
    }
}

/// The addresses the Browser window's page and lasso use.
public enum BrowserPage {
    public static let scheme = "lasso-browser"
    static let pageHost = "lasso"
    static let documentPath = "/desktop-browser.html"
    /// lasso's own browser (`/cdp`).
    public static let defaultBrowser = "default"

    /// The page, showing `browser`.
    static func pageURL(browser: String) -> URL {
        var c = URLComponents()
        c.scheme = scheme
        c.host = pageHost
        c.path = documentPath
        c.queryItems = [URLQueryItem(name: "profile", value: browser)]
        return c.url!
    }

    /// The path and query on lasso a page request asks for, or nil when the
    /// page may not have it: its document, its build's files, and the browser
    /// status (read, and POSTed to start a stopped browser).
    static func lassoPath(_ url: URL, method: String) -> String? {
        guard url.scheme == scheme, url.host == pageHost,
              let c = URLComponents(url: url, resolvingAgainstBaseURL: false)
        else { return nil }
        let path = c.percentEncodedPath
        switch (method, path) {
        case ("GET", documentPath):
            return path + (c.percentEncodedQuery.map { "?" + $0 } ?? "")
        case ("GET", "/api/browser"), ("POST", "/api/browser"):
            return path
        case ("GET", _) where LassoPageAssets.isAsset(path):
            return path
        default:
            return nil
        }
    }

    /// A browser's CDP socket: `/cdp` (the default) or `/cdp/p/<id>`, ids as
    /// lasso makes them; anything else is refused.
    static func socketURL(base: URL, path: String) -> URL? {
        if path != "/cdp" {
            guard path.hasPrefix("/cdp/p/") else { return nil }
            let id = path.dropFirst("/cdp/p/".count)
            guard (1...32).contains(id.count), let first = id.first, first.isLowercase || first.isNumber,
                  id.allSatisfy({ ($0.isLowercase && $0.isASCII) || ($0.isNumber && $0.isASCII) || $0 == "-" })
            else { return nil }
        }
        return LassoPageBridge.socketURL(base: base, path: path, query: nil)
    }
}

/// The Browser window's page: its requests and sockets confined to the
/// browsers, and the page saying it is up.
@MainActor
final class BrowserBridge: LassoPageBridge {
    /// The page has mounted and takes what the window hands it.
    var onReady: () -> Void = {}

    init(client: LassoClient) { super.init(client: client, global: "__lassoPage") }

    override func lassoPath(_ url: URL, method: String) -> String? { BrowserPage.lassoPath(url, method: method) }

    override func socketURL(path: String, query: String?) -> URL? { BrowserPage.socketURL(base: client.baseURL, path: path) }

    override func handle(_ op: String, _ body: [String: Any]) {
        if op == "ready" { onReady() }
    }

    /// The page's own object (`window.__lassoBrowser`, set once it mounts).
    private func page(_ call: String) {
        webView?.evaluateJavaScript("window.__lassoBrowser && window.__lassoBrowser." + call, completionHandler: nil)
    }

    /// Show `browser` in the page.
    func select(_ browser: String) { page("select(\(Self.literal(browser)))") }

    /// Show `browser` on the page an agent opened.
    func show(_ o: BrowserOpen) {
        let obj: [String: String] = ["profile": o.browser, "tab_id": o.tab_id ?? "", "url": o.url ?? "", "from": o.who]
        guard let data = try? JSONSerialization.data(withJSONObject: obj), let json = String(data: data, encoding: .utf8) else { return }
        page("show(\(json))")
    }

    /// A string as a JavaScript literal.
    nonisolated static func literal(_ s: String) -> String {
        guard let d = try? JSONSerialization.data(withJSONObject: [s]), let a = String(data: d, encoding: .utf8) else { return "\"\"" }
        return String(a.dropFirst().dropLast())
    }

    /// Runs in the page before its own script: the lasso page's native
    /// sockets and links, and WebKit's menu kept shut (the page's own
    /// right-clicks go to the browser).
    nonisolated static let script = LassoPage.script(
        handler: "lassoPage", global: "__lassoPage",
        rest: #"""
              addEventListener("contextmenu", (e) => e.preventDefault(), { capture: true });
        """#)
}
