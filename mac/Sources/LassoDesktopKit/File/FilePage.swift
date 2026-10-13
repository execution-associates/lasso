import AppKit
import Foundation
import WebKit

// A file window's editor (PLAN 26): lasso's /desktop-file.html
// (src/web/src/desktop/file-window.tsx), the web Files viewer's editor and
// markdown renderer and nothing else of the web app, run as a lasso page
// (Page/LassoPage.swift) under the `lasso-file:` scheme. Its requests carry
// this Mac's sign-in, so it reaches only what one window needs: its document
// and build files, its own file on its own machine (and the files its
// markdown names there, the images), and a write of that one file. A page
// that could name any path could read the machine; one that could write any
// path could own it.

/// The addresses the file page and lasso use.
public enum FilePage {
    public static let scheme = "lasso-file"
    static let pageHost = "lasso"
    static let documentPath = "/desktop-file.html"
    /// The document's query a page load may carry.
    static let fileQueryKeys: Set<String> = ["path", "host", "v"]

    /// The page, showing `path` on `host` as `kind`, at `line` if given (a
    /// line opens the source, raw).
    static func pageURL(path: String, host: String, kind: FileKind, mode: FileMode, line: Int?) -> URL {
        var pairs = [("path", path), ("host", host), ("kind", kind.rawValue), ("mode", line != nil ? "raw" : mode.rawValue)]
        if let line { pairs.append(("line", String(line))) }
        return URL(string: "\(scheme)://\(pageHost)\(documentPath)?" + query(pairs))!
    }

    /// A query lasso's Go reads back exactly: everything outside the
    /// unreserved set encoded, `+` included (Go reads a bare `+` as a space,
    /// and URLComponents leaves it bare).
    static func query(_ pairs: [(String, String)]) -> String {
        pairs.map { "\(encode($0.0))=\(encode($0.1))" }.joined(separator: "&")
    }

    static func encode(_ s: String) -> String {
        s.addingPercentEncoding(withAllowedCharacters: unreserved) ?? ""
    }

    private static let unreserved = CharacterSet(charactersIn: "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~/")

    /// A path a page may name: absolute (or the machine's home), no NUL.
    static func validPath(_ p: String) -> Bool {
        (p.hasPrefix("/") || p.hasPrefix("~/")) && !p.contains("\u{0}")
    }

    /// The path and query on lasso a page request asks for, or nil when the
    /// page may not have it:
    ///   - its document and its build's files;
    ///   - `GET /api/file` on the window's own machine, with nothing but a
    ///     path, its host and the page's cache-buster (`v`): never
    ///     `download`, `within` or another machine;
    ///   - `POST /api/file-write`, whose body `admitsWrite` then checks.
    static func lassoPath(_ url: URL, method: String, host: String) -> String? {
        guard url.scheme == scheme, url.host == pageHost,
              let c = URLComponents(url: url, resolvingAgainstBaseURL: false)
        else { return nil }
        let path = c.percentEncodedPath
        switch (method, path) {
        case ("GET", documentPath):
            return path + (c.percentEncodedQuery.map { "?" + $0 } ?? "")
        case ("GET", _) where LassoPageAssets.isAsset(path):
            return path
        case ("GET", "/api/file"), ("HEAD", "/api/file"):
            guard let file = filePath(c, host: host) else { return nil }
            return "/api/file?" + query([("path", file), ("host", host)])
        case ("POST", "/api/file-write"):
            return path
        default:
            return nil
        }
    }

    /// The file a page's `/api/file` asks for, when its query is one the
    /// window allows: path (once), host (once, the window's), v.
    static func filePath(_ c: URLComponents, host: String) -> String? {
        let items = c.queryItems ?? []
        var seen: Set<String> = []
        for i in items {
            guard fileQueryKeys.contains(i.name), seen.insert(i.name).inserted else { return nil }
        }
        guard let p = items.first(where: { $0.name == "path" })?.value, validPath(p) else { return nil }
        if let h = items.first(where: { $0.name == "host" }) {
            guard FileHost.normal(h.value) == FileHost.normal(host) else { return nil }
        }
        return p
    }

    /// A write's body names the window's own file on the window's own
    /// machine, and nothing past what the page sends.
    static func admitsWrite(_ body: Data?, path: String, host: String) -> Bool {
        guard let body, let obj = try? JSONSerialization.jsonObject(with: body) as? [String: Any],
              let p = obj["path"] as? String, p == path
        else { return false }
        let allowed: Set<String> = ["path", "host", "content", "base_mtime"]
        guard Set(obj.keys).isSubset(of: allowed), obj["content"] is String else { return false }
        let h = obj["host"]
        if h != nil, !(h is String) { return false }
        return FileHost.normal(h as? String) == FileHost.normal(host)
    }
}

/// What the page says of its buffer (`file-state`).
struct FilePageState: Equatable, Sendable {
    var dirty = false
    var mode: FileMode = .raw
    var saving = false
    var saveError: String?
    var conflict = false
    var loaded = false
    var missing = false
    /// lasso sent its "too large" line, not the file: nothing to save.
    var readOnly = false
    var mtime: Int64?

    init() {}

    init(_ body: [String: Any]) {
        dirty = (body["dirty"] as? NSNumber)?.boolValue ?? false
        mode = FileMode(rawValue: body["mode"] as? String ?? "") ?? .raw
        saving = (body["saving"] as? NSNumber)?.boolValue ?? false
        saveError = body["saveError"] as? String
        conflict = (body["conflict"] as? NSNumber)?.boolValue ?? false
        loaded = (body["loaded"] as? NSNumber)?.boolValue ?? false
        missing = (body["missing"] as? NSNumber)?.boolValue ?? false
        readOnly = (body["readOnly"] as? NSNumber)?.boolValue ?? false
        mtime = (body["mtime"] as? NSNumber)?.int64Value
    }
}

/// A file window's page: its requests confined to its one file, the files
/// it loaded noted for the watcher, and its state handed to the window.
@MainActor
final class FileBridge: LassoPageBridge {
    let host: String
    let path: String
    /// The page has mounted and takes calls.
    var onReady: () -> Void = {}
    var onState: (FilePageState) -> Void = { _ in }
    var onSaved: (Int64) -> Void = { _ in }
    /// Every other file the page loaded through `/api/file`, oldest first.
    private(set) var served: [String] = []

    init(client: LassoClient, host: String, path: String) {
        self.host = FileHost.normal(host)
        self.path = path
        super.init(client: client, global: "__lassoPage")
    }

    /// A file's text is at most FileKind.textLimit; JSON's escapes can
    /// double it, and a little more is the rest of the body.
    override var maxBody: Int { 3 * Int(FileKind.textLimit) + (64 << 10) }

    override func lassoPath(_ url: URL, method: String) -> String? {
        guard let out = FilePage.lassoPath(url, method: method, host: host) else { return nil }
        if out.hasPrefix("/api/file?"), let c = URLComponents(url: url, resolvingAgainstBaseURL: false),
           let p = FilePage.filePath(c, host: host), p != path, !served.contains(p)
        {
            served.append(p)
        }
        return out
    }

    override func admit(path: String, method: String, body: Data?) -> Bool {
        guard method == "POST" else { return true }
        return path == "/api/file-write" && FilePage.admitsWrite(body, path: self.path, host: host)
    }

    override func handle(_ op: String, _ body: [String: Any]) {
        switch op {
        case "ready": onReady()
        case "file-state": onState(FilePageState(body))
        case "file-saved": onSaved((body["mtime"] as? NSNumber)?.int64Value ?? 0)
        default: break
        }
    }

    /// The page's own object (`window.__lassoFile`).
    private func page(_ call: String) {
        webView?.evaluateJavaScript("window.__lassoFile && window.__lassoFile." + call, completionHandler: nil)
    }

    func changed() { page("changed()") }
    func assetsChanged() { page("assetsChanged()") }
    func setMode(_ m: FileMode) { page("setMode(\(BrowserBridge.literal(m.rawValue)))") }
    func save() { page("save()") }
    func takeDisk() { page("takeDisk()") }
    func keepMine() { page("keepMine()") }

    /// Show `line` (raw, scrolled there).
    func load(kind: FileKind, mode: FileMode, line: Int?) {
        var obj: [String: Any] = ["path": path, "host": host, "kind": kind.rawValue, "mode": line != nil ? "raw" : mode.rawValue]
        if let line { obj["line"] = line }
        guard let d = try? JSONSerialization.data(withJSONObject: obj), let json = String(data: d, encoding: .utf8) else { return }
        page("load(\(json))")
    }

    /// The buffer as typed, saved or not.
    func draft() async -> String? {
        try? await webView?.evaluateJavaScript("window.__lassoFile ? window.__lassoFile.draft() : null") as? String
    }

    /// Runs in the page before its own script: the lasso page's links.
    nonisolated static let script = LassoPage.script(handler: "lassoPage", global: "__lassoPage")
}

/// The file page's web view: an editor keeps its Cut, Copy, Paste and
/// spelling menu, and loses WebKit's Reload, Inspect and link items (a link
/// opens in the Mac's browser).
final class FileWebView: LassoPageWebView {
    override func willOpenMenu(_ menu: NSMenu, with event: NSEvent) {
        for item in menu.items {
            let id = item.identifier?.rawValue ?? ""
            if ["Reload", "Inspect", "Link", "Download", "Back", "Forward", "Image"].contains(where: id.contains) {
                menu.removeItem(item)
            }
        }
    }
}
