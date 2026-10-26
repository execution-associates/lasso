import AppKit
import Foundation
import Testing

@testable import LassoDesktopKit

private func bots(_ json: String) -> [BotView] {
    try! JSONDecoder().decode(BotsPayload.self, from: Data(("{\"bots\":[" + json + "]}").utf8)).bots
}

private func bot(_ name: String, state: String, stopped: Bool = false, extra: String = "") -> String {
    #"{"id":1,"host":"local","name":"\#(name)","runtime":"omp","stopped":\#(stopped),"state":"\#(state)""# + extra + "}"
}

private let fleetBots = bots(
    [
        bot("stub", state: "idle", extra: #","last_text":"Done: posted the weekly digest.\nMore.""#),
        bot("fitty", state: "stopped", stopped: true),
        bot("olivia", state: "working"),
        bot("homie", state: "idle", extra: #","error":"omp exited (code 1)""#),
        bot("jessica", state: "blocked", extra: #","waiting_for":"Book the 3pm with Dana or move it?""#),
        bot("amy", state: "idle", extra: #","pending_ui":[{"id":"q1","method":"confirm","title":"Send it?"}]"#),
    ].joined(separator: ","))

/// The column's order: asking (blocked or holding a request), working,
/// failed, idle, stopped; by name within each. The rail's dot is the most
/// urgent that needs a look, none when all are idle or stopped.
@Test func botsAreOrderedByWhatTheyNeed() {
    #expect(BotStanding.ordered(fleetBots).map(\.name) == ["amy", "jessica", "olivia", "homie", "stub", "fitty"])
    #expect(fleetBots.first { $0.name == "jessica" }?.lastLine == "Book the 3pm with Dana or move it?")
    #expect(fleetBots.first { $0.name == "amy" }?.lastLine == "Send it?")
    #expect(fleetBots.first { $0.name == "homie" }?.lastLine == "omp exited (code 1)")
    #expect(fleetBots.first { $0.name == "stub" }?.lastLine == "Done: posted the weekly digest.")
    #expect(BotStanding.railDot(fleetBots) == .blocked)
    #expect(BotStanding.railDot(fleetBots.filter { $0.name == "olivia" || $0.name == "stub" }) == .working)
    #expect(BotStanding.railDot(fleetBots.filter { $0.name == "stub" || $0.name == "fitty" }) == nil)
}

/// The rail's Bots button sits between the agents and the terminal while
/// lasso has bots, and is hovered and opened like the others.
@MainActor @Test func theRailHasBotsBetweenAgentsAndTerminal() {
    let fleet = FleetModel()
    let m = RailModel(fleet: fleet)
    m.setExpanded(true, animated: false)
    #expect(!m.items.contains(.bots))
    fleet.apply(bots: BotsPayload(bots: fleetBots))
    #expect(m.items == [.inbox, .bots, .terminal, .browser, .files, .newAgent, .settings])
    #expect(RailMetrics.itemFrames(0, bots: true).map(\.0) == [.inbox, .bots, .terminal, .browser, .files, .newAgent, .settings])
    var opened = false
    m.onBots = { opened = true }
    m.activate(.bots)
    #expect(opened)
}

/// Answers like lasso's bot routes, and records what came.
final class BotStub: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var seen: [(String, String, Data)] = []
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
        let url = request.url!
        Self.seen.append((request.httpMethod ?? "GET", url.path, body))
        var status = 200
        var answer = #"{"bot":{}}"#
        switch url.path {
        case "/api/bots/jessica/sessions":
            answer = #"{"sessions":[{"id":"s1","title":"Calendar","updated_at":"2026-10-11T18:00:00Z","current":true}]}"#
        case "/api/bots/jessica/jobs": answer = #"{"jobs":[{"id":1},{"id":2}],"now":"x"}"#
        case "/api/bots/jessica/model": answer = #"{"bot":{},"restart_needed":true}"#
        case "/api/bots/jessica/sessions/resume": status = 409; answer = "jessica is mid-turn"
        default: break
        }
        let resp = HTTPURLResponse(url: url, statusCode: status, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": "application/json"])!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(answer.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

/// The window's actions are lasso's /api/bots/<name> routes: start, stop and
/// restart, the conversations and a switch between them, the model, the jobs.
@MainActor @Test func aBotWindowUsesLassosBotRoutes() async throws {
    BotStub.seen = []
    let client = LassoClient(baseURL: URL(string: "http://127.0.0.1:18090")!, protocolClasses: [BotStub.self])
    let a = LiveBotActions(client: client)
    let b = fleetBots.first { $0.name == "jessica" }!
    for action in ["start", "stop", "restart"] { try await a.lifecycle(b, action) }
    #expect(try await a.sessions(b).map(\.id) == ["s1"])
    try await a.newSession(b)
    #expect(try await a.setModel(b, "deepseek-v4-flash"))
    #expect(try await a.jobCount(b) == 2)
    await #expect(throws: LassoError.self) { try await a.resume(b, session: "s0") }
    let calls = BotStub.seen.map { "\($0.0) \($0.1)" }
    #expect(calls == [
        "POST /api/bots/jessica/start", "POST /api/bots/jessica/stop", "POST /api/bots/jessica/restart",
        "GET /api/bots/jessica/sessions", "POST /api/bots/jessica/sessions/new", "POST /api/bots/jessica/model",
        "GET /api/bots/jessica/jobs", "POST /api/bots/jessica/sessions/resume",
    ])
    let model = try JSONSerialization.jsonObject(with: BotStub.seen[5].2) as? [String: String]
    let resume = try JSONSerialization.jsonObject(with: BotStub.seen[7].2) as? [String: String]
    #expect(model == ["model": "deepseek-v4-flash"] && resume == ["id": "s0"])
}

/// Start and stop go to lasso and say so; a switch lasso refuses mid-turn
/// says why in the window, never silently.
@MainActor @Test func aBotWindowSaysWhatHappened() async throws {
    let fleet = FleetModel()
    fleet.apply(bots: BotsPayload(bots: fleetBots))
    let b = fleetBots.first { $0.name == "fitty" }!
    let actions = FixtureBotActions()
    let m = BotWindowModel(chat: ChatModel(bot: b, actions: FixtureBotChatActions(payload: nil)), actions: actions, fleet: fleet)
    #expect(!m.running && m.standing == .stopped)
    m.startBot()
    try await Task.sleep(nanoseconds: 100_000_000)
    #expect(actions.calls == ["start fitty"] && m.chat.notice == "Starting fitty…")
    m.stopBot()
    try await Task.sleep(nanoseconds: 100_000_000)
    #expect(actions.calls.last == "stop fitty")
    actions.refusal = .http(409, "fitty is mid-turn\n")
    await m.load()
    m.resume(try #require(m.sessions.first { $0.current != true }))
    try await Task.sleep(nanoseconds: 100_000_000)
    #expect(actions.calls.last == "resume fitty s1" && m.chat.notice == "Not now: fitty is mid-turn")
    m.newConversation()
    try await Task.sleep(nanoseconds: 100_000_000)
    #expect(m.chat.notice == "Not now: fitty is mid-turn")
}

/// The Bots column is a hover pop-out like the agents column: the pointer
/// leaving closes it. A row click opens the bot's own window, which stays.
@MainActor @Test func aBotsWindowOutlivesTheColumn() async throws {
    let fleet = FleetModel()
    fleet.apply(bots: BotsPayload(bots: fleetBots))
    let app = AppCoordinator(fleet: fleet, sync: nil, fixture: nil)
    defer { app.closeRailPanels() }
    app.hoverOpen(.bots)
    #expect(app.bots.isOpen && app.railPanelOpen)
    #expect(app.bots.model.bots.first?.name == "amy")
    app.bots.model.open(fleetBots.first { $0.name == "olivia" }!)
    let w = try #require(app.botWindow("olivia"))
    defer {
        w.model.chat.stopPolling()
        w.panel.orderOut(nil)
    }
    var t = Date(timeIntervalSince1970: 1000)
    let away = NSPoint(x: -9000, y: -9000)
    while t <= Date(timeIntervalSince1970: 1000.6) {
        app.pointerTick(away, now: t)
        t = t.addingTimeInterval(0.05)
    }
    #expect(!app.bots.isOpen && w.panel.isVisible)
}
