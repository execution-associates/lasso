import AppKit
import Foundation
import Testing
import WebKit

@testable import LassoDesktopKit

// File windows (PLAN 26): what a file is shown as, what its page may reach on
// lasso, what its rendered HTML may load, the watcher, the gate an agent's
// `open-file` passes, and the windows themselves (reopen, cascade, the cap,
// the unsaved-close confirm, a lasso switch). The real page against lasso's
// built web app is FilePageTests'.

// MARK: stand-ins

/// lasso's file routes, canned: a file's stat, its bytes, and the requests seen.
@MainActor final class FakeFileSource: FileSource {
    var files: [String: FileStatEntry] = [:]
    /// An older lasso: no /api/file-stat.
    var refuse = false
    var stats: [(host: String, paths: [String])] = []
    var downloads: [String] = []
    /// What a download writes, by path (else a few bytes).
    var bytes: [String: URL] = [:]

    func stat(host: String, paths: [String]) async throws -> [FileStatEntry] {
        if refuse { throw LassoError.http(404, "no such route on this lasso") }
        stats.append((host, paths))
        return paths.map { files[$0] ?? FileStatEntry(path: $0, exists: false) }
    }

    func download(host: String, path: String, to dest: URL, progress: @escaping @Sendable (Int64, Int64) -> Void) async throws {
        downloads.append(path)
        try FileManager.default.createDirectory(at: dest.deletingLastPathComponent(), withIntermediateDirectories: true)
        if FileManager.default.fileExists(atPath: dest.path) { try FileManager.default.removeItem(at: dest) }
        if let src = bytes[path] { try FileManager.default.copyItem(at: src, to: dest) } else { try Data("x".utf8).write(to: dest) }
    }

    func put(_ path: String, size: Int64 = 10, mtime: Int64 = 1000, dir: Bool = false) {
        files[path] = FileStatEntry(path: path, exists: true, dir: dir, size: size, mtime: mtime)
    }
}

/// A window as the watcher sees one.
@MainActor final class FakeWatched: FileWatched {
    var watchHost = "local"
    var watchDoc: String
    var watchAssets: [String] = []
    var watchVisible = true
    var changed: [FileStatEntry] = []
    var deleted = 0
    var assets = 0

    init(_ doc: String, host: String = "local") {
        watchDoc = doc
        watchHost = host
    }

    func docChanged(_ e: FileStatEntry) { changed.append(e) }
    func docDeleted() { deleted += 1 }
    func assetsChanged() { assets += 1 }
}

/// A stand-in for lasso's file page: a field for the buffer, `__lassoFile`
/// logging what native calls, and `file-state` posted as the real page does.
let standInFilePage = #"""
<html><body style="margin:0"><textarea id="t" style="width:100vw;height:100vh"></textarea><script>
window.__log = [];
const post = (m) => webkit.messageHandlers.lassoPage.postMessage(m);
const t = document.getElementById("t");
const state = (dirty) => post({ op: "file-state", dirty, mode: "raw", saving: false, saveError: null, conflict: false, loaded: true, missing: false, readOnly: false, mtime: 1000 });
window.__lassoFile = {
  changed() { __log.push("changed") }, assetsChanged() { __log.push("assets") }, setMode(m) { __log.push("mode " + m) },
  save() { __log.push("save"); state(false) }, takeDisk() { __log.push("takeDisk") }, keepMine() { __log.push("keepMine") },
  draft() { return t.value }, load(o) { __log.push("load " + o.line) },
};
t.addEventListener("input", () => state(true));
post({ op: "ready" });
state(false);
</script></body></html>
"""#

/// lasso's HTTP for a file window: the stand-in page, and every request kept.
final class FilePageStub: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var seen: [String] = []
    nonisolated(unsafe) static var page = standInFilePage
    override class func canInit(with request: URLRequest) -> Bool { request.url?.scheme == "http" }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let url = request.url!
        Self.seen.append("\(request.httpMethod ?? "GET") \(url.path)\(url.query.map { "?" + $0 } ?? "")")
        let html = url.path == "/desktop-file.html"
        let resp = HTTPURLResponse(
            url: url, statusCode: html ? 200 : 404, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": html ? "text/html" : "text/plain"])!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data((html ? Self.page : "").utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

@MainActor private func fileClient() -> LassoClient {
    LassoClient(baseURL: URL(string: "http://127.0.0.1:9")!, protocolClasses: [FilePageStub.self])
}

@MainActor private func windows(_ source: FakeFileSource, present: Bool = true) -> FileWindows {
    let f = FileWindows(client: fileClient(), source: source, presence: FixedPresence(present))
    f.enabled = { true }
    f.cache = FileCache(root: FileManager.default.temporaryDirectory.appendingPathComponent("lasso-file-tests-" + UUID().uuidString))
    return f
}

private let pngFixture = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
    .deletingLastPathComponent().appendingPathComponent("Spec/fixtures/media/build-times.png")

// MARK: what a file is

@Test func aFileIsShownByItsTypeAndSize() {
    let cases: [(String, FileKind)] = [
        ("/r/README.md", .markdown), ("/r/notes.markdown", .markdown), ("/r/report.html", .html), ("/r/a.htm", .html),
        ("/r/logo.svg", .svg), ("/r/main.swift", .text), ("/r/app.ts", .text), ("/r/config.json", .text), ("/r/Makefile", .text),
        ("/r/.env", .text), ("/r/Cargo.lock", .text), ("/r/x.toml", .text), ("/r/shot.png", .image), ("/r/a.gif", .image),
        ("/r/clip.mp4", .av), ("/r/take.mp3", .av), ("/r/talk.mov", .av), ("/r/paper.pdf", .pdf), ("/r/plan.docx", .quicklook),
        ("/r/sheet.xlsx", .quicklook), ("/r/deck.key", .quicklook), ("/r/bundle.zip", .quicklook),
    ]
    for (path, kind) in cases {
        let got = FileKind.classify(path: path, size: 100)
        #expect(got == kind, "\(path): \(got)")
    }
    // Over 2 MiB, lasso sends a text file's "too large" line, not the file:
    // read-only in QuickLook, never an editor that could save that line.
    let big = FileKind.textLimit + 1
    for p in ["/r/huge.log", "/r/huge.md", "/r/huge.html", "/r/huge.svg"] { #expect(FileKind.classify(path: p, size: big) == .quicklook, "\(p)") }
    #expect(FileKind.classify(path: "/r/huge.png", size: big) == .image)
    #expect(FileKind.classify(path: "/r/edge.md", size: FileKind.textLimit) == .markdown)
    #expect(!FileKind.playable("/r/a.webm") && !FileKind.playable("/r/a.MKV") && !FileKind.playable("/r/a.ogg") && FileKind.playable("/r/a.mp4"))
    #expect(FileKind.markdown.hasRich && FileKind.html.hasRich && FileKind.svg.hasRich && !FileKind.text.hasRich)
}

/// The goldens TestDesktopContract writes decode into the app's models.
@Test func fileModelsDecodeTheServersContract() throws {
    let dir = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        .appendingPathComponent("Spec/contract")
    let o = try JSONDecoder().decode(FileOpen.self, from: Data(contentsOf: dir.appendingPathComponent("open_file.json")))
    #expect(o.path == "x" && o.host == "x" && o.line == 1 && o.dir == true && o.from == "x" && o.app == "x")
    let s = try JSONDecoder().decode(FileStatPayload.self, from: Data(contentsOf: dir.appendingPathComponent("file_stat.json")))
    #expect(s.host == "x" && s.files.first == FileStatEntry(path: "x", exists: true, dir: true, size: 1, mtime: 1, error: "x"))
    let r = try JSONDecoder().decode(FileStatRequest.self, from: Data(contentsOf: dir.appendingPathComponent("file_stat_request.json")))
    #expect(r == FileStatRequest(host: "x", paths: ["x"]))
    #expect(FileOpen(path: "/a", host: "").hostKey == "local" && FileOpen(path: "/a").who == "an agent")
    // An agent's open_file with app "web" is the web app's alone.
    #expect(FileOpen(path: "/a", app: "web").webOnly && !FileOpen(path: "/a", app: "mac").webOnly && !FileOpen(path: "/a").webOnly)
}

/// lasso's `open-file` on the stream reaches the coordinator's handler.
@MainActor @Test func theStreamsOpenFileReachesTheApp() {
    let sync = FleetSync(client: fileClient(), fleet: FleetModel())
    var got: [FileOpen] = []
    sync.onOpenFile = { got.append($0) }
    sync.handle(SSEEvent(event: "open-file", data: #"{"path":"/home/s/a.md","host":"norm","line":7,"from":"olivia"}"#))
    sync.handle(SSEEvent(event: "open-file", data: "not json"))
    #expect(got == [FileOpen(path: "/home/s/a.md", host: "norm", line: 7, from: "olivia")])
}

// MARK: the page's reach

@Test func theFilePageReachesItsOwnFileAndNothingElse() {
    func path(_ s: String, _ method: String = "GET", host: String = "local") -> String? {
        FilePage.lassoPath(URL(string: s)!, method: method, host: host)
    }
    #expect(path("lasso-file://lasso/desktop-file.html?path=%2Fr%2Fa.md&host=local") == "/desktop-file.html?path=%2Fr%2Fa.md&host=local")
    #expect(path("lasso-file://lasso/assets/desktop-file-B7MImw1p.js") == "/assets/desktop-file-B7MImw1p.js")
    #expect(path("lasso-file://lasso/api/file?path=%2Fr%2Fa.md&host=local") == "/api/file?path=/r/a.md&host=local")
    #expect(path("lasso-file://lasso/api/file?path=%2Fr%2Fimg.png&host=local&v=3") == "/api/file?path=/r/img.png&host=local")
    #expect(path("lasso-file://lasso/api/file?path=/r/a.md") == "/api/file?path=/r/a.md&host=local", "no host: the window's")
    #expect(path("lasso-file://lasso/api/file?path=/r/a.md&host=norm", host: "norm") == "/api/file?path=/r/a.md&host=norm")
    // Go reads a bare + as a space: it goes encoded.
    #expect(path("lasso-file://lasso/api/file?path=/r/c%2B%2B%20notes.md&host=local") == "/api/file?path=/r/c%2B%2B%20notes.md&host=local")
    #expect(path("lasso-file://lasso/api/file-write", "POST") == "/api/file-write")
    for (s, m) in [
        ("lasso-file://lasso/api/file?path=/r/a.md&host=norm", "GET"),  // another machine
        ("lasso-file://lasso/api/file?path=/r/a.md&host=local&download=1", "GET"),
        ("lasso-file://lasso/api/file?path=/r/a.md&host=local&within=/", "GET"),
        ("lasso-file://lasso/api/file?path=/r/a.md&path=/etc/passwd", "GET"),
        ("lasso-file://lasso/api/file?path=r/a.md", "GET"),
        ("lasso-file://lasso/api/file?host=local", "GET"),
        ("lasso-file://lasso/api/file?path=/r/a.md", "POST"),
        ("lasso-file://lasso/api/files?path=/r", "GET"),
        ("lasso-file://lasso/api/file-write", "GET"),
        ("lasso-file://lasso/api/file-delete", "POST"),
        ("lasso-file://lasso/api/ui-state", "GET"),
        ("lasso-file://lasso/api/telemetry", "POST"),
        ("lasso-file://lasso/assets/../api/file", "GET"),
        ("lasso-file://lasso/assets/%2e%2e/api", "GET"),
        ("lasso-file://lasso/assets/.env", "GET"),
        ("lasso-file://other/desktop-file.html", "GET"),
        ("lasso-browser://lasso/desktop-file.html", "GET"),
    ] {
        #expect(path(s, m) == nil, "\(m) \(s)")
    }
}

@Test func aWriteMayNameOnlyTheWindowsFile() {
    func body(_ o: [String: Any]) -> Data { try! JSONSerialization.data(withJSONObject: o) }
    let ok: [String: Any] = ["path": "/r/a.md", "host": "local", "content": "hi", "base_mtime": 1000]
    #expect(FilePage.admitsWrite(body(ok), path: "/r/a.md", host: "local"))
    #expect(FilePage.admitsWrite(body(["path": "/r/a.md", "content": "hi"]), path: "/r/a.md", host: "local"), "no host: lasso's own")
    #expect(!FilePage.admitsWrite(body(["path": "/r/a.md", "content": "hi"]), path: "/r/a.md", host: "norm"))
    #expect(!FilePage.admitsWrite(body(ok.merging(["path": "/r/.bashrc"]) { $1 }), path: "/r/a.md", host: "local"))
    #expect(!FilePage.admitsWrite(body(ok.merging(["host": "norm"]) { $1 }), path: "/r/a.md", host: "local"))
    #expect(!FilePage.admitsWrite(body(ok.merging(["mode": "0777"]) { $1 }), path: "/r/a.md", host: "local"))
    #expect(!FilePage.admitsWrite(body(ok.merging(["content": 1]) { $1 }), path: "/r/a.md", host: "local"))
    #expect(!FilePage.admitsWrite(Data("{".utf8), path: "/r/a.md", host: "local"))
    #expect(!FilePage.admitsWrite(nil, path: "/r/a.md", host: "local"))
}

/// The bridge notes the files the page loaded besides its own (for the
/// watcher), refuses any POST but its own file's write, and takes a body up
/// to a whole file's.
@MainActor @Test func theBridgeNotesWhatItServed() {
    let b = FileBridge(client: fileClient(), host: "", path: "/r/a.md")
    _ = b.lassoPath(URL(string: "lasso-file://lasso/api/file?path=/r/a.md&host=local")!, method: "GET")
    _ = b.lassoPath(URL(string: "lasso-file://lasso/api/file?path=/r/img/one.png&host=local&v=1")!, method: "GET")
    _ = b.lassoPath(URL(string: "lasso-file://lasso/api/file?path=/r/img/one.png&host=local&v=2")!, method: "GET")
    _ = b.lassoPath(URL(string: "lasso-file://lasso/api/file?path=/etc/x&host=norm")!, method: "GET")
    #expect(b.served == ["/r/img/one.png"])
    #expect(!b.admit(path: "/api/file-write", method: "POST", body: Data(#"{"path":"/r/b.md","content":""}"#.utf8)))
    #expect(b.admit(path: "/api/file-write", method: "POST", body: Data(#"{"path":"/r/a.md","host":"local","content":""}"#.utf8)))
    #expect(b.admit(path: "/api/file?path=/r/a.md&host=local", method: "GET", body: nil))
    #expect(b.maxBody > 2 * Int(FileKind.textLimit))
    #expect(LassoPageBridge.passedHeaders.contains("X-Lasso-Mtime") && LassoPageBridge.passedHeaders.contains("X-Lasso-Too-Large"))
    let s = FilePageState(["dirty": true, "mode": "rich", "conflict": true, "saveError": "Changed on disk", "loaded": true, "readOnly": true, "mtime": 5])
    #expect(s.dirty && s.mode == .rich && s.conflict && s.saveError == "Changed on disk" && s.readOnly && s.mtime == 5)
}

/// A body past the cap is refused whole: a cut-short write would be a cut file.
@Test func aBodyPastTheCapIsRefused() {
    let small = InputStream(data: Data(repeating: 1, count: 100))
    #expect(LassoPageBridge.drain(small, limit: 1000)?.count == 100)
    #expect(LassoPageBridge.drain(InputStream(data: Data(repeating: 1, count: 40 << 10)), limit: 1000) == nil)
}

@MainActor @Test func buildFilesAreKeptPerLasso() {
    let a = LassoPageAssets(capacity: 100)
    a.put("http://a/assets/x.js", data: Data(count: 20), type: "text/javascript")
    #expect(a.get("http://a/assets/x.js")?.type == "text/javascript" && a.get("http://b/assets/x.js") == nil)
    for i in 0..<6 { a.put("http://a/assets/\(i).js", data: Data(count: 20), type: "t") }
    #expect(a.get("http://a/assets/x.js") == nil && a.get("http://a/assets/5.js") != nil, "the oldest go first")
    #expect(LassoPageAssets.isAsset("/assets/index-abc.js") && !LassoPageAssets.isAsset("/assets/a/b.js") && !LassoPageAssets.isAsset("/assets/%2e"))
}

// MARK: rendered HTML

@Test func aRenderedPageLoadsOnlyFromItsFolder() {
    let root = "/home/s/report"
    func c(_ s: String, token: String = "tok", root: String = root) -> String? {
        DocPage.confined(URL(string: s)!, token: token, root: root)
    }
    #expect(c("lasso-doc://tok/home/s/report/index.html") == "/home/s/report/index.html")
    #expect(c("lasso-doc://tok/home/s/report/img/chart%20one.png") == "/home/s/report/img/chart one.png")
    #expect(DocPage.confined(DocPage.url(token: "tok", path: "/home/s/report/c++ & d.css"), token: "tok", root: root) == "/home/s/report/c++ & d.css")
    for s in [
        "lasso-doc://other/home/s/report/index.html",  // another window's
        "lasso-doc://tok/home/s/notes.md",  // beside the folder
        "lasso-doc://tok/home/s/reportx/a.css",  // a prefix, not a folder
        "lasso-doc://tok/home/s/report",  // the folder itself
        "lasso-doc://tok/home/s/report/../.ssh/id_ed25519",
        "lasso-doc://tok/home/s/report/%2e%2e/.ssh/id_ed25519",
        "lasso-doc://tok/home/s/report/%2E%2E/x",
        "lasso-doc://tok/home/s/report/a%2f..%2f..%2fx",
        "lasso-doc://tok/home/s/report/a%5cb",
        "lasso-doc://tok/home/s/report/./a.css",
        "lasso-doc://tok/home/s/report//a.css",
        "lasso-doc://tok/home/s/report/.git/config",
        "lasso-doc://tok/home/s/report/img/.hidden.png",
        "lasso-doc://tok/home/s/report/a%00.css",
        "https://tok/home/s/report/a.css",
    ] {
        #expect(c(s) == nil, "\(s)")
    }
    // A page at / sees its own files only.
    #expect(c("lasso-doc://tok/a.html", root: "/") == "/a.html")
    #expect(c("lasso-doc://tok/etc/passwd", root: "/") == nil)
    #expect(DocPage.root(of: "/home/s/report/index.html") == root && DocPage.isHTML("/a/B.HTM") && !DocPage.isHTML("/a/b.js"))
}

@Test func thePolicyGoesInAfterTheDoctype() {
    let meta = #"<meta http-equiv="Content-Security-Policy""#
    #expect(DocPage.inject("<!DOCTYPE html><html><body>x").hasPrefix("<!DOCTYPE html>" + meta))
    #expect(DocPage.inject("\u{feff}<!-- made by an agent -->\n<!doctype html>\n<p>").hasPrefix("\u{feff}<!-- made by an agent -->\n<!doctype html>" + meta))
    #expect(DocPage.inject("<p>no doctype</p>").hasPrefix(meta))
    #expect(DocPage.inject("<p>x</p><!doctype html>").hasPrefix(meta), "a doctype later on is not the doctype")
    for rule in ["default-src 'none'", "connect-src 'self' lasso-doc: https:", "form-action 'none'", "frame-src 'self'", "base-uri 'self'"] {
        #expect(DocPage.csp.contains(rule), "\(rule)")
    }
    #expect(!DocPage.csp.contains("http:"))
    #expect(!DocPage.passedHeaders.contains("Content-Disposition") && DocPage.passedHeaders.contains("Content-Range"))
}

/// The content rules compile, block first, and open only this window's
/// origin, https, data:, blob: and about:.
@MainActor @Test func theContentRulesCompile() async throws {
    let json = DocPage.rules(token: "abc123")
    let list = try #require(try JSONSerialization.jsonObject(with: Data(json.utf8)) as? [[String: [String: String]]])
    #expect(list.first?["action"]?["type"] == "block")
    #expect(Set(list.dropFirst().compactMap { $0["trigger"]?["url-filter"] }) == ["^lasso-doc://abc123/", "^https:", "^data:", "^blob:", "^about:"])
    let id = "lasso-doc-test-" + UUID().uuidString
    let compiled = try await WKContentRuleListStore.default().compileContentRuleList(forIdentifier: id, encodedContentRuleList: json)
    #expect(compiled != nil)
    try? await WKContentRuleListStore.default().removeContentRuleList(forIdentifier: id)
}

// MARK: the watcher

@MainActor @Test func aChangeOnDiskIsHeardOnce() async {
    let src = FakeFileSource()
    src.put("/r/a.md", mtime: 1000)
    src.put("/r/img.png", mtime: 5)
    let w = FakeWatched("/r/a.md")
    w.watchAssets = ["/r/img.png"]
    let watcher = FileWatcher(source: src)
    var t = Date()
    watcher.now = { t }
    watcher.add(w, baseline: src.files["/r/a.md"])
    watcher.stop()
    await watcher.tick()
    #expect(w.changed.isEmpty && w.assets == 0, "the first reading is the baseline")

    src.put("/r/a.md", size: 12, mtime: 2000)
    t += 1
    await watcher.tick()
    t += 1
    await watcher.tick()
    #expect(w.changed.map(\.mtime) == [2000], "once")

    src.put("/r/img.png", mtime: 6)
    t += 1
    await watcher.tick()
    #expect(w.assets == 1)

    src.files["/r/a.md"] = nil
    t += 1
    await watcher.tick()
    t += 1
    await watcher.tick()
    #expect(w.deleted == 1)
    src.put("/r/a.md", mtime: 3000)
    t += 1
    await watcher.tick()
    #expect(w.changed.map(\.mtime) == [2000, 3000], "back again")
}

/// One request per machine per tick, every window on it; a window out of
/// sight is asked about every hidden_s, one in view every visible_s.
@MainActor @Test func theWatcherAsksOncePerMachine() async {
    let src = FakeFileSource()
    let a = FakeWatched("/r/a.md")
    let b = FakeWatched("/r/b.md")
    let c = FakeWatched("/r/c.md", host: "norm")
    let watcher = FileWatcher(source: src)
    var t = Date()
    watcher.now = { t }
    for w in [a, b, c] { watcher.add(w) }
    watcher.stop()
    await watcher.tick()
    #expect(src.stats.count == 2)
    #expect(Set(src.stats.first { $0.host == "local" }?.paths ?? []) == ["/r/a.md", "/r/b.md"])

    a.watchVisible = false
    b.watchVisible = false
    c.watchVisible = false
    src.stats = []
    t += 1
    await watcher.tick()
    #expect(src.stats.isEmpty, "out of sight: not every second")
    t += watcher.hiddenEvery
    await watcher.tick()
    #expect(src.stats.count == 2)
    src.stats = []
    watcher.poke(a)
    await watcher.tick()
    #expect(src.stats.map(\.paths) == [["/r/a.md"]], "expanded: asked about at once")
    watcher.remove(a)
    watcher.remove(b)
    watcher.remove(c)
}

/// More than max_paths on one machine go in slices; a window's own list is
/// capped.
@MainActor @Test func theWatcherKeepsToTheServersCap() async {
    let src = FakeFileSource()
    let w = FakeWatched("/r/a.md")
    w.watchAssets = (0..<100).map { "/r/img/\($0).png" }
    let w2 = FakeWatched("/r/b.md")
    w2.watchAssets = (0..<100).map { "/r/other/\($0).png" }
    let watcher = FileWatcher(source: src)
    watcher.add(w)
    watcher.add(w2)
    watcher.stop()
    await watcher.tick()
    #expect(src.stats.allSatisfy { $0.paths.count <= watcher.maxPaths })
    #expect(src.stats.reduce(0) { $0 + $1.paths.count } == 2 * watcher.maxPaths)
    watcher.remove(w)
    watcher.remove(w2)
}

// MARK: the gate

@MainActor @Test func onlyTheMacSomeoneIsAtOpensTheFile() async {
    let src = FakeFileSource()
    src.put("/r/a.md")
    src.put("/r/dir", dir: true)
    let o = FileOpen(path: "/r/a.md", host: "local", from: "olivia")

    let off = windows(src)
    off.enabled = { false }
    #expect(await off.opened(o) == .off)
    #expect(await windows(src, present: false).opened(o) == .away)
    #expect(await windows(src).opened(FileOpen(path: "/r/dir", dir: true)) == .directory)
    #expect(await windows(src).opened(FileOpen(path: "/r/dir")) == .directory, "lasso says it is a folder")
    #expect(await windows(src).opened(FileOpen(path: "/r/gone.md")) == .missing)
    src.refuse = true
    let old = windows(src)
    #expect(await old.opened(o) == .unsupported, "a lasso without /api/file-stat")
    #expect(old.windows.isEmpty)
    src.refuse = false
    #expect(src.stats.allSatisfy { $0.paths == ["/r/dir"] || $0.paths == ["/r/gone.md"] }, "no request at all before the setting, presence and folder gates")

    let f = windows(src)
    #expect(await f.opened(o) == nil)
    let w = try! #require(f.windows[o.key])
    defer { f.closeAll() }
    #expect(w.isOpen && w.panel.level == .floating)
    #expect(!w.panel.isKeyWindow, "an agent's file never takes the keyboard")
    #expect(w.model.subtitle == "local · opened by olivia" && w.model.kind == .markdown && w.model.mode == .rich)

    // The setting: absent is on.
    let d = UserDefaults(suiteName: "file-window-tests")!
    d.removePersistentDomain(forName: "file-window-tests")
    #expect(SettingsModel.openAgentFilesOn(d))
    d.set(false, forKey: SettingsModel.openAgentFilesKey)
    #expect(!SettingsModel.openAgentFilesOn(d))
}

@MainActor @Test func presenceIsTheMacsOwnWord() {
    let p = SystemPresence(idleLimit: 1e9)
    // The test runs in a logged-in session on the console (minime's Aqua).
    #expect(SystemPresence.idleSeconds >= 0)
    #expect(p.isPresent == SystemPresence.sessionActive)
    #expect(!SystemPresence(idleLimit: -1).isPresent, "idle past the limit")
}

// MARK: the windows

@MainActor @Test func aFileOpenedAgainComesForward() async throws {
    let src = FakeFileSource()
    src.put("/r/a.md")
    src.put("/r/b.md")
    let f = windows(src)
    defer { f.closeAll() }
    await f.opened(FileOpen(path: "/r/a.md", from: "olivia"))
    let w = try #require(f.windows[FileOpen(path: "/r/a.md").key])
    w.window.toggle()
    #expect(w.window.collapsed)
    await f.opened(FileOpen(path: "/r/a.md", line: 12, from: "stub"))
    #expect(f.windows.count == 1 && f.windows[FileOpen(path: "/r/a.md").key] === w)
    #expect(!w.window.collapsed, "expanded again")
    #expect(w.model.mode == .raw && w.model.from == "stub", "a line opens the source")

    // The next one cascades from it.
    await f.opened(FileOpen(path: "/r/b.md"))
    let b = try #require(f.windows[FileOpen(path: "/r/b.md").key])
    let step = Theme.File.window.number("cascade")
    #expect(b.panel.frame.minX == w.panel.frame.minX + step || b.panel.frame.maxX == Screens.visibleFrame(for: b.panel.frame).maxX)
    #expect(b.panel.frame.maxY == w.panel.frame.maxY - step || b.panel.frame.minY == Screens.visibleFrame(for: b.panel.frame).minY)
}

/// Past the cap, the oldest window without unsaved edits gives way; one
/// with edits is never closed for a new one.
@MainActor @Test func theOldestCleanWindowGivesWay() async throws {
    let src = FakeFileSource()
    let f = windows(src)
    defer { f.closeAll() }
    var opened: [FileWindowController] = []
    for i in 0..<f.cap {
        src.put("/r/\(i).txt")
        await f.opened(FileOpen(path: "/r/\(i).txt"))
        opened.append(try #require(f.windows[FileOpen(path: "/r/\(i).txt").key]))
        try await Task.sleep(nanoseconds: 5_000_000)
    }
    var s = FilePageState()
    s.loaded = true
    s.dirty = true
    opened[0].model.take(s)
    let place = opened[1].panel.frame
    src.put("/r/new.txt")
    await f.opened(FileOpen(path: "/r/new.txt"))
    #expect(f.windows.count == f.cap)
    #expect(opened[0].isOpen, "unsaved edits stay")
    #expect(!opened[1].isOpen, "the oldest clean one went")
    #expect(f.windows[FileOpen(path: "/r/new.txt").key]?.panel.frame == place)
}

/// ⌘W with unsaved edits asks first, in the window (no alert): the banner
/// says so; a second ⌘W closes. Without edits, it closes at once.
@MainActor @Test func unsavedEditsAskBeforeClosing() async throws {
    let src = FakeFileSource()
    src.put("/r/a.md")
    src.put("/r/b.md")
    let f = windows(src)
    defer { f.closeAll() }
    await f.opened(FileOpen(path: "/r/a.md"))
    let w = try #require(f.windows[FileOpen(path: "/r/a.md").key])
    var s = FilePageState()
    s.loaded = true
    s.dirty = true
    w.model.take(s)
    w.window.requestClose()
    #expect(w.isOpen && w.model.banner == .confirmClose)
    w.model.keepEditing()
    #expect(w.model.banner == nil)
    w.panel.onCloseKey()
    #expect(w.isOpen && w.model.confirmingClose, "⌘W asks too")
    s.dirty = false
    w.model.take(s)
    #expect(!w.model.confirmingClose, "saved: nothing to ask")
    s.dirty = true
    w.model.take(s)
    w.panel.onCloseKey()
    w.panel.onCloseKey()
    #expect(!w.isOpen && f.windows[FileOpen(path: "/r/a.md").key] == nil, "the second closes")

    await f.opened(FileOpen(path: "/r/b.md"))
    let b = try #require(f.windows[FileOpen(path: "/r/b.md").key])
    b.window.requestClose()
    #expect(!b.isOpen)
}

/// Switching to another lasso closes every file window, edits or not: a
/// save would otherwise land on the new lasso.
@MainActor @Test func aLassoSwitchClosesTheFileWindows() async throws {
    let src = FakeFileSource()
    src.put("/r/a.md")
    let fleet = FleetModel()
    let sync = FleetSync(client: fileClient(), fleet: fleet)
    let app = AppCoordinator(fleet: fleet, sync: sync, fixture: nil)
    app.fileSource = src
    app.filePresence = FixedPresence(true)
    let saved = UserDefaults.standard.object(forKey: SettingsModel.openAgentFilesKey)
    UserDefaults.standard.removeObject(forKey: SettingsModel.openAgentFilesKey)
    let url = UserDefaults.standard.object(forKey: LassoClient.baseURLDefaultsKey)
    defer {
        UserDefaults.standard.set(saved, forKey: SettingsModel.openAgentFilesKey)
        UserDefaults.standard.set(url, forKey: LassoClient.baseURLDefaultsKey)
        sync.stop()
    }
    app.fileOpened(FileOpen(path: "/r/a.md"))
    await until(3) { app.files?.windows.isEmpty == false }
    let w = try #require(app.files?.windows.values.first)
    var s = FilePageState()
    s.loaded = true
    s.dirty = true
    w.model.take(s)
    app.use(URL(string: "http://127.0.0.1:9")!)
    #expect(!w.isOpen && app.files == nil)
}

/// A native kind's file comes down to the cache and shows; a change on disk
/// brings the new version and drops the old; closing empties its folder.
@MainActor @Test func anImageWindowFollowsItsFile() async throws {
    let src = FakeFileSource()
    src.put("/r/chart.png", size: 3060, mtime: 1000)
    src.bytes["/r/chart.png"] = pngFixture
    let f = windows(src)
    await f.opened(FileOpen(path: "/r/chart.png"))
    let w = try #require(f.windows[FileOpen(path: "/r/chart.png").key])
    await until(3) { w.model.phase == .live }
    #expect(w.model.phase == .live && src.downloads == ["/r/chart.png"])
    let folder = f.cache.folder(host: "local", path: "/r/chart.png")
    #expect((try? FileManager.default.contentsOfDirectory(atPath: folder.path)) == ["1000-3060.png"])
    src.put("/r/chart.png", size: 3060, mtime: 2000)
    w.docChanged(src.files["/r/chart.png"]!)
    await until(3) { src.downloads.count == 2 && (try? FileManager.default.contentsOfDirectory(atPath: folder.path)) == ["2000-3060.png"] }
    #expect((try? FileManager.default.contentsOfDirectory(atPath: folder.path)) == ["2000-3060.png"])
    w.docDeleted()
    #expect(w.model.banner == .deleted)
    f.closeAll()
    #expect(!FileManager.default.fileExists(atPath: folder.path))
}

/// A video macOS cannot play says so and offers the default app, and
/// fetches nothing until asked.
@MainActor @Test func aWebmSaysItCannotPlay() async throws {
    let src = FakeFileSource()
    src.put("/r/clip.webm")
    let f = windows(src)
    defer { f.closeAll() }
    await f.opened(FileOpen(path: "/r/clip.webm"))
    let w = try #require(f.windows[FileOpen(path: "/r/clip.webm").key])
    #expect(w.model.phase == .unplayable && src.downloads.isEmpty)
}

/// A file window's page gets its state to the bar, and the bar's and the
/// watcher's calls reach the page.
@MainActor @Test func theWindowAndItsPageTalk() async throws {
    let src = FakeFileSource()
    src.put("/r/a.md")
    let f = windows(src)
    defer { f.closeAll() }
    await f.opened(FileOpen(path: "/r/a.md"))
    let w = try #require(f.windows[FileOpen(path: "/r/a.md").key])
    let web = try #require(w.pageView)
    await until(5) { w.model.page.loaded }
    #expect(w.model.page.loaded && w.model.phase == .live)
    #expect(FilePageStub.seen.contains { $0.hasPrefix("GET /desktop-file.html?path=/r/a.md&host=local&kind=markdown&mode=rich") })
    func log() async -> String { ((try? await web.evaluateJavaScript("__log.join(',')")) as? String) ?? "" }
    w.docChanged(FileStatEntry(path: "/r/a.md", exists: true, size: 2, mtime: 2))
    w.assetsChanged()
    w.model.setMode(.raw)
    w.model.onSave()
    await until(3) { await log() == "changed,assets,mode raw,save" }
    #expect(await log() == "changed,assets,mode raw,save")
    _ = try await web.evaluateJavaScript("document.getElementById('t').value = 'draft'; document.getElementById('t').dispatchEvent(new Event('input'))")
    await until(3) { w.model.dirty }
    #expect(w.model.dirty && w.model.canSave)
    #expect(await w.pageBridge?.draft() == "draft")
}

/// A file's bytes for a download: refused once (a spent token), then served.
final class DownloadStub: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var seen: [String] = []
    nonisolated(unsafe) static var status = 200
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let url = request.url!
        Self.seen.append(url.path + " " + (request.value(forHTTPHeaderField: "Authorization") ?? "-"))
        var code = Self.status
        var body = Data(repeating: 7, count: 300_000)
        if url.path == "/token" {
            body = Data(#"{"access_token":"new","expires_in":900}"#.utf8)
            code = 200
        } else if request.value(forHTTPHeaderField: "Authorization") != "Bearer new" {
            code = 401
            body = Data("spent".utf8)
        } else if code != 200 {
            body = Data("not a file".utf8)
        }
        let resp = HTTPURLResponse(url: url, statusCode: code, httpVersion: "HTTP/1.1", headerFields: ["Content-Length": String(body.count)])!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: body)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

/// A download goes straight to disk, refreshes a refused token once and
/// tries again, and an error status throws and leaves the file as it was.
@Test func aDownloadGoesToDiskAndRetriesOnce() async throws {
    let base = URL(string: "https://lasso.example.com")!
    let grant = OAuthGrant(
        clientID: "c1", tokenEndpoint: URL(string: "https://auth.example.com/token")!, resource: base.absoluteString,
        accessToken: "old", refreshToken: "r1", expiresAt: Date().addingTimeInterval(900))
    let c = LassoClient(baseURL: base, grant: grant, protocolClasses: [DownloadStub.self], persist: { _, _ in })
    let dir = FileManager.default.temporaryDirectory.appendingPathComponent("lasso-download-test-" + UUID().uuidString)
    defer { try? FileManager.default.removeItem(at: dir) }
    let dest = dir.appendingPathComponent("clip.mp4")
    DownloadStub.seen = []
    DownloadStub.status = 200
    try await c.download(c.request(raw: "/api/file?path=/r/clip.mp4&host=local&download=1"), to: dest)
    #expect((try Data(contentsOf: dest)).count == 300_000)
    #expect(DownloadStub.seen == ["/api/file Bearer old", "/token -", "/api/file Bearer new"])
    DownloadStub.status = 404
    await #expect(throws: LassoError.http(404, "not a file")) {
        try await c.download(c.request(raw: "/api/file?path=/r/gone.mp4&host=local&download=1"), to: dest)
    }
    #expect((try Data(contentsOf: dest)).count == 300_000, "the file it had stays")
}
