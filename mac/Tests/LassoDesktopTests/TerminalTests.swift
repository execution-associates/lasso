import AppKit
import Foundation
import JavaScriptCore
import Testing

@testable import LassoDesktopKit

/// Answers like lasso for the herdr window's routes, and records what came.
final class TerminalStub: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var seen: [(URLRequest, Data)] = []

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        var body = request.httpBody ?? Data()
        if body.isEmpty, let s = request.httpBodyStream {
            s.open()
            var buf = [UInt8](repeating: 0, count: 4096)
            while s.hasBytesAvailable {
                let n = s.read(&buf, maxLength: buf.count)
                if n <= 0 { break }
                body.append(buf, count: n)
            }
            s.close()
        }
        Self.seen.append((request, body))
        let url = request.url!
        var status = 200
        var headers = ["Content-Type": "application/json"]
        var answer = "{}"
        switch url.path {
        case "/api/host": answer = #"{"active":"norm","version":"0.9.3","protocol":22}"#
        case "/api/active": answer = #"{"host":"norm","host_slug":"norm-1a2b3c4d","term_owner":""}"#
        case "/api/focus": answer = #"{"ok":true,"reattach":true}"#
        case "/api/term-claim": answer = #"{"host":"norm","term_owner":"lasso-desktop-term-x"}"#
        case _ where url.absoluteString.hasSuffix("/terminal/norm-1a2b3c4d/"):
            // URL.path drops the trailing slash; lasso's route needs it.
            status = 302
            headers = ["Location": "/terminal/norm-1a2b3c4d/?rendererType=canvas&theme=%7B%22background%22%3A%22%231b0a2c%22%7D"]
            answer = ""
        default: status = 404
        }
        let resp = HTTPURLResponse(url: url, statusCode: status, httpVersion: "HTTP/1.1", headerFields: headers)!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(answer.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}

    static func last(_ path: String) -> (URLRequest, Data)? { seen.last { $0.0.url?.path == path } }
}

private func pane(host: String = "norm", id: String = "w2:p3") -> HostPane {
    HostPane(
        host: host, host_label: host, pane_id: id, workspace_id: "w2", workspace_label: "Agents",
        tab_id: "w2:t1", tab_label: "stub", cwd: "/", agent: "claude", agent_status: "idle", has_agent: true, focused: false)
}

private func query(_ r: URLRequest) -> [URLQueryItem]? { URLComponents(url: r.url!, resolvingAgainstBaseURL: false)?.queryItems }

/// Opening a host's herdr attaches it the way a web tab does, then reads its
/// slug from lasso, and the document's palette off lasso's redirect without
/// following it.
@MainActor @Test func herdrWindowAttachesAndReadsTheDocumentsQuery() async throws {
    let client = LassoClient(baseURL: URL(string: "http://127.0.0.1:18090")!, protocolClasses: [TerminalStub.self])
    let source = LiveHerdrTerminalSource(client: client)
    let slug = try await source.attach("norm")
    #expect(slug == "norm-1a2b3c4d")
    let (attach, body) = try #require(TerminalStub.last("/api/host"))
    #expect(attach.httpMethod == "POST")
    #expect(try JSONDecoder().decode([String: String].self, from: body) == ["host": "norm"])
    let (active, _) = try #require(TerminalStub.last("/api/active"))
    #expect(query(active) == [URLQueryItem(name: "host", value: "norm")])

    let q = try await source.documentQuery(slug)
    #expect(q.hasPrefix("rendererType=canvas&theme=%7B"))
    let page = HerdrTerminal.pageURL(slug: slug, query: q)
    #expect(page.absoluteString == "lasso-terminal://lasso/terminal/norm-1a2b3c4d/?" + q)
}

/// Showing an agent focuses its pane on the pane's own host, asking lasso to
/// reveal it, and passes on whether the herdr client must start again.
@MainActor @Test func showInHerdrFocusesThePaneOnItsHost() async throws {
    let client = LassoClient(baseURL: URL(string: "http://127.0.0.1:18090")!, protocolClasses: [TerminalStub.self])
    let source = LiveHerdrTerminalSource(client: client)
    #expect(try await source.focus(pane()))
    let (req, body) = try #require(TerminalStub.last("/api/focus"))
    #expect(req.httpMethod == "POST" && query(req) == [URLQueryItem(name: "host", value: "norm")])
    let sent = try JSONSerialization.jsonObject(with: body) as? [String: Any]
    #expect(sent?["pane_id"] as? String == "w2:p3" && sent?["reveal"] as? Bool == true)

    #expect(try await source.claim("norm", client: "lasso-desktop-term-x") == "lasso-desktop-term-x")
    let (claim, claimBody) = try #require(TerminalStub.last("/api/term-claim"))
    #expect(query(claim) == [URLQueryItem(name: "host", value: "norm")])
    #expect(try JSONDecoder().decode([String: String].self, from: claimBody) == ["client_id": "lasso-desktop-term-x"])
}

/// The page reaches only its own terminal: the document and the file beside
/// it (ttyd's token), never another route on lasso with this Mac's sign-in.
@Test func thePageReachesOnlyItsTerminal() {
    let slug = "norm-1a2b3c4d"
    func path(_ s: String) -> String? { HerdrTerminal.lassoPath(URL(string: s)!, slug: slug) }
    #expect(path("lasso-terminal://lasso/terminal/norm-1a2b3c4d/?theme=x") == "/terminal/norm-1a2b3c4d/?theme=x")
    #expect(path("lasso-terminal://lasso/terminal/norm-1a2b3c4d/token") == "/terminal/norm-1a2b3c4d/token")
    for refused in [
        "lasso-terminal://lasso/api/file?path=/etc/passwd",
        "lasso-terminal://lasso/terminal/local/",
        "lasso-terminal://lasso/terminal/norm-1a2b3c4d/../../api/file",
        "lasso-terminal://lasso/terminal/norm-1a2b3c4d/%2e%2e/api",
        "lasso-terminal://lasso/terminal/norm-1a2b3c4d/a/b",
        "lasso-terminal://elsewhere/terminal/norm-1a2b3c4d/",
        "https://lasso/terminal/norm-1a2b3c4d/",
    ] {
        #expect(path(refused) == nil, "\(refused)")
    }
}

/// The page's websocket goes to the terminal's own `ws` on lasso, over TLS
/// when lasso is reached over https; anything else is refused.
@Test func thePagesSocketIsTheTerminals() {
    let slug = "local"
    let local = HerdrTerminal.socketURL(
        base: URL(string: "http://127.0.0.1:18090")!, path: "/terminal/local/ws", query: "rendererType=canvas", slug: slug)
    #expect(local?.absoluteString == "ws://127.0.0.1:18090/terminal/local/ws?rendererType=canvas")
    let remote = HerdrTerminal.socketURL(base: URL(string: "https://lasso.example.com")!, path: "/terminal/local/ws", query: "", slug: slug)
    #expect(remote?.absoluteString == "wss://lasso.example.com/terminal/local/ws")
    #expect(HerdrTerminal.socketURL(base: URL(string: "https://lasso.example.com")!, path: "/terminal/norm/ws", query: nil, slug: slug) == nil)
    #expect(HerdrTerminal.socketURL(base: URL(string: "https://lasso.example.com")!, path: "/api/events", query: nil, slug: slug) == nil)
}

/// The terminal's size follows the client a human last used: free or ours,
/// this window may resize; a claim goes at once when it is not ours and is
/// renewed at most every live.term_claim's renew.
@Test func termClaimFollowsTheHuman() {
    var c = TermClaim(me: "me")
    #expect(c.may)
    let t0 = Date(timeIntervalSince1970: 1000)
    let renew = Double(Theme.Live.termClaim.number("renew"))
    let first = c.due(now: t0, renew: renew)
    c.owner = "me"
    let soon = c.due(now: t0.addingTimeInterval(1), renew: renew)
    let later = c.due(now: t0.addingTimeInterval(renew + 1), renew: renew)
    #expect(first && !soon && later)
    c.owner = "a-browser-tab"
    #expect(!c.may)
    let taken = c.due(now: t0.addingTimeInterval(renew + 2), renew: renew)
    #expect(taken)
    #expect(HerdrTerminalController.owner(#"{"host":"local","term_owner":"me"}"#) == "me")
    #expect(HerdrTerminalController.owner(#"{"host":"local"}"#) == nil)
}

/// The page script at least parses: it runs before ttyd's own, and a syntax
/// error would leave the page with the browser's WebSocket, which lasso
/// refuses from another origin.
@Test func thePageScriptParses() {
    let ctx = JSContext()!
    var problem: String?
    ctx.exceptionHandler = { _, e in problem = e?.toString() }
    ctx.evaluateScript("new Function(\(String(data: try! JSONEncoder().encode(TerminalBridge.script), encoding: .utf8)!))")
    #expect(problem == nil, "\(problem ?? "")")
}

/// The rail's terminal sits above the browser and the plus (under the Bots
/// button when there is one), and opens on a click.
@MainActor @Test func railTerminalSitsAboveBrowserAndNew() {
    let m = RailModel(fleet: FleetModel())
    m.setExpanded(true, animated: false)
    #expect(m.items == [.inbox, .terminal, .browser, .files, .newAgent, .settings])
    var opened = false
    m.onTerminal = { opened = true }
    m.activate(.terminal)
    #expect(opened)
    let frames = RailMetrics.itemFrames(0).map(\.0)
    #expect(frames == [.inbox, .terminal, .browser, .files, .newAgent, .settings])
}

/// T, and the header's button, show the agent in herdr: live only where
/// there is a herdr window to show it in.
@MainActor @Test func showInHerdrIsTWhereThereIsAWindow() {
    let p = pane()
    let fleet = FleetModel()
    fleet.apply(panes: AllPanes(panes: [p]))
    let m = CardModel(pane: p, fleet: fleet, actions: FixtureCardActions(chats: [:], canAcknowledge: true))
    #expect(!m.canShowInHerdr && !m.activeBindings.contains(Keys.herdr))
    var shown: HostPane?
    m.onShowInHerdr = { shown = $0 }
    #expect(m.canShowInHerdr && m.activeBindings.contains(Keys.herdr))
    m.showInHerdr()
    #expect(shown?.key == p.key)
    let t = NSEvent.keyEvent(
        with: .keyDown, location: .zero, modifierFlags: [], timestamp: 0, windowNumber: 0, context: nil,
        characters: "t", charactersIgnoringModifiers: "t", isARepeat: false, keyCode: 17)!
    #expect(CardKeys.route(t, editing: false, pointerKeyed: false) == .herdr)
    #expect(CardKeys.route(t, editing: true, pointerKeyed: false) == nil)
}

// MARK: text size (⌘= ⌘- ⌘0)

/// A step is the token's, within its bounds; Actual Size is lasso's own 14
/// (ttyd's launch), which the web app's terminal text also starts from.
@Test func terminalZoomStepsWithinItsBounds() {
    let s = TerminalZoom.Scale()
    #expect(s == TerminalZoom.Scale(min: 8, max: 32, step: 1, standard: 14))
    #expect(s.apply(.bigger, to: 14) == 15)
    #expect(s.apply(.smaller, to: 14) == 13)
    #expect(s.apply(.bigger, to: 32) == 32)
    #expect(s.apply(.smaller, to: 8) == 8)
    #expect(s.apply(.actual, to: 27) == 14)
    #expect(s.apply(.bigger, to: 40) == 32)
    let wide = TerminalZoom.Scale(min: 10, max: 20, step: 4, standard: 12)
    #expect(wide.apply(.bigger, to: 18) == 20)
    #expect(wide.apply(.smaller, to: 12) == 10)
}

/// The size is kept across windows and launches; lasso's own is kept as no
/// choice at all, and a stored size out of bounds comes back in them.
@Test func terminalZoomIsRemembered() throws {
    let name = "lasso-zoom-test-\(UUID().uuidString)"
    let d = try #require(UserDefaults(suiteName: name))
    defer { d.removePersistentDomain(forName: name) }
    #expect(TerminalZoom.stored(d) == 14)
    TerminalZoom.store(17, d)
    #expect(TerminalZoom.stored(d) == 17)
    TerminalZoom.store(14, d)
    #expect(d.object(forKey: TerminalZoom.defaultsKey) == nil)
    d.set(99, forKey: TerminalZoom.defaultsKey)
    #expect(TerminalZoom.stored(d) == 32)
    d.set("big", forKey: TerminalZoom.defaultsKey)
    #expect(TerminalZoom.stored(d) == 14)
}

/// ⌘= and ⌘+ (⌘⇧= on a US layout) are bigger, ⌘- smaller, ⌘0 lasso's own;
/// Option or Control make them the terminal's, and a bare key is typing.
@Test func terminalZoomKeys() {
    func key(_ c: String, _ f: NSEvent.ModifierFlags) -> TerminalZoom.Step? {
        let e = NSEvent.keyEvent(
            with: .keyDown, location: .zero, modifierFlags: f, timestamp: 0, windowNumber: 0, context: nil,
            characters: c, charactersIgnoringModifiers: c, isARepeat: false, keyCode: 0)!
        return TerminalZoom.step(e)
    }
    #expect(key("=", .command) == .bigger)
    #expect(key("+", [.command, .shift]) == .bigger)
    #expect(key("+", [.command, .numericPad]) == .bigger)
    #expect(key("-", .command) == .smaller)
    #expect(key("0", .command) == .actual)
    #expect(key("=", [.command, .capsLock]) == .bigger)
    for (c, f) in [
        ("=", NSEvent.ModifierFlags()), ("-", []), ("0", .shift), ("=", [.command, .option]), ("-", [.command, .control]),
        ("_", [.command, .shift]), ("w", .command), ("1", .command),
    ] as [(String, NSEvent.ModifierFlags)] {
        #expect(key(c, f) == nil, "\(c) \(f)")
    }
}

/// The page's query carries the size for ttyd, beside lasso's palette,
/// replacing the one a page already had.
@Test func terminalZoomRidesInThePagesQuery() {
    let palette = "rendererType=canvas&theme=%7B%22background%22%3A%22%231b0a2c%22%7D"
    #expect(TerminalZoom.query(palette, fontSize: 16) == palette + "&fontSize=16")
    #expect(TerminalZoom.query(palette + "&fontSize=16", fontSize: 18) == palette + "&fontSize=18")
    #expect(TerminalZoom.query("", fontSize: 12) == "fontSize=12")
    let url = HerdrTerminal.pageURL(slug: "local", query: TerminalZoom.query(palette, fontSize: 16))
    #expect(HerdrTerminal.lassoPath(url, slug: "local") == "/terminal/local/?" + palette + "&fontSize=16")
}

/// The page script in a bare JavaScript context, with just enough of a
/// browser (and a stand-in for ttyd's xterm as window.term) to run it.
/// Listeners it adds on the window are in `listeners`.
private func pageContext(_ problem: @escaping (String) -> Void) -> JSContext {
    let ctx = JSContext()!
    ctx.exceptionHandler = { _, e in problem(e?.toString() ?? "?") }
    ctx.evaluateScript(#"""
    var window = globalThis;
    globalThis.posted = [];
    window.webkit = { messageHandlers: { lassoTerm: { postMessage: (m) => posted.push(m) } } };
    globalThis.EventTarget = class { dispatchEvent() {} };
    globalThis.Event = class { constructor(t) { this.type = t; } };
    globalThis.CloseEvent = class extends Event { constructor(t, i) { super(t); Object.assign(this, i); } };
    globalThis.MessageEvent = class extends Event { constructor(t, i) { super(t); this.data = i.data; } };
    globalThis.Blob = class {};
    globalThis.DOMException = class extends Error {};
    globalThis.TextEncoder = class { encode(s) { return new Uint8Array([...s].map((c) => c.charCodeAt(0))); } };
    globalThis.TextDecoder = class { decode(u) { return String.fromCharCode(...u); } };
    const B64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    globalThis.atob = (s) => {
      let o = "", buf = 0, bits = 0;
      for (const ch of s.replace(/=+$/, "")) {
        buf = (buf << 6) | B64.indexOf(ch);
        bits += 6;
        if (bits >= 8) { bits -= 8; o += String.fromCharCode((buf >> bits) & 255); }
      }
      return o;
    };
    globalThis.URL = class {
      constructor(h) {
        const i = h.indexOf("?");
        this.base = i < 0 ? h : h.slice(0, i);
        this.search = i < 0 ? "" : h.slice(i);
        this.pathname = (this.base.match(/^[a-z-]+:\/\/[^\/]*(\/.*)?$/) || [])[1] || "/";
        const p = new Map(this.search.slice(1).split("&").filter(Boolean).map((kv) => kv.split("=")));
        this.p = p;
        this.searchParams = { get: (k) => (p.has(k) ? p.get(k) : null), set: (k, v) => p.set(k, v) };
      }
      get href() {
        const q = [...this.p].map(([k, v]) => k + "=" + v).join("&");
        return this.base + (q ? "?" + q : "");
      }
    };
    globalThis.location = { href: "lasso-terminal://lasso/terminal/local/?theme=x&fontSize=14" };
    globalThis.history = { state: null, replaceState: (s, t, u) => { location.href = u; } };
    globalThis.listeners = {};
    globalThis.addEventListener = (t, f) => (listeners[t] = listeners[t] || []).push(f);
    globalThis.MouseEvent = class extends Event { constructor(t, i) { super(t); Object.assign(this, i); } };
    globalThis.dispatchEvent = () => {};
    globalThis.setTimeout = () => {};
    globalThis.requestAnimationFrame = (f) => f();
    globalThis.fits = 0;
    window.term = {
      options: { fontSize: 14 },
      fit: () => { fits++; },
      parser: { registerOscHandler: () => {} },
      attachCustomKeyEventHandler: () => {},
    };
    """#)
    ctx.evaluateScript(TerminalBridge.script)
    return ctx
}

/// In the page: a zoom sets xterm's fontSize and refits through ttyd's fit,
/// keeps the size in the page's query, and sets it again when ttyd's
/// preferences (sent on every connect) put lasso's back.
@Test func thePageAppliesTheTextSize() throws {
    var problem: String?
    let ctx = pageContext { problem = $0 }
    ctx.evaluateScript("__lassoTerm.zoom(16)")
    #expect(problem == nil, "\(problem ?? "")")
    #expect(ctx.evaluateScript("term.options.fontSize").toInt32() == 16)
    #expect(ctx.evaluateScript("fits").toInt32() == 2)
    #expect(ctx.evaluateScript("location.href").toString() == "lasso-terminal://lasso/terminal/local/?theme=x&fontSize=16")
    // The same size again changes nothing, and refits nothing.
    ctx.evaluateScript("__lassoTerm.zoom(16)")
    #expect(ctx.evaluateScript("fits").toInt32() == 2)

    // ttyd connects again and applies lasso's options: the size comes back.
    ctx.evaluateScript(#"""
    const ws = new WebSocket("lasso-terminal://lasso/terminal/local/ws");
    ws.binaryType = "arraybuffer";
    ws.onmessage = (e) => { if (new Uint8Array(e.data)[0] === 0x32) term.options.fontSize = 14; };
    __lassoTerm.opened(1, "tty");
    """#)
    let prefs = Data("2{\"fontSize\":14}".utf8).base64EncodedString()
    ctx.evaluateScript("__lassoTerm.message(1, '\(prefs)', false)")
    #expect(problem == nil, "\(problem ?? "")")
    #expect(ctx.evaluateScript("term.options.fontSize").toInt32() == 16)
    // Output is only output.
    ctx.evaluateScript("term.options.fontSize = 13")
    ctx.evaluateScript("__lassoTerm.message(1, '\(Data("0hi".utf8).base64EncodedString())', false)")
    #expect(ctx.evaluateScript("term.options.fontSize").toInt32() == 13)
    #expect(ctx.evaluateScript("posted[0].op").toString() == "open")
}

/// Right-clicks are herdr's: WebKit's menu is cancelled while the press and
/// release go on to xterm untouched, and a Control-click reaches xterm as
/// the right button's press and release.
@Test func rightClicksAreHerdrs() {
    var problem: String?
    let ctx = pageContext { problem = $0 }
    ctx.evaluateScript(#"""
    globalThis.fire = (type, init) => {
      const e = Object.assign({ isTrusted: true, prevented: false, stopped: false }, init);
      e.preventDefault = () => { e.prevented = true; };
      e.stopImmediatePropagation = () => { e.stopped = true; };
      for (const f of listeners[type] || []) f(e);
      return e;
    };
    globalThis.sent = [];
    globalThis.target = { dispatchEvent: (e) => sent.push(e) };
    """#)
    #expect(ctx.evaluateScript("fire('contextmenu', { button: 2, target }).prevented").toBool())
    let right = ctx.evaluateScript("const r = fire('mousedown', { button: 2, target }); [r.prevented, r.stopped]").toArray() as? [Bool]
    #expect(right == [false, false])
    #expect(ctx.evaluateScript("sent.length").toInt32() == 0)

    let ctrl = ctx.evaluateScript("const c = fire('mousedown', { button: 0, ctrlKey: true, clientX: 7, target }); [c.prevented, c.stopped]").toArray() as? [Bool]
    #expect(ctrl == [true, true])
    ctx.evaluateScript("fire('mouseup', { button: 0, ctrlKey: true, clientX: 7, target })")
    #expect(ctx.evaluateScript("sent.map((e) => e.type + ':' + e.button + ':' + e.buttons + ':' + e.clientX).join(' ')").toString()
        == "mousedown:2:2:7 mouseup:2:0:7")
    // The page's own synthetic events, and plain clicks, pass untouched.
    #expect(!ctx.evaluateScript("fire('mousedown', { button: 0, target }).stopped").toBool())
    #expect(!ctx.evaluateScript("fire('mousedown', { button: 0, ctrlKey: true, isTrusted: false, target }).stopped").toBool())
    #expect(problem == nil, "\(problem ?? "")")
}
