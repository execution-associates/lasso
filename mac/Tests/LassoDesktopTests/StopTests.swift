import AppKit
import Foundation
import Testing

@testable import LassoDesktopKit

/// Records what Stop sends, and answers like lasso's /api/chat/stop.
final class StopStub: URLProtocol, @unchecked Sendable {
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
        let resp = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": "application/json"])!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(#"{"outcome":"sent"}"#.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

private func pane(_ status: String, host: String = "minime", id: String = "w2:p3") -> HostPane {
    HostPane(
        host: host, host_label: host, pane_id: id, workspace_id: "w2", workspace_label: "Agents",
        tab_id: "w2:t1", tab_label: "stub", cwd: "/", agent: "claude", agent_status: status, has_agent: true, focused: false)
}

private func chat(running: Bool) throws -> ChatPayload {
    try JSONDecoder().decode(
        ChatPayload.self,
        from: Data(#"{"pane_id":"w2:p3","agent":"claude","host":"minime","running":\#(running),"items":[{"kind":"user","id":"u1","text":"go"}]}"#.utf8))
}

/// Stop posts {pane_id} to /api/chat/stop on the pane's own host.
@MainActor @Test func stopPostsToThePanesHost() async throws {
    let client = LassoClient(baseURL: URL(string: "http://127.0.0.1:18090")!, protocolClasses: [StopStub.self])
    let actions = LiveCardActions(client: client, fleet: FleetModel())
    let o = try await actions.stop(pane("working"))
    #expect(o.outcome == "sent")
    let (req, body) = try #require(StopStub.seen.last)
    #expect(req.httpMethod == "POST" && req.url?.path == "/api/chat/stop")
    #expect(URLComponents(url: req.url!, resolvingAgainstBaseURL: false)?.queryItems == [URLQueryItem(name: "host", value: "minime")])
    #expect(try JSONDecoder().decode([String: String].self, from: body) == ["pane_id": "w2:p3"])
}

/// Stop takes Send's place while the agent works with nothing typed, ⌘.
/// is live then, and the button stays down until a poll shows it idle.
@MainActor @Test func stopFollowsThePhase() async throws {
    let p = pane("working")
    let actions = FixtureCardActions(chats: [p.key: try chat(running: true)], canAcknowledge: true)
    let m = CardModel(pane: p, fleet: FleetModel(), actions: actions)
    #expect(!m.showsStop)
    await m.load()
    #expect(m.isWorking && m.showsStop && m.activeBindings.contains(Keys.stop))
    #expect(m.stopLabel == "Stop \(p.displayName)")
    m.composer = "and also"
    #expect(!m.showsStop && m.isWorking)
    m.composer = ""
    m.stop()
    #expect(m.stopping)
    try await Task.sleep(nanoseconds: 100_000_000)
    #expect(actions.calls == ["stop"])
    // Still working on that poll: the button stays down.
    #expect(m.stopping)
    m.stop()
    #expect(actions.calls == ["stop"])
    // The turn ended.
    actions.chats[p.key] = try chat(running: false)
    let idle = CardModel(pane: pane("idle"), fleet: FleetModel(), actions: actions)
    await idle.load()
    #expect(!idle.isWorking && !idle.showsStop && !idle.activeBindings.contains(Keys.stop))
}

@MainActor @Test func aRefusedStopSaysSoAndComesBack() async throws {
    let p = pane("working")
    let actions = FixtureCardActions(chats: [p.key: try chat(running: true)], canAcknowledge: true)
    actions.stopOutcome = Outcome(outcome: "refused", detail: "the agent is not working")
    let m = CardModel(pane: p, fleet: FleetModel(), actions: actions)
    await m.load()
    m.stop()
    try await Task.sleep(nanoseconds: 100_000_000)
    #expect(!m.stopping && m.notice == "Not stopped: the agent is not working")
}

/// ⌘. is Stop even while the composer has the keys (a field would take it as esc).
@MainActor @Test func commandPeriodIsStop() {
    let e = NSEvent.keyEvent(
        with: .keyDown, location: .zero, modifierFlags: [.command], timestamp: 0, windowNumber: 0, context: nil,
        characters: ".", charactersIgnoringModifiers: ".", isARepeat: false, keyCode: 47)!
    #expect(Keys.stop.matches(e))
    #expect(CardKeys.route(e, editing: true, pointerKeyed: false) == .stop)
    let plain = NSEvent.keyEvent(
        with: .keyDown, location: .zero, modifierFlags: [], timestamp: 0, windowNumber: 0, context: nil,
        characters: ".", charactersIgnoringModifiers: ".", isARepeat: false, keyCode: 47)!
    #expect(!Keys.stop.matches(plain))
}
