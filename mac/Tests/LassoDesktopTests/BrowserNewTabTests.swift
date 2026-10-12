import AppKit
import Foundation
import Network
import Testing
import WebKit

@testable import LassoDesktopKit

// The Browser window's page strip "+", end to end on this Mac: lasso's real
// built page (src/web/dist's desktop-browser.html, when it is beside the
// package), the app's window, scheme handler, page script and native socket,
// against a stand-in Chromium that answers CDP as one does.

/// A browser's CDP endpoint on loopback: one page open, `Target.createTarget`
/// makes another and announces it as discovery does, and every command is
/// answered. It keeps the methods it was sent.
final class StandInCDP: @unchecked Sendable {
    private let listener: NWListener
    private let queue = DispatchQueue(label: "stand-in-cdp")
    private let lock = NSLock()
    private var got: [String] = []
    private var made = 0
    private var refusal: String?
    /// From now on `Target.createTarget` fails with `message`, as Chromium answers.
    func refuseCreate(_ message: String) { lock.withLock { refusal = message } }
    var methods: [String] { lock.withLock { got } }
    var port: UInt16 { listener.port?.rawValue ?? 0 }

    init() throws {
        let ws = NWProtocolWebSocket.Options()
        ws.autoReplyPing = true
        let params = NWParameters.tcp
        params.defaultProtocolStack.applicationProtocols.insert(ws, at: 0)
        params.requiredLocalEndpoint = .hostPort(host: "127.0.0.1", port: .any)
        listener = try NWListener(using: params)
        listener.newConnectionHandler = { [weak self] c in
            guard let self else { return }
            c.start(queue: self.queue)
            self.receive(c)
        }
        listener.start(queue: queue)
    }

    private func page(_ id: String, _ url: String) -> [String: Any] {
        ["targetId": id, "type": "page", "url": url, "title": url, "attached": false]
    }

    private func send(_ c: NWConnection, _ obj: [String: Any]) {
        guard let d = try? JSONSerialization.data(withJSONObject: obj) else { return }
        let meta = NWProtocolWebSocket.Metadata(opcode: .text)
        c.send(
            content: d, contentContext: NWConnection.ContentContext(identifier: "cdp", metadata: [meta]), isComplete: true,
            completion: .idempotent)
    }

    private func receive(_ c: NWConnection) {
        c.receiveMessage { [weak self] data, _, _, error in
            guard let self, error == nil else { return }
            if let data, let msg = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any],
               let id = msg["id"] as? Int, let method = msg["method"] as? String
            {
                self.lock.withLock { self.got.append(method) }
                var reply: [String: Any] = ["id": id, "result": [String: Any]()]
                if let s = msg["sessionId"] { reply["sessionId"] = s }
                switch method {
                case "Target.setDiscoverTargets":
                    self.send(c, ["method": "Target.targetCreated", "params": ["targetInfo": self.page("P0", "https://example.com/")]])
                case "Target.createTarget" where self.lock.withLock({ self.refusal }) != nil:
                    reply = ["id": id, "error": ["code": -32000, "message": self.lock.withLock { self.refusal! }]]
                case "Target.createTarget":
                    let n = self.lock.withLock { self.made += 1; return self.made }
                    let id = "P\(n)"
                    reply["result"] = ["targetId": id]
                    self.send(c, reply)
                    let url = (msg["params"] as? [String: Any])?["url"] as? String ?? "about:blank"
                    self.send(c, ["method": "Target.targetCreated", "params": ["targetInfo": self.page(id, url)]])
                    self.receive(c)
                    return
                case "Target.attachToTarget":
                    let t = (msg["params"] as? [String: Any])?["targetId"] as? String ?? ""
                    reply["result"] = ["sessionId": "S-" + t]
                default: break
                }
                self.send(c, reply)
            }
            self.receive(c)
        }
    }

    func stop() { listener.cancel() }
}

/// The built web app beside the package (`../src/web/dist`), if it is there.
private func webDist() -> URL? {
    let pkg = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
    let dist = pkg.deletingLastPathComponent().appendingPathComponent("src/web/dist")
    return FileManager.default.fileExists(atPath: dist.appendingPathComponent("desktop-browser.html").path) ? dist : nil
}

/// lasso's HTTP for the real page: its files from the build, the status.
final class DistStub: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var dist: URL?
    nonisolated(unsafe) static var seen: [String] = []
    override class func canInit(with request: URLRequest) -> Bool { request.url?.scheme == "http" }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let url = request.url!
        Self.seen.append("\(request.httpMethod ?? "GET") \(url.path)")
        var status = 200
        var type = "text/plain"
        var body = Data()
        if url.path == "/api/browser" {
            type = "application/json"
            body = Data(
                #"{"available":true,"profiles":[{"id":"default","name":"Default","default":true,"running":true,"pages":[],"ws_path":"/cdp"}]}"#
                    .utf8)
        } else if let dist = Self.dist, let d = try? Data(contentsOf: dist.appendingPathComponent(String(url.path.dropFirst()))) {
            body = d
            type =
                url.pathExtension == "html" ? "text/html" : url.pathExtension == "js" ? "text/javascript"
                : url.pathExtension == "css" ? "text/css" : "application/octet-stream"
        } else {
            status = 404
        }
        let resp = HTTPURLResponse(url: url, statusCode: status, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": type])!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: body)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

/// A real click at `point` (the web view's own, top-left origin), through
/// the window server, and that the page's web view is what it lands on (a
/// view over the page that takes the press keeps the release from WebKit,
/// and the page never sees a click).
@MainActor private func click(_ web: WKWebView, at point: NSPoint) async {
    let hit = hitView(web, at: point)
    #expect(hit.map { $0 === web || $0.isDescendant(of: web) } == true, "the click lands on \(hit.map { String(describing: type(of: $0)) } ?? "nothing")")
    await realClick(web, at: point)
}

/// The centre of the page's element `selector`, in the web view.
@MainActor private func centre(_ web: WKWebView, _ selector: String) async -> NSPoint? {
    let js = "(() => { const e = document.querySelector(\(BrowserBridge.literal(selector))); if (!e) return null; const b = e.getBoundingClientRect(); return [b.x + b.width / 2, b.y + b.height / 2] })()"
    guard let r = (try? await web.evaluateJavaScript(js)) as? [Double], r.count == 2 else { return nil }
    return NSPoint(x: r[0], y: r[1])
}

/// The strip's "+", clicked for real, asks the browser for a new page with
/// the window not yet holding the keyboard (the click that makes it key is
/// the one that acts), the page it makes shows in the strip, selected, and a
/// browser that refuses says so in the page. lasso's real page, so it needs
/// the build beside the package, and real clicks; without either this checks
/// nothing.
@MainActor @Test func theStripsPlusOpensANewPage() async throws {
    guard let dist = webDist(), canPostClicks() else { return }
    let cdp = try StandInCDP()
    defer { cdp.stop() }
    await until(2) { cdp.port != 0 }
    DistStub.dist = dist
    DistStub.seen = []
    UserDefaults.standard.set("default", forKey: BrowserWindowModel.storedKey)
    defer { UserDefaults.standard.removeObject(forKey: BrowserWindowModel.storedKey) }
    let client = LassoClient(baseURL: URL(string: "http://127.0.0.1:\(cdp.port)")!, protocolClasses: [DistStub.self])
    let c = BrowserWindowController(source: FixtureBrowserSource(BrowserStatusPayload(profiles: [])), client: client)
    c.open()
    defer { c.close() }
    let w = c.hostWindow
    w.setFrame(NSRect(x: 500, y: 400, width: 900, height: 600), display: true)
    let web = try #require(c.pageView)
    // A test process's windows never hear they are on screen (it is not a
    // launched app), and the page connects only while it is visible.
    web.configuration.userContentController.addUserScript(
        WKUserScript(
            source:
                "Object.defineProperty(document, 'visibilityState', { get: () => 'visible' }); Object.defineProperty(document, 'hidden', { get: () => false });",
            injectionTime: .atDocumentStart, forMainFrameOnly: true))
    web.reload()
    func js(_ s: String) async -> String { ((try? await web.evaluateJavaScript(s)) as? String) ?? "" }
    let tabs =
        "[...document.querySelectorAll('[role=tab]')].map(t => (t.getAttribute('aria-selected') === 'true' ? '*' : '') + t.title).join(' ')"
    await until(10) { await js(tabs) == "*https://example.com/" }
    #expect(await js(tabs) == "*https://example.com/", "requests: \(DistStub.seen), cdp: \(cdp.methods)")

    if w.isKeyWindow { w.resignKey() }
    let plus = try #require(await centre(web, "[aria-label='new tab']"))
    await click(web, at: plus)
    await until(5) { await js(tabs) == "https://example.com/ *about:blank" }
    #expect(cdp.methods.contains("Target.createTarget"), "cdp: \(cdp.methods)")
    #expect(await js(tabs) == "https://example.com/ *about:blank")
    #expect(await js("document.querySelector('.text-destructive')?.textContent ?? ''") == "")

    // A browser that will not open one: the page says why.
    cdp.refuseCreate("Not allowed")
    await click(web, at: try #require(await centre(web, "[aria-label='new tab']")))
    await until(5) { await js("document.querySelector('.text-destructive')?.textContent ?? ''") != "" }
    #expect(await js("document.querySelector('.text-destructive')?.textContent ?? ''") == "Target.createTarget: Not allowed")
}

/// A stand-in page with one button that logs its clicks.
private let buttonPage = #"""
<html><body style="margin:0"><button id="b" style="position:absolute;left:40px;top:40px;width:80px;height:40px">+</button><script>
window.__log = [];
document.getElementById("b").addEventListener("click", () => __log.push("click"));
webkit.messageHandlers.lassoPage.postMessage({ op: "ready" });
</script></body></html>
"""#

final class ButtonPageStub: URLProtocol, @unchecked Sendable {
    override class func canInit(with request: URLRequest) -> Bool { request.url?.scheme == "http" }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let url = request.url!
        let html = url.path == "/desktop-browser.html"
        // As lasso's page does, it takes a moment: the window's status
        // ("Opening …") is up over the page first, and hidden once it is live.
        if html { Thread.sleep(forTimeInterval: 0.5) }
        let resp = HTTPURLResponse(
            url: url, statusCode: html ? 200 : 404, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": html ? "text/html" : "text/plain"])!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data((html ? buttonPage : "").utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

/// A lasso page's controls act on a real click, the one that brings their
/// window the keyboard included: WebKit's own rule spends that click on
/// making the window key, and the status that covered the page while it
/// opened, hidden since, must not take it.
@MainActor @Test func aPagesFirstClickActs() async throws {
    guard canPostClicks() else { return }
    let client = LassoClient(baseURL: URL(string: "http://127.0.0.1:9")!, protocolClasses: [ButtonPageStub.self])
    let c = BrowserWindowController(source: FixtureBrowserSource(BrowserStatusPayload(profiles: [])), client: client)
    c.open()
    defer { c.close() }
    let w = c.hostWindow
    w.setFrame(NSRect(x: 600, y: 450, width: 700, height: 500), display: true)
    let web = try #require(c.pageView)
    await until(10) {
        guard c.model.phase == .live else { return false }
        return await centre(web, "#b") != nil
    }
    if w.isKeyWindow { w.resignKey() }
    #expect(web.acceptsFirstMouse(for: nil))
    await click(web, at: try #require(await centre(web, "#b")))
    await until(3) { ((try? await web.evaluateJavaScript("__log.join(' ')")) as? String) == "click" }
    #expect(((try? await web.evaluateJavaScript("__log.join(' ')")) as? String) == "click")
}
