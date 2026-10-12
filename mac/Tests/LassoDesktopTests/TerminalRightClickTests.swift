import AppKit
import Foundation
import Network
import Testing
import WebKit

@testable import LassoDesktopKit

// The herdr window's right-clicks, end to end on this Mac: the app's own
// window (controller, scheme handler, page script, native socket) with a
// stand-in for ttyd's page and a stand-in ttyd, clicked through AppKit.

/// A page that reports the mouse as xterm does with mouse tracking on: the
/// press on the terminal's element, the release anywhere, as SGR reports
/// over the page's socket. It logs what it saw.
private let standInPage = #"""
<html><body style="margin:0"><div id="t" style="width:100vw;height:100vh"></div><script>
window.__log = [];
const ws = new WebSocket("ws://lasso/terminal/local/ws", ["tty"]);
ws.binaryType = "arraybuffer";
ws.onopen = () => { window.__open = true; };
const report = (e, up) => ws.send(new TextEncoder().encode(
  "0\x1b[<" + e.button + ";" + (Math.floor(e.clientX / 10) + 1) + ";" + (Math.floor(e.clientY / 20) + 1) + (up ? "m" : "M")));
document.getElementById("t").addEventListener("mousedown", (e) => { __log.push("mousedown:" + e.button); e.preventDefault(); report(e, false); });
document.addEventListener("mouseup", (e) => { __log.push("mouseup:" + e.button); report(e, true); });
document.addEventListener("contextmenu", (e) => __log.push("contextmenu:" + (e.defaultPrevented ? "cancelled" : "open")));
</script></body></html>
"""#

/// lasso's HTTP for the window: the document and its token.
final class RightClickStub: URLProtocol, @unchecked Sendable {
    override class func canInit(with request: URLRequest) -> Bool { request.url?.scheme == "http" }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let url = request.url!
        let page = url.path.hasPrefix("/terminal/") && !url.path.hasSuffix("/token")
        let status = url.path.hasPrefix("/terminal/") ? 200 : 404
        let resp = HTTPURLResponse(
            url: url, statusCode: status, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": page ? "text/html" : "application/json"])!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data((page ? standInPage : #"{"token":""}"#).utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

@MainActor private final class StubSource: HerdrTerminalSource {
    func attach(_ host: String) async throws -> String { "local" }
    func documentQuery(_ slug: String) async throws -> String { "rendererType=canvas" }
    func focus(_ pane: HostPane) async throws -> Bool { false }
    func claim(_ host: String, client: String) async throws -> String { client }
}

/// A stand-in ttyd (or CDP endpoint) on loopback: accepts a page's socket and keeps what
/// the page sends.
final class StandInSocketServer: @unchecked Sendable {
    private let listener: NWListener
    private let queue = DispatchQueue(label: "stand-in-ttyd")
    private let lock = NSLock()
    private var got: [String] = []
    var frames: [String] { lock.withLock { got } }
    var port: UInt16 { listener.port?.rawValue ?? 0 }

    init() throws {
        let ws = NWProtocolWebSocket.Options()
        ws.autoReplyPing = true
        // ttyd's "tty" when the page asks for it; a CDP client asks for none.
        ws.setClientRequestHandler(DispatchQueue(label: "stand-in-ttyd-upgrade")) { offered, _ in
            NWProtocolWebSocket.Response(status: .accept, subprotocol: offered.first)
        }
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

    private func receive(_ c: NWConnection) {
        c.receiveMessage { [weak self] data, _, _, error in
            guard let self, error == nil else { return }
            if let data {
                let s = String(decoding: data, as: UTF8.self).replacingOccurrences(of: "\u{1b}", with: "ESC")
                self.lock.withLock { self.got.append(s) }
            }
            self.receive(c)
        }
    }

    func clear() { lock.withLock { got.removeAll() } }
    func stop() { listener.cancel() }
}

@MainActor func until(_ seconds: Double, _ done: () async -> Bool) async {
    let end = Date().addingTimeInterval(seconds)
    while !(await done()), Date() < end { try? await Task.sleep(nanoseconds: 30_000_000) }
}

/// A right-click in the herdr window reaches the page as the right button's
/// press and release (so xterm reports both to herdr), WebKit's menu stays
/// shut, and a Control-click is the same right-click.
@MainActor @Test func rightClicksInTheHerdrWindowReachHerdr() async throws {
    let ttyd = try StandInSocketServer()
    defer { ttyd.stop() }
    await until(2) { ttyd.port != 0 }
    let client = LassoClient(baseURL: URL(string: "http://127.0.0.1:\(ttyd.port)")!, protocolClasses: [RightClickStub.self])
    let c = HerdrTerminalController(source: StubSource(), client: client, fleet: FleetModel())
    c.open()
    defer { c.close() }
    let w = c.hostWindow
    w.setFrame(NSRect(x: 200, y: 200, width: 640, height: 400), display: true)
    let web = try #require(c.pageView)
    await until(10) {
        guard c.model.phase == .live else { return false }
        return ((try? await web.evaluateJavaScript("window.__open === true")) as? Bool) == true
    }
    #expect(c.model.phase == .live)

    var menus = 0
    let watch = NotificationCenter.default.addObserver(forName: NSMenu.didBeginTrackingNotification, object: nil, queue: nil) { n in
        nonisolated(unsafe) let m = n.object as? NSMenu
        MainActor.assumeIsolated { menus += 1 }
        m?.cancelTracking()
    }
    defer { NotificationCenter.default.removeObserver(watch) }
    // The page's (205, 105): cell 21;6.
    let at = web.convert(NSPoint(x: 205, y: web.isFlipped ? 105 : web.bounds.height - 105), to: nil)
    func click(_ down: NSEvent.EventType, _ up: NSEvent.EventType, _ flags: NSEvent.ModifierFlags = []) -> (NSEvent, NSEvent) {
        let make = { (t: NSEvent.EventType) in
            NSEvent.mouseEvent(
                with: t, location: at, modifierFlags: flags, timestamp: ProcessInfo.processInfo.systemUptime,
                windowNumber: w.windowNumber, context: nil, eventNumber: 0, clickCount: 1, pressure: 1)!
        }
        return (make(down), make(up))
    }
    func pageLog() async -> String {
        (try? await web.evaluateJavaScript("(() => { const l = __log.join(' '); __log = []; return l })()")) as? String ?? "?"
    }

    // A right-click, as AppKit hands it to the window.
    let (rd, ru) = click(.rightMouseDown, .rightMouseUp)
    w.sendEvent(rd)
    w.sendEvent(ru)
    await until(3) { ttyd.frames.count >= 2 }
    #expect(ttyd.frames == ["0ESC[<2;21;6M", "0ESC[<2;21;6m"])
    #expect(await pageLog() == "mousedown:2 contextmenu:cancelled mouseup:2")
    #expect(menus == 0)

    // A Control-click: the left button's press and release become the right's.
    ttyd.clear()
    let (cd, cu) = click(.leftMouseDown, .leftMouseUp, .control)
    web.mouseDown(with: cd)
    web.mouseUp(with: cu)
    await until(3) { ttyd.frames.count >= 2 }
    #expect(ttyd.frames == ["0ESC[<2;21;6M", "0ESC[<2;21;6m"])
    #expect(await pageLog() == "mousedown:2 contextmenu:cancelled mouseup:2")
    #expect(menus == 0)

    // A plain click is the left button's, untouched.
    ttyd.clear()
    let (ld, lu) = click(.leftMouseDown, .leftMouseUp)
    web.mouseDown(with: ld)
    web.mouseUp(with: lu)
    await until(3) { ttyd.frames.count >= 2 }
    #expect(ttyd.frames == ["0ESC[<0;21;6M", "0ESC[<0;21;6m"])
    #expect(await pageLog() == "mousedown:0 mouseup:0")
}
