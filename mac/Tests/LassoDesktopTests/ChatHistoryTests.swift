import AppKit
import Foundation
import SwiftUI
import Testing

@testable import LassoDesktopKit

private func row(_ id: String, _ text: String = "", kind: String = "agent", tool: String? = nil) -> ChatItem {
    ChatItem(kind: tool == nil ? kind : "tool", id: id, text: text.isEmpty ? id : text, tool: tool.map { ChatTool(state: $0) })
}

private func page(
    _ ids: [String], path: String = "/s/a.jsonl", start: Int64? = 1000, more: Bool? = true, rows: [ChatItem]? = nil
) -> ChatPayload {
    ChatPayload(
        pane_id: "w1:p1", agent: "claude", host: "local", path: path, start_offset: start, items: rows ?? ids.map { row($0) },
        more: more)
}

/// Start reading the page above, which there must be.
private func begin(_ h: inout ChatHistory, sourceLocation: SourceLocation = #_sourceLocation) throws -> ChatHistory.OlderRead {
    let read = h.beginOlder()
    return try #require(read, sourceLocation: sourceLocation)
}

/// The first page, then the page above it: its rows go on top, and those
/// already read are not drawn twice.
@Test func anOlderPagePrependsWithoutDuplicates() throws {
    var h = ChatHistory()
    let started = h.take(page(["c", "d", "e"]), key: "local\u{0}w1:p1")
    #expect(started)
    #expect(h.hasMore && h.before == 1000)
    let read = try begin(&h)
    #expect(read.before == 1000 && h.loadingOlder)
    // A second read waits for the first.
    let second = h.beginOlder()
    #expect(second == nil)
    // The page above overlaps the first by a row (a wider window).
    h.endOlder(read, page: page(["a", "b", "c"], start: 400))
    #expect(h.items.map(\.id) == ["a", "b", "c", "d", "e"])
    #expect(!h.loadingOlder && h.olderStart == 400 && h.before == 400 && h.hasMore)
    let top = try begin(&h)
    #expect(top.before == 400)
    h.endOlder(top, page: page(["z"], start: 0, more: false))
    #expect(h.items.map(\.id) == ["z", "a", "b", "c", "d", "e"])
    // The top of the transcript: nothing more to ask for.
    let none = h.beginOlder()
    #expect(!h.hasMore && h.before == nil && none == nil)
    #expect(h.payload?.items.map(\.id) == h.items.map(\.id))
}

/// A poll's page merges into what has been read: older pages stay, rows the
/// page carries again are updated in place, new rows go where the page puts
/// them, and a finished tool never runs again.
@Test func aRefreshMerges() throws {
    var h = ChatHistory()
    h.take(page(["c", "d"], rows: [row("c"), row("t", tool: "running"), row("d")]), key: "k")
    let read = try begin(&h)
    h.endOlder(read, page: page(["a", "b"], start: 0))
    #expect(h.items.map(\.id) == ["a", "b", "c", "t", "d"])
    // The window has moved past a, b and c; t finished; e is new.
    let next = page([], start: 2000, rows: [row("t", tool: "completed"), row("d", "d, longer"), row("e")])
    let merged = !h.take(next, key: "k")
    #expect(merged)
    #expect(h.items.map(\.id) == ["a", "b", "c", "t", "d", "e"])
    #expect(h.items[3].tool?.state == "completed" && h.items[4].text == "d, longer")
    // The cursor stays where the reader got to, not the poll's start.
    #expect(h.olderStart == 0 && !h.hasMore)
    // A new row the page puts before rows already on screen goes there, not
    // at the end (a wider window's start, readLogPage).
    h.take(page([], start: 1500, rows: [row("d2"), row("e"), row("f")]), key: "k")
    #expect(h.items.map(\.id) == ["a", "b", "c", "t", "d", "d2", "e", "f"])
    // An older parse of the tool, still running, does not undo it.
    h.take(page([], rows: [row("t", tool: "running"), row("f")]), key: "k")
    #expect(h.items[3].tool?.state == "completed")
    #expect(h.items.count == 8)
}

/// A new session in the pane (another transcript), or another pane, starts
/// over; so does the card moving on.
@Test func aNewTranscriptOrPaneResets() throws {
    var h = ChatHistory()
    h.take(page(["c"]), key: "k")
    let read = try begin(&h)
    h.endOlder(read, page: page(["a", "b"], start: 10))
    h.revealEarlier()
    let newSession = h.take(page(["x"], path: "/s/b.jsonl"), key: "k")
    #expect(newSession)
    #expect(h.items.map(\.id) == ["x"] && h.olderStart == nil && !h.earlier && h.path == "/s/b.jsonl")
    let otherPane = h.take(page(["y"], path: "/s/b.jsonl"), key: "other")
    #expect(otherPane)
    #expect(h.items.map(\.id) == ["y"] && h.key == "other")
    h.reset()
    #expect(h.payload == nil && h.items.isEmpty && !h.hasMore)
}

/// Whether there is more: the newest page's word until a page above has
/// been read, then whether that one started past zero.
@Test func hasMoreFollowsTheOldestPage() throws {
    var h = ChatHistory()
    let empty = h.beginOlder()
    #expect(!h.hasMore && empty == nil)
    h.take(page(["a"], start: 0, more: false), key: "k")
    #expect(!h.hasMore && h.before == nil)
    // `more` without an offset to ask before: nothing can be asked for.
    h.take(page(["a"], start: 0, more: true), key: "k3")
    let unaddressed = h.beginOlder()
    #expect(h.hasMore && h.before == nil && unaddressed == nil)
    h.take(page(["b"], start: 300, more: true), key: "k4")
    let read = try begin(&h)
    // A page with no offset reads as the top.
    h.endOlder(read, page: page(["a"], start: nil))
    #expect(h.olderStart == 0 && !h.hasMore)
}

/// A page that fails leaves the rows and the cursor as they were, and the
/// next scroll asks again; a page for a conversation since left is dropped.
@Test func aFailedOrStalePageKeepsState() throws {
    var h = ChatHistory()
    h.take(page(["c", "d"]), key: "k")
    let before = h
    let read = try begin(&h)
    h.endOlder(read, page: nil)
    #expect(h == before)
    let again = try begin(&h)
    #expect(again.before == 1000)
    // The card moved on before it landed.
    h.reset()
    h.take(page(["x"]), key: "k")
    h.endOlder(again, page: page(["a"], start: 0))
    #expect(h.items.map(\.id) == ["x"] && h.olderStart == nil)
    // A page from another transcript is not this conversation's.
    let r = try begin(&h)
    h.endOlder(r, page: page(["q"], path: "/s/other.jsonl", start: 0))
    #expect(h.items.map(\.id) == ["x"] && !h.loadingOlder && h.olderStart == nil)
}

private let pane = HostPane(
    host: "local", host_label: "titan", pane_id: "w1:p1", workspace_id: "w1", workspace_label: "Agents",
    tab_id: "w1:t1", tab_label: "stub", cwd: "/", agent: "claude", agent_status: "idle", has_agent: true, focused: false)

/// The card reads the page above through its actions, with `before` the
/// oldest offset read; a failed read changes nothing and is asked again.
@MainActor @Test func theCardReadsThePageAbove() async throws {
    let actions = FixtureCardActions(chats: [pane.key: page(["c", "d"])], canAcknowledge: true)
    let m = CardModel(pane: pane, fleet: FleetModel(), actions: actions)
    await m.load()
    #expect(m.items.map(\.id) == ["c", "d"] && m.history.hasMore)
    await m.readOlder()
    #expect(actions.befores == [1000] && m.items.map(\.id) == ["c", "d"] && !m.history.loadingOlder)
    actions.olderPages[1000] = page(["a", "b"], start: 0)
    await m.readOlder()
    #expect(actions.befores == [1000, 1000] && m.items.map(\.id) == ["a", "b", "c", "d"])
    // The poll keeps them.
    actions.chats[pane.key] = page(["c", "d", "e"], start: 1200)
    await m.load()
    #expect(m.items.map(\.id) == ["a", "b", "c", "d", "e"] && !m.history.hasMore)
}

/// A bot's chat reads its pages the same way.
@MainActor @Test func aBotChatReadsThePageAbove() async throws {
    let bot = BotView(id: 1, host: "local", name: "jessica", runtime: "omp", stopped: false, state: "idle")
    let actions = FixtureBotChatActions(payload: page(["c"]))
    actions.olderPages[1000] = page(["a", "b"], start: 0)
    let m = ChatModel(bot: bot, actions: actions)
    await m.load()
    await m.readOlder()
    #expect(m.chat?.items.map(\.id) == ["a", "b", "c"] && !m.history.hasMore)
}

/// The page above is asked for at lasso's own address for it: the pane's
/// `/api/chat` with `before`, or an omp bot's chat route.
@MainActor @Test func olderPagesAskWithBefore() async throws {
    let client = LassoClient(baseURL: URL(string: "http://127.0.0.1:18090")!, protocolClasses: [PageStub.self])
    let actions = LiveCardActions(client: client, fleet: FleetModel())
    _ = try await actions.chat(pane, before: 4096)
    let req = try #require(PageStub.seen.last)
    #expect(req.url?.path == "/api/chat")
    let q = Dictionary(
        uniqueKeysWithValues: (URLComponents(url: req.url!, resolvingAgainstBaseURL: false)?.queryItems ?? []).map { ($0.name, $0.value ?? "") })
    #expect(q == ["before": "4096", "pane": "w1:p1", "host": "local"])
    _ = try await actions.chat(pane)
    #expect(PageStub.seen.last?.url?.query?.contains("before") == false)
}

final class PageStub: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var seen: [URLRequest] = []

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        Self.seen.append(request)
        let resp = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": "application/json"])!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(#"{"pane_id":"w1:p1","agent":"claude","host":"local","items":[]}"#.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

/// Let the main actor run (SwiftUI's updates, the page's read) until
/// `done`, for at most two seconds.
@MainActor private func wait(until done: () -> Bool) async {
    let end = Date().addingTimeInterval(2)
    while !done(), Date() < end { try? await Task.sleep(nanoseconds: 10_000_000) }
}

@MainActor private func pause(_ seconds: Double) async {
    try? await Task.sleep(nanoseconds: UInt64(seconds * 1e9))
}

/// The scroll view a mounted conversation draws in.
@MainActor private func scrollView(in v: NSView) -> NSScrollView? {
    if let s = v as? NSScrollView { return s }
    for sub in v.subviews { if let s = scrollView(in: sub) { return s } }
    return nil
}

/// The reader's distances from the top and the bottom of the rows.
@MainActor private func place(_ s: NSScrollView) -> (top: CGFloat, bottom: CGFloat, height: CGFloat) {
    let doc = s.documentView!
    let r = s.documentVisibleRect
    let h = doc.frame.height
    let top = doc.isFlipped ? r.minY : h - r.maxY
    return (top, h - r.height - top, h)
}

/// In a window: it opens at the newest row; scrolled to the top it reads the
/// page above, and the rows the reader was looking at stay where they were
/// on screen; a poll's new row then does not move them either.
@MainActor @Test func theReadersPlaceHoldsWhenAPageLandsAbove() async throws {
    let long = String(repeating: "A line of the agent's reply that wraps. ", count: 6)
    let first = page([], rows: (0..<12).map { row("n\($0)", long) })
    let actions = FixtureCardActions(chats: [pane.key: first], canAcknowledge: true)
    actions.olderPages[1000] = page([], start: 0, rows: (0..<10).map { row("o\($0)", long) })
    let m = CardModel(pane: pane, fleet: FleetModel(), actions: actions)
    await m.load()
    let host = NSHostingView(rootView: ConversationBody(model: m, window: true).frame(width: 420, height: 360))
    let w = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 420, height: 360), styleMask: [.borderless], backing: .buffered, defer: false)
    w.contentView = host
    w.orderFront(nil)
    defer { w.orderOut(nil) }
    var s: NSScrollView?
    await wait { s = scrollView(in: host); return s.map { place($0).height > 360 } ?? false }
    let scroll = try #require(s)
    await wait { place(scroll).bottom < 1 }
    #expect(place(scroll).bottom < 1, "opens at the newest row: \(place(scroll))")
    // Up to the top, as the reader would.
    let clip = scroll.contentView
    let topY: CGFloat = scroll.documentView!.isFlipped ? 0 : place(scroll).height - clip.bounds.height
    clip.scroll(to: NSPoint(x: 0, y: topY))
    scroll.reflectScrolledClipView(clip)
    let was = place(scroll)
    await wait { m.items.count == 22 && !m.history.loadingOlder }
    await wait { place(scroll).height > was.height + 100 }
    // Let the last layout settle.
    await pause(0.2)
    let now = place(scroll)
    #expect(actions.befores == [1000] && m.items.first?.id == "o0")
    #expect(now.height > was.height + 100, "the page went in: \(was) → \(now)")
    if abs(now.bottom - was.bottom) >= 1 { Issue.record("the reader moved: \(was) → \(now)") }
    // A poll adds a row at the bottom: the reader, up in the history, stays.
    actions.chats[pane.key] = page([], start: 1200, rows: (0..<13).map { row("n\($0)", long) })
    await m.load()
    await pause(0.2)
    let after = place(scroll)
    if abs(after.top - now.top) >= 1 { Issue.record("a new row moved the reader: \(now) → \(after)") }
}
