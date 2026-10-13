import AppKit
import Foundation
import WebKit

// A rendered HTML file (PLAN 26): the agent's report, with its own scripts,
// styles and images from beside it. It is untrusted (an agent wrote it, or it
// came off the internet), so it runs in a web view of its own, never inside
// the file page:
//
//   - a separate top-level WKWebView with a non-persistent store, no lasso
//     scheme and no message handler: nothing in it can reach the bridge
//     (an iframe in the file page could POST to `lasso-file:` and write);
//   - its files come through `lasso-doc://<token>/<absolute path>`, GET and
//     HEAD only, the token this window's and the path inside the HTML's own
//     folder (lexically here, and again by lasso: `within=`, which also
//     refuses dot folders, symlinks and a folder at or above home);
//   - a CSP lasso's web preview uses (src/web/src/lib/html-preview.ts) with
//     its own folder and https allowed, and a content rule list that blocks
//     every load but those, data:, blob: and about:;
//   - a link opens in the Mac's browser.
//
// Known limit: a page can read its own folder and send it out over https.
// That is decision 3 (CDN scripts work), bounded by the folder rules above.

public enum DocPage {
    public static let scheme = "lasso-doc"

    /// The document, or a file beside it, in this window's origin.
    static func url(token: String, path: String) -> URL {
        URL(string: "\(scheme)://\(token)" + FilePage.encode(path))!
    }

    /// The folder an HTML file may load from: its own.
    static func root(of path: String) -> String { (path as NSString).deletingLastPathComponent }

    /// The absolute path a request names, when it is this window's (its
    /// token) and inside `root`: no encoded slash or backslash, no NUL, no
    /// empty, `.` or `..` segment anywhere (WebKit resolves most of these
    /// before a request is made; `%2e%2e` it may not), and below the root
    /// nothing hidden. A root of `/` allows its own files only (lasso also
    /// holds home and anything above it to that).
    static func confined(_ url: URL, token: String, root: String) -> String? {
        guard url.scheme == scheme, url.host == token,
              let raw = URLComponents(url: url, resolvingAgainstBaseURL: false)?.percentEncodedPath
        else { return nil }
        let lower = raw.lowercased()
        guard !["%2f", "%5c", "%00", "\\"].contains(where: lower.contains), let path = raw.removingPercentEncoding,
              path.hasPrefix("/"), !path.contains("\u{0}"), !path.contains("\\")
        else { return nil }
        let segments = path.split(separator: "/", omittingEmptySubsequences: false).dropFirst()
        guard !segments.isEmpty, !segments.contains(where: { $0.isEmpty || $0 == "." || $0 == ".." }) else { return nil }
        let base = root == "/" ? "" : root
        guard path.hasPrefix(base + "/") else { return nil }
        let below = path.dropFirst(base.count + 1).split(separator: "/")
        guard !below.isEmpty, !below.contains(where: { $0.hasPrefix(".") }) else { return nil }
        if root == "/", below.count != 1 { return nil }
        return path
    }

    /// lasso's web preview's policy (html-preview.ts), with the document's
    /// own origin (its folder, through this scheme) allowed beside https for
    /// scripts, styles, fonts, images, media and fetches. Frames are its own
    /// HTML files only, each served with this policy too; forms post nowhere.
    static let csp = [
        "default-src 'none'",
        "script-src 'self' \(scheme): 'unsafe-inline' 'unsafe-eval' https: data: blob:",
        "style-src 'self' \(scheme): 'unsafe-inline' https: data:",
        "img-src 'self' \(scheme): https: data: blob:",
        "font-src 'self' \(scheme): https: data:",
        "media-src 'self' \(scheme): https: data: blob:",
        "worker-src 'self' blob: data:",
        "connect-src 'self' \(scheme): https:",
        "form-action 'none'",
        "frame-src 'self'",
        "base-uri 'self'",
    ].joined(separator: "; ")

    /// The policy as a <meta>, inserted right after the doctype (before any
    /// other tag, the parser opens an implicit head for it; before the
    /// doctype, the page would drop to quirks mode). htmlPreviewDoc's rule.
    static func inject(_ html: String) -> String {
        let meta = "<meta http-equiv=\"Content-Security-Policy\" content=\"\(csp.replacingOccurrences(of: "&", with: "&amp;").replacingOccurrences(of: "\"", with: "&quot;"))\">"
        let ns = html as NSString
        if let m = doctype.firstMatch(in: html, range: NSRange(location: 0, length: ns.length)) {
            let head = ns.substring(to: m.range.upperBound)
            return head + meta + ns.substring(from: m.range.upperBound)
        }
        return meta + html
    }

    /// A leading BOM, whitespace and comments may come before the doctype.
    private static let doctype = try! NSRegularExpression(
        pattern: #"^\x{feff}?(?:\s|<!--[\s\S]*?-->)*<!doctype[^>]*>"#, options: [.caseInsensitive])

    /// Every load is blocked but this window's own scheme and token, https,
    /// data:, blob: and about:.
    static func rules(token: String) -> String {
        let allow = ["^\(scheme)://\(token)/", "^https:", "^data:", "^blob:", "^about:"]
        let list: [[String: Any]] =
            [["trigger": ["url-filter": ".*"], "action": ["type": "block"]]]
            + allow.map { ["trigger": ["url-filter": $0], "action": ["type": "ignore-previous-rules"]] }
        let d = try! JSONSerialization.data(withJSONObject: list)
        return String(decoding: d, as: UTF8.self)
    }

    /// HTML files get the policy; everything else is streamed as lasso sends it.
    static func isHTML(_ path: String) -> Bool { ["html", "htm", "xhtml"].contains((path as NSString).pathExtension.lowercased()) }

    /// lasso's response headers a document's file keeps: what a range
    /// request (a video's) needs, never Content-Disposition (`download=1`
    /// sets it, and it would turn the page into a download).
    static let passedHeaders = ["Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified"]

    /// Largest HTML file read whole to put the policy in.
    static let htmlLimit = 16 << 20
}

/// One rendered HTML file: its web view, the scheme its files come through,
/// and the window's way to reload it where it was scrolled.
@MainActor
final class DocView: NSObject {
    let client: LassoClient
    let host: String
    let path: String
    let root: String
    /// This window's origin: another window's, or a guessed one, is refused.
    let token: String
    let web: FileWebView
    /// The unsaved buffer, when there is one: the document is served from it.
    var draft: () async -> String? = { nil }
    var onLoaded: () -> Void = {}
    var onFailed: (String) -> Void = { _ in }
    /// Every other file the document loaded, oldest first, for the watcher.
    private(set) var served: [String] = []
    private var rules: WKContentRuleList?
    private var scroll: (Double, Double)?
    private var streams: DocStreams!

    init(client: LassoClient, host: String, path: String) {
        self.client = client
        self.host = FileHost.normal(host)
        self.path = path
        root = DocPage.root(of: path)
        token = UUID().uuidString.replacingOccurrences(of: "-", with: "").lowercased()
        // A web view copies its configuration when it is made: the scheme
        // is set first, its handler pointed here once there is a here.
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .nonPersistent()
        let handler = WeakSchemeHandler()
        config.setURLSchemeHandler(handler, forURLScheme: DocPage.scheme)
        web = FileWebView(frame: .zero, configuration: config)
        super.init()
        handler.target = self
        streams = DocStreams(view: self)
        web.navigationDelegate = self
        web.uiDelegate = self
        web.underPageBackgroundColor = Theme.Panel.surface.nsColor
        web.setAccessibilityLabel("Rendered page")
    }

    /// Load (or reload) the document, kept where it was scrolled.
    func load() {
        Task {
            if web.url != nil, let pos = try? await web.evaluateJavaScript("[window.scrollX, window.scrollY]") as? [Double], pos.count == 2 {
                scroll = (pos[0], pos[1])
            }
            if rules == nil {
                rules = try? await WKContentRuleListStore.default().compileContentRuleList(
                    forIdentifier: "lasso-doc-" + token, encodedContentRuleList: DocPage.rules(token: token))
                if let rules { web.configuration.userContentController.add(rules) }
            }
            // No rules, no page: unblocked, it could load http anywhere.
            guard rules != nil else { return onFailed("Could not set up the page's sandbox") }
            web.load(URLRequest(url: DocPage.url(token: token, path: path)))
        }
    }

    func teardown() {
        streams.cancelAll()
        web.stopLoading()
        web.loadHTMLString("", baseURL: nil)
        if rules != nil {
            rules = nil
            WKContentRuleListStore.default().removeContentRuleList(forIdentifier: "lasso-doc-" + token) { _ in }
        }
    }

    /// The request for `file` on lasso: whole (`download=1`, past the
    /// preview cap) and confined by lasso as well (`within`).
    func request(_ file: String, method: String) -> URLRequest {
        var r = client.request(
            raw: "/api/file?" + FilePage.query([("path", file), ("host", host), ("download", "1"), ("within", root)]))
        r.httpMethod = method
        r.setValue("*/*", forHTTPHeaderField: "Accept")
        return r
    }
}

extension DocView: WKURLSchemeHandler {
    func webView(_ webView: WKWebView, start task: any WKURLSchemeTask) {
        let method = (task.request.httpMethod ?? "GET").uppercased()
        guard ["GET", "HEAD"].contains(method), let url = task.request.url,
              let file = DocPage.confined(url, token: token, root: root)
        else {
            task.didFailWithError(URLError(.noPermissionsToReadFile))
            return
        }
        if file != path, !served.contains(file) { served.append(file) }
        if DocPage.isHTML(file) {
            html(task, url: url, file: file, method: method)
        } else {
            var r = request(file, method: method)
            if let range = task.request.value(forHTTPHeaderField: "Range") { r.setValue(range, forHTTPHeaderField: "Range") }
            streams.start(task, r)
        }
    }

    func webView(_ webView: WKWebView, stop task: any WKURLSchemeTask) { streams.stop(task) }

    /// An HTML file, read whole (or the unsaved buffer, for the document),
    /// with the policy put in.
    private func html(_ task: any WKURLSchemeTask, url: URL, file: String, method: String) {
        let box = SchemeBox(task)
        streams.track(box)
        Task {
            var text: String?
            var code = 200
            if file == path, let d = await draft() {
                text = d
            } else {
                do {
                    let (data, http) = try await client.response(request(file, method: "GET"))
                    code = http?.statusCode ?? 502
                    if (200..<300).contains(code), data.count <= DocPage.htmlLimit {
                        text = String(decoding: data, as: UTF8.self)
                    } else if code == 200 {
                        code = 413
                    }
                } catch {
                    code = 502
                }
            }
            guard streams.live(box) else { return }
            streams.untrack(box)
            let body = Data((text.map(DocPage.inject) ?? "").utf8)
            let out = HTTPURLResponse(
                url: url, statusCode: text == nil ? code : 200, httpVersion: "HTTP/1.1",
                headerFields: ["Content-Type": "text/html; charset=utf-8", "Cache-Control": "no-store"])!
            box.task.didReceive(out)
            if method == "GET" { box.task.didReceive(body) }
            box.task.didFinish()
        }
    }
}

extension DocView: WKNavigationDelegate {
    func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction) async -> WKNavigationActionPolicy {
        guard let url = action.request.url else { return .cancel }
        if url.scheme == DocPage.scheme, url.host == token { return .allow }
        if url.scheme == "about" { return .allow }
        if ["http", "https", "mailto"].contains(url.scheme?.lowercased() ?? ""), action.targetFrame?.isMainFrame ?? true {
            NSWorkspace.shared.open(url)
        }
        return .cancel
    }

    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        if let (x, y) = scroll {
            scroll = nil
            // Once now, and again once late images and fonts have laid out.
            let js = "window.scrollTo(\(x), \(y))"
            webView.evaluateJavaScript(js, completionHandler: nil)
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.3) { webView.evaluateJavaScript(js, completionHandler: nil) }
        }
        onLoaded()
    }

    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: any Error) {
        guard (error as NSError).code != NSURLErrorCancelled else { return }
        onFailed(error.localizedDescription)
    }

    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) { load() }
}

extension DocView: WKUIDelegate {
    /// A link asking for a new window opens in the Mac's browser instead.
    func webView(
        _ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration, for action: WKNavigationAction,
        windowFeatures: WKWindowFeatures
    ) -> WKWebView? {
        if let url = action.request.url, ["http", "https", "mailto"].contains(url.scheme?.lowercased() ?? "") {
            NSWorkspace.shared.open(url)
        }
        return nil
    }
}

/// The configuration keeps its scheme handler for the web view's life: this
/// keeps the window's view out of that cycle.
final class WeakSchemeHandler: NSObject, WKURLSchemeHandler {
    weak var target: (any WKURLSchemeHandler)?

    init(_ target: (any WKURLSchemeHandler)? = nil) { self.target = target }

    func webView(_ webView: WKWebView, start task: any WKURLSchemeTask) {
        guard let target else { return task.didFailWithError(URLError(.cancelled)) }
        target.webView(webView, start: task)
    }

    func webView(_ webView: WKWebView, stop task: any WKURLSchemeTask) { target?.webView(webView, stop: task) }
}

/// A scheme task held across an await.
final class SchemeBox: @unchecked Sendable {
    let task: any WKURLSchemeTask
    init(_ task: any WKURLSchemeTask) { self.task = task }
}

/// A document's files that are not HTML, streamed from lasso to WebKit as
/// they arrive (a video is never read whole), with a range request passed
/// through both ways: WebKit's media loader asks for ranges and wants 206,
/// Content-Range and Accept-Ranges back. A 401 refreshes the sign-in once
/// and asks again.
@MainActor
final class DocStreams: NSObject {
    private weak var view: DocView?
    private final class Flow {
        let box: SchemeBox
        var request: URLRequest
        var task: URLSessionDataTask?
        var retried = false
        var answered = false
        init(_ box: SchemeBox, _ request: URLRequest) {
            self.box = box
            self.request = request
        }
    }

    private var flows: [ObjectIdentifier: Flow] = [:]
    private var byTask: [Int: ObjectIdentifier] = [:]
    private var held: Set<ObjectIdentifier> = []
    private var sessionStore: URLSession?
    private var session: URLSession {
        if let s = sessionStore { return s }
        // The client's own settings (ephemeral, no cookies, no cache, its
        // timeouts), with a delegate to stream through.
        let cfg = view?.client.urlSession.configuration ?? .ephemeral
        let s = URLSession(configuration: cfg, delegate: DocStreamEvents(self), delegateQueue: .main)
        sessionStore = s
        return s
    }

    init(view: DocView) { self.view = view }

    // An HTML file's task, answered whole elsewhere: held so a stop is heard.
    func track(_ b: SchemeBox) { held.insert(ObjectIdentifier(b.task)) }
    func untrack(_ b: SchemeBox) { held.remove(ObjectIdentifier(b.task)) }
    func live(_ b: SchemeBox) -> Bool { held.contains(ObjectIdentifier(b.task)) }

    func start(_ t: any WKURLSchemeTask, _ r: URLRequest) {
        let flow = Flow(SchemeBox(t), r)
        flows[ObjectIdentifier(t)] = flow
        Task { await send(flow) }
    }

    private func send(_ f: Flow) async {
        guard let client = view?.client else { return }
        await client.auth?.freshen()
        var r = f.request
        client.authorize(&r)
        guard flows[ObjectIdentifier(f.box.task)] === f else { return }
        let task = session.dataTask(with: r)
        f.task = task
        byTask[task.taskIdentifier] = ObjectIdentifier(f.box.task)
        task.resume()
    }

    func stop(_ t: any WKURLSchemeTask) {
        let id = ObjectIdentifier(t)
        held.remove(id)
        guard let f = flows.removeValue(forKey: id) else { return }
        f.task?.cancel()
    }

    func cancelAll() {
        for f in flows.values { f.task?.cancel() }
        flows.removeAll()
        held.removeAll()
        sessionStore?.invalidateAndCancel()
        sessionStore = nil
    }

    private func flow(_ task: URLSessionTask) -> Flow? {
        byTask[task.taskIdentifier].flatMap { flows[$0] }
    }

    fileprivate func response(_ task: URLSessionTask, _ resp: URLResponse) -> URLSession.ResponseDisposition {
        guard let f = flow(task), let http = resp as? HTTPURLResponse else { return .cancel }
        if http.statusCode == 401, !f.retried, let auth = view?.client.auth {
            f.retried = true
            byTask[task.taskIdentifier] = nil
            Task {
                guard await auth.refreshAfterRefusal(), flows[ObjectIdentifier(f.box.task)] === f else { return fail(f) }
                await send(f)
            }
            return .cancel
        }
        var headers = ["Cache-Control": "no-store"]
        for h in DocPage.passedHeaders {
            if let v = http.value(forHTTPHeaderField: h) { headers[h] = v }
        }
        let url = f.box.task.request.url ?? URL(string: "\(DocPage.scheme)://x")!
        f.box.task.didReceive(HTTPURLResponse(url: url, statusCode: http.statusCode, httpVersion: "HTTP/1.1", headerFields: headers)!)
        f.answered = true
        return .allow
    }

    fileprivate func data(_ task: URLSessionTask, _ d: Data) {
        guard let f = flow(task), f.answered else { return }
        f.box.task.didReceive(d)
    }

    fileprivate func done(_ task: URLSessionTask, _ error: (any Error)?) {
        guard let f = flow(task) else { return }
        byTask[task.taskIdentifier] = nil
        flows[ObjectIdentifier(f.box.task)] = nil
        if f.answered, error == nil { f.box.task.didFinish() } else { f.box.task.didFailWithError(error ?? URLError(.badServerResponse)) }
    }

    private func fail(_ f: Flow) {
        guard flows.removeValue(forKey: ObjectIdentifier(f.box.task)) != nil else { return }
        f.box.task.didFailWithError(URLError(.userAuthenticationRequired))
    }
}

/// The stream session's delegate, on the main queue.
private final class DocStreamEvents: NSObject, URLSessionDataDelegate, @unchecked Sendable {
    weak var streams: DocStreams?

    init(_ s: DocStreams) { streams = s }

    func urlSession(
        _ session: URLSession, dataTask: URLSessionDataTask, didReceive response: URLResponse,
        completionHandler: @escaping (URLSession.ResponseDisposition) -> Void
    ) {
        let d = MainActor.assumeIsolated { streams?.response(dataTask, response) ?? .cancel }
        completionHandler(d)
    }

    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
        MainActor.assumeIsolated { streams?.data(dataTask, data) }
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: (any Error)?) {
        MainActor.assumeIsolated { streams?.done(task, error) }
    }
}
