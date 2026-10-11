import Foundation
import Testing

@testable import LassoDesktopKit

private let contractDir = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
    .deletingLastPathComponent().appendingPathComponent("Spec/contract")

private func golden<T: Decodable>(_ name: String, _ type: T.Type) throws -> T {
    try JSONDecoder().decode(type, from: Data(contentsOf: contractDir.appendingPathComponent("\(name).json")))
}

/// The goldens are written by the Go test TestDesktopContract with every
/// field set, so decoding them proves the models match the server's tags.
@Test func modelsDecodeTheServersContract() throws {
    let pane = try golden("host_pane", HostPane.self)
    #expect(pane.transcript_at == 1 && pane.repo_label == "x" && pane.closed == true && pane.agent_label == "x")
    let bot = try golden("bot_view", BotView.self)
    #expect(bot.name == "x" && bot.runtime == "x" && bot.waiting_for == "x")
    let chat = try golden("chat_payload", ChatPayload.self)
    #expect(chat.items.first?.tool?.ask?.questions.first?.options?.first?.label == "x")
    #expect(chat.items.first?.meta?["x"] == "x")
    #expect(chat.pending_ui?.first?.options == ["x"])
    let ui = try golden("ui_state", UIStateSlice.self)
    #expect(ui.pinned_agents == ["x"] && ui.dismissed_cards?["x"] == 1)
    let hosts = try golden("hosts", HostsPayload.self)
    #expect(hosts.hosts.first?.compatible == true && hosts.local.hostname == "x")
}

@Test func sseParserFramesEvents() {
    var p = SSEParser()
    var got: [SSEEvent] = []
    for line in [
        ": comment", "event: active", "data: {\"panes_rev\":3}", "", "event: ping", "data: {}", "",
        "data: one", "data: two", "", "", "event: bot-chat\r", "data: {\"bot\":\"a\"}\r", "\r",
    ] {
        if let e = p.feed(line: line) { got.append(e) }
    }
    #expect(
        got == [
            SSEEvent(event: "active", data: "{\"panes_rev\":3}"), SSEEvent(event: "ping", data: "{}"),
            SSEEvent(event: "message", data: "one\ntwo"), SSEEvent(event: "bot-chat", data: "{\"bot\":\"a\"}"),
        ])
}

@Test func activeFrameDecodesTheRevs() throws {
    let f = try JSONDecoder().decode(
        ActiveFrame.self, from: Data(#"{"pane_id":"w1:p1","panes_rev":7,"ui_state_rev":2,"herdr_up":true}"#.utf8))
    #expect(f.panes_rev == 7 && f.ui_state_rev == 2 && f.herdr_up == true)
}

@Test func basicAuthGoesInTheHeaderOnly() {
    let c = LassoClient(baseURL: URL(string: "http://127.0.0.1:18090")!, basicAuth: "u:p")
    let r = c.request("/api/all-panes", query: ["host": "local"])
    #expect(r.value(forHTTPHeaderField: "Authorization") == "Basic dTpw")
    #expect(r.url?.absoluteString == "http://127.0.0.1:18090/api/all-panes?host=local")
}

/// Settings' picker moves every holder of the one client to another lasso,
/// credentials and all.
@Test func retargetMovesTheSharedClient() {
    let c = LassoClient(baseURL: URL(string: "http://127.0.0.1:18090")!, basicAuth: "u:p")
    c.retarget(URL(string: "http://127.0.0.1:18191")!, remember: false)
    let r = c.request("/api/version")
    #expect(r.url?.absoluteString == "http://127.0.0.1:18191/api/version")
    #expect(r.value(forHTTPHeaderField: "Authorization") == "Basic dTpw")
    #expect(c.probe(URL(string: "http://127.0.0.1:18192")!).request("/api/version").value(forHTTPHeaderField: "Authorization") == "Basic dTpw")
}

/// A dev lasso says so, and where it bound behind the tunnel.
@Test func lassoChoicesNameDevLassos() throws {
    let v = try JSONDecoder().decode(
        VersionInfo.self,
        from: Data(#"{"lasso_protocol":1,"lasso_version":"6.4.0-dev","compatible":true,"dev":true,"listen":"127.0.0.1:8191"}"#.utf8))
    let c = LassoChoice(url: "http://127.0.0.1:18191", version: v)
    #expect(c.title == "Dev lasso 6.4.0-dev")
    #expect(c.detail == "127.0.0.1:18191 → 127.0.0.1:8191")
    #expect(LassoChoice(url: "http://127.0.0.1:18090", version: nil).title == "Not answering")
}

/// Agents are named by their herdr label, else their tab, never the
/// workspace: one workspace ("Agents") holds several of them.
@Test func agentsAreNamedByLabelThenTab() {
    var p = HostPane(
        host: "local", host_label: "titan", pane_id: "w1:p1", workspace_id: "w1", workspace_label: "Agents",
        tab_id: "w1:t1", tab_label: "stub-tab", cwd: "/", agent: "claude", agent_status: "idle", has_agent: true,
        focused: false)
    #expect(p.displayName == "stub-tab")
    #expect(p.group == "Agents")
    p.agent_label = "stub"
    #expect(p.displayName == "stub")
}
