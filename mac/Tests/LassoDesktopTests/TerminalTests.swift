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

/// The rail's terminal sits between the plus and the ellipsis, and opens on
/// a click.
@MainActor @Test func railTerminalSitsBetweenNewAgentAndSettings() {
    let m = RailModel(fleet: FleetModel())
    m.setExpanded(true, animated: false)
    #expect(m.items == [.inbox, .newAgent, .terminal, .settings])
    var opened = false
    m.onTerminal = { opened = true }
    m.activate(.terminal)
    #expect(opened)
    let frames = RailMetrics.itemFrames(0).map(\.0)
    #expect(frames == [.inbox, .newAgent, .terminal, .settings])
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
