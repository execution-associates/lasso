import AppKit
import Foundation
import Testing
import WebKit

@testable import LassoDesktopKit

// A file window's editor end to end on this Mac (PLAN 26): lasso's real built
// page (src/web/dist's desktop-file.html, when it is beside the package), the
// app's window, scheme handler and bridge, against a stand-in lasso that
// keeps one file and answers /api/file and /api/file-write as lasso does,
// mtimes and 409 included. Without the build this checks nothing.

private func fileDist() -> URL? {
    let pkg = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
    let dist = pkg.deletingLastPathComponent().appendingPathComponent("src/web/dist")
    return FileManager.default.fileExists(atPath: dist.appendingPathComponent("desktop-file.html").path) ? dist : nil
}

/// lasso for one file: its text and mtime, every write kept, the build's files.
final class FileDistStub: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var dist: URL?
    nonisolated(unsafe) static var disk = ""
    nonisolated(unsafe) static var mtime: Int64 = 1000
    nonisolated(unsafe) static var writes: [[String: Any]] = []
    nonisolated(unsafe) static var seen: [String] = []
    override class func canInit(with request: URLRequest) -> Bool { request.url?.scheme == "http" }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let url = request.url!
        Self.seen.append("\(request.httpMethod ?? "GET") \(url.path)")
        var status = 200
        var headers = ["Content-Type": "text/plain; charset=utf-8"]
        var body = Data()
        switch url.path {
        case "/api/file":
            body = Data(Self.disk.utf8)
            headers["X-Lasso-Mtime"] = String(Self.mtime)
        case "/api/file-write":
            var raw = request.httpBody ?? Data()
            if raw.isEmpty, let s = request.httpBodyStream {
                s.open()
                var buf = [UInt8](repeating: 0, count: 1 << 16)
                while s.hasBytesAvailable {
                    let n = s.read(&buf, maxLength: buf.count)
                    if n <= 0 { break }
                    raw.append(buf, count: n)
                }
                s.close()
            }
            let obj = (try? JSONSerialization.jsonObject(with: raw) as? [String: Any]) ?? [:]
            Self.writes.append(obj)
            headers["Content-Type"] = "application/json"
            if let base = (obj["base_mtime"] as? NSNumber)?.int64Value, base != Self.mtime {
                status = 409
                body = Data(#"{"error":"changed on disk","mtime":\#(Self.mtime)}"#.utf8)
            } else {
                Self.disk = obj["content"] as? String ?? ""
                Self.mtime += 1000
                body = Data(#"{"ok":true,"mtime":\#(Self.mtime)}"#.utf8)
            }
        default:
            if let dist = Self.dist, let d = try? Data(contentsOf: dist.appendingPathComponent(String(url.path.dropFirst()))) {
                body = d
                headers["Content-Type"] =
                    url.pathExtension == "html" ? "text/html" : url.pathExtension == "js" ? "text/javascript"
                    : url.pathExtension == "css" ? "text/css" : "application/octet-stream"
            } else {
                status = 404
            }
        }
        let resp = HTTPURLResponse(url: url, statusCode: status, httpVersion: "HTTP/1.1", headerFields: headers)!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: body)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

/// A key's press and release as the window server hands them to the app.
@MainActor private func press(_ w: NSWindow, _ chars: String, _ code: UInt16, _ flags: NSEvent.ModifierFlags = []) {
    for t in [NSEvent.EventType.keyDown, .keyUp] {
        let e = NSEvent.keyEvent(
            with: t, location: .zero, modifierFlags: flags, timestamp: ProcessInfo.processInfo.systemUptime,
            windowNumber: w.windowNumber, context: nil, characters: chars, charactersIgnoringModifiers: chars, isARepeat: false,
            keyCode: code)!
        NSApp.sendEvent(e)
    }
}

/// Load, type, ⌘S (with the mtime it was read at), ⌘Z, ⌘F; a rewrite on
/// disk under the edits is a banner and leaves them; a save over a newer
/// file is refused (409) and says so. All with the app never active, as
/// Lasso is while another app is in front.
@MainActor @Test func theRealFilePageEditsAndSaves() async throws {
    guard let dist = fileDist() else { return }
    FileDistStub.dist = dist
    FileDistStub.disk = "first line\nsecond line\n"
    FileDistStub.mtime = 1000
    FileDistStub.writes = []
    FileDistStub.seen = []
    let client = LassoClient(baseURL: URL(string: "http://127.0.0.1:9")!, protocolClasses: [FileDistStub.self])
    let source = FakeFileSource()
    source.put("/r/notes.txt")
    let c = FileWindowController(
        open: FileOpen(path: "/r/notes.txt", from: "olivia"), kind: .text, stat: source.files["/r/notes.txt"], hostLabel: "titan",
        client: client, source: source)
    defer { c.close() }
    c.show(takeKey: true)
    let w = c.panel
    w.setFrame(NSRect(x: 500, y: 400, width: 800, height: 600), display: true)
    let web = try #require(c.pageView)
    func js(_ s: String) async -> String { ((try? await web.evaluateJavaScript(s)) as? String) ?? "" }
    let text = "document.querySelector('.cm-content')?.innerText ?? ''"
    await until(10) { c.model.page.loaded }
    #expect(c.model.page.loaded && c.model.phase == .live, "requests: \(FileDistStub.seen)")
    #expect(await js(text).contains("first line"))
    #expect(!FileDistStub.seen.contains { !["/desktop-file.html", "/api/file"].contains($0.split(separator: " ")[1]) && !$0.contains("/assets/") },
            "only its own routes: \(FileDistStub.seen)")

    // Typing reaches CodeMirror, with the window key and the app inactive.
    _ = try? await web.evaluateJavaScript("document.querySelector('.cm-content').focus()")
    #expect(w.isKeyWindow)
    press(w, "x", 7)
    await until(3) { c.model.dirty }
    let typed = await js(text)
    #expect(c.model.dirty && typed.hasPrefix("xfirst"))

    // ⌘S: saved with the mtime it was read at.
    press(w, "s", 1, .command)
    await until(3) { !c.model.dirty }
    #expect(FileDistStub.writes.count == 1)
    #expect((FileDistStub.writes.first?["base_mtime"] as? NSNumber)?.int64Value == 1000)
    #expect(FileDistStub.disk.hasPrefix("xfirst") && !c.model.dirty)

    // ⌘Z undoes in CodeMirror; ⌘F opens its search.
    press(w, "z", 6, .command)
    await until(3) { (await js(text)).hasPrefix("first") }
    #expect((await js(text)).hasPrefix("first"), "⌘Z")
    press(w, "f", 3, .command)
    await until(3) { await js("document.querySelector('.cm-search') ? 'yes' : ''") == "yes" }
    #expect(await js("document.querySelector('.cm-search') ? 'yes' : ''") == "yes", "⌘F")
    press(w, "\u{1b}", 53)

    // Rewritten on disk under unsaved edits: a banner, the edits kept.
    _ = try? await web.evaluateJavaScript("document.querySelector('.cm-content').focus()")
    press(w, "y", 16)
    await until(3) { c.model.dirty }
    FileDistStub.disk = "rewritten by the agent\n"
    FileDistStub.mtime = 5000
    c.docChanged(FileStatEntry(path: "/r/notes.txt", exists: true, size: 23, mtime: 5000))
    await until(3) { c.model.banner == .changed }
    #expect(c.model.banner == .changed && c.model.dirty)
    #expect(!(await js(text)).contains("rewritten"), "the edits stay")
    c.model.onTakeDisk()
    await until(3) { (await js(text)).contains("rewritten") }
    let taken = await js(text)
    #expect(taken.contains("rewritten") && c.model.banner == nil && !c.model.dirty)

    // A save over a file that changed since it was read: refused, said.
    _ = try? await web.evaluateJavaScript("document.querySelector('.cm-content').focus()")
    press(w, "z", 6)
    await until(3) { c.model.dirty }
    // (Its mtime alone moving is no conflict: the page compares content.)
    FileDistStub.disk = "rewritten again\n"
    FileDistStub.mtime = 6000
    c.model.onSave()
    await until(3) { c.model.banner == .conflict }
    #expect(c.model.banner == .conflict, "page: \(c.model.page), writes: \(FileDistStub.writes.count), \(FileDistStub.seen.suffix(4))")
    #expect(FileDistStub.disk == "rewritten again\n", "the file is untouched")
}

/// lasso's files for a rendered report: its HTML, the stylesheet beside it,
/// and every request kept (with its query: `within` is lasso's half of the
/// confinement).
final class DocStub: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var seen: [String] = []
    nonisolated(unsafe) static var files: [String: (String, String)] = [:]
    override class func canInit(with request: URLRequest) -> Bool { request.url?.scheme == "http" }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let url = request.url!
        let q = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems ?? []
        let path = q.first { $0.name == "path" }?.value ?? ""
        Self.seen.append("\(path) within=\(q.first { $0.name == "within" }?.value ?? "-") download=\(q.first { $0.name == "download" }?.value ?? "-")")
        let hit = Self.files[path]
        let resp = HTTPURLResponse(
            url: url, statusCode: hit == nil ? 404 : 200, httpVersion: "HTTP/1.1",
            headerFields: ["Content-Type": hit?.1 ?? "text/plain", "Content-Disposition": "attachment"])!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data((hit?.0 ?? "").utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

/// A rendered HTML file loads the stylesheet beside it through its own
/// origin, carries the policy, never reaches outside its folder (lasso is
/// asked with `within` too), and shows the unsaved buffer when there is one.
@MainActor @Test func aRenderedReportLoadsItsFolderOnly() async throws {
    DocStub.seen = []
    DocStub.files = [
        "/r/report/index.html": (
            #"<!doctype html><html><head><link rel="stylesheet" href="style.css"></head><body><h1>Report</h1><img src="../secret.png"><img src="http://example.com/x.png"></body></html>"#,
            "text/html; charset=utf-8"
        ),
        "/r/report/style.css": ("body { background: rgb(1, 2, 3) }", "text/css; charset=utf-8"),
        "/r/secret.png": ("x", "image/png"),
    ]
    let client = LassoClient(baseURL: URL(string: "http://127.0.0.1:9")!, protocolClasses: [DocStub.self])
    let d = DocView(client: client, host: "local", path: "/r/report/index.html")
    defer { d.teardown() }
    let window = NSWindow(contentRect: NSRect(x: -3000, y: -3000, width: 600, height: 400), styleMask: .borderless, backing: .buffered, defer: false)
    window.contentView = d.web
    var loaded = 0
    var failed: [String] = []
    d.onLoaded = { loaded += 1 }
    d.onFailed = { failed.append($0) }
    d.load()
    await until(10) { loaded == 1 || !failed.isEmpty }
    #expect(loaded == 1, "failed: \(failed), url: \(String(describing: d.web.url))")
    func js(_ s: String) async -> String { ((try? await d.web.evaluateJavaScript(s)) as? String) ?? "" }
    await until(3) { await js("getComputedStyle(document.body).backgroundColor") == "rgb(1, 2, 3)" }
    #expect(await js("getComputedStyle(document.body).backgroundColor") == "rgb(1, 2, 3)", "seen: \(DocStub.seen)")
    #expect(await js("document.querySelector('meta[http-equiv]')?.getAttribute('content') ?? ''") == DocPage.csp)
    #expect(await js("document.querySelector('h1')?.textContent ?? ''") == "Report")
    #expect(DocStub.seen.contains("/r/report/style.css within=/r/report download=1"))
    #expect(!DocStub.seen.contains { $0.hasPrefix("/r/secret.png") }, "outside its folder: never asked for")
    #expect(d.served == ["/r/report/style.css"])

    // Unsaved edits in the source are what it renders.
    d.draft = { "<!doctype html><p id=d>draft</p>" }
    d.load()
    await until(10) { loaded == 2 }
    await until(3) { await js("document.getElementById('d')?.textContent ?? ''") == "draft" }
    #expect(await js("document.getElementById('d')?.textContent ?? ''") == "draft")
}
