import AppKit
import Foundation
import Testing

@testable import LassoDesktopKit

// The agents column's hover (ColumnHover, PLAN 16/19): one poll maps the
// pointer to a row through AppKit's scroll view, and that row is the one
// highlight and what the preview follows. Ticked by hand with a pointer and
// a clock the test sets, so every run sees the same thing.

private func panes(_ n: Int) -> [HostPane] {
    (1...n).map { i in
        HostPane(
            host: "local", host_label: "titan", pane_id: "w1:p\(i)", workspace_id: "w1", workspace_label: "Agents",
            tab_id: "w1:t\(i)", tab_label: "agent\(i)", cwd: "/", agent: "claude", agent_status: "idle", has_agent: true,
            focused: false)
    }
}

/// A column with `n` agents, open, its rows laid out, its timer off.
@MainActor private func column(_ n: Int = 30) async -> (ListController, FleetModel, [HostPane]) {
    let fleet = FleetModel()
    let ps = panes(n)
    fleet.apply(panes: AllPanes(panes: ps))
    let c = ListController(model: ListModel(fleet: fleet, actions: FixtureFleetActions()))
    c.hover.timerEnabled = false
    c.open(takeKey: false)
    await settle { c.model.rowFrames.count >= n }
    return (c, fleet, ps)
}

/// Let SwiftUI and AppKit catch up for a moment.
@MainActor private func idle(_ seconds: Double = 0.15) {
    RunLoop.main.run(until: Date().addingTimeInterval(seconds))
}

/// The panel's pixels as drawn now.
@MainActor private func snapshot(_ c: ListController) -> NSBitmapImageRep? {
    guard let host = c.panel.contentView, let rep = host.bitmapImageRepForCachingDisplay(in: host.bounds) else { return nil }
    host.cacheDisplay(in: host.bounds, to: rep)
    return rep
}

/// Where SwiftUI paints `key`'s row (screen y, bottom to top): the band of
/// pixel rows that change when the row is lit, the ground truth the hover
/// map must agree with. It is left lit.
@MainActor private func paintedRow(_ c: ListController, _ key: String) -> ClosedRange<CGFloat>? {
    c.model.keyboardLit = false
    c.model.hovered = nil
    idle()
    guard let off = snapshot(c) else { return nil }
    c.model.selected = key
    c.model.keyboardLit = true
    idle()
    guard let on = snapshot(c), on.pixelsHigh == off.pixelsHigh, let host = c.panel.contentView else { return nil }
    let scale = CGFloat(on.pixelsHigh) / host.bounds.height
    // A column right of the names and left of the rows' close buttons.
    let x = Int(host.bounds.width * 0.6 * scale)
    func level(_ r: NSBitmapImageRep, _ y: Int) -> CGFloat {
        guard let col = r.colorAt(x: x, y: y)?.usingColorSpace(.sRGB) else { return 0 }
        return col.redComponent + col.greenComponent + col.blueComponent
    }
    let changed = (0..<on.pixelsHigh).filter { abs(level(on, $0) - level(off, $0)) > 0.01 }
    guard let top = changed.first, let bottom = changed.last else { return nil }
    // Pixel rows run down from the top of the panel.
    let f = c.panel.frame
    return (f.maxY - CGFloat(bottom + 1) / scale)...(f.maxY - CGFloat(top) / scale)
}

private extension ClosedRange where Bound == CGFloat {
    var mid: CGFloat { (lowerBound + upperBound) / 2 }
}

/// The screen centre of a row as the hover map places it.
@MainActor private func centre(_ c: ListController, _ key: String) -> NSPoint? {
    c.model.rowFrames[key].map { c.hover.screenRect($0).center }
}

/// The pointer on a row, wherever the column is scrolled to, is that row:
/// checked against where SwiftUI paints it, before and after a scroll. The
/// map reads the scroll from AppKit as it happens: right after a scroll,
/// with no turn of the run loop for SwiftUI's geometry to catch up, it
/// already says what it says once everything has settled.
@MainActor @Test func thePointerMapsToTheRowDrawnUnderIt() async throws {
    let (c, _, ps) = await column()
    defer { c.close() }
    var at = NSPoint.zero
    c.hover.pointer = { at }
    for i in [0, 4, 12] {
        let band = try #require(paintedRow(c, ps[i].key), "agent\(i + 1) is painted")
        #expect(band.upperBound - band.lowerBound > 20, "\(band)")
        at = NSPoint(x: c.panel.frame.midX, y: band.mid)
        #expect(c.hover.rowUnderPointer()?.id == ps[i].key, "agent\(i + 1)")
        // The row's frame comes back to the screen where it was painted.
        let back = try #require(c.hover.rowUnderPointer()?.rect)
        #expect(abs(back.midY - band.mid) < 2, "\(back) vs \(band)")
    }
    // Scroll by an odd amount (not a whole row) and read at once.
    func scrollView(_ v: NSView) -> NSScrollView? {
        if let s = v as? NSScrollView { return s }
        for sub in v.subviews { if let s = scrollView(sub) { return s } }
        return nil
    }
    let content = try #require(c.panel.contentView)
    let s = try #require(scrollView(content))
    let probe = NSPoint(x: c.panel.frame.midX, y: c.panel.frame.midY)
    at = probe
    let before = c.hover.rowUnderPointer()?.id
    s.contentView.scroll(to: NSPoint(x: 0, y: 317))
    s.reflectScrolledClipView(s.contentView)
    let atOnce = c.hover.rowUnderPointer()?.id
    #expect(atOnce != before)
    idle()
    #expect(c.hover.rowUnderPointer()?.id == atOnce)
    // And it is the row painted there.
    let band = try #require(paintedRow(c, ps[19].key), "agent20 is painted after the scroll")
    at = NSPoint(x: c.panel.frame.midX, y: band.mid)
    #expect(c.hover.rowUnderPointer()?.id == ps[19].key)
    // Under the top edge, where rows have scrolled out of sight, nothing
    // scrolled away is hovered.
    at = NSPoint(x: c.panel.frame.midX, y: c.panel.frame.maxY - 1)
    let r = c.hover.rowUnderPointer()?.id
    #expect(r == nil || (c.model.rowFrames[r!]?.minY ?? 0) >= 300, "\(r ?? "")")
}

/// A rest previews once: a fleet poll redrawing the rows mid-rest neither
/// restarts it nor moves the highlight, and the highlight is the row the
/// preview opens on.
@MainActor @Test func aFleetPollMidRestChangesNothing() async throws {
    let (c, fleet, ps) = await column(8)
    defer { c.close() }
    let at = try #require(centre(c, ps[4].key))
    var t = Date(timeIntervalSince1970: 1000)
    var shown: [String] = []
    var showing: String?
    c.hover.pointer = { at }
    c.hover.now = { t }
    c.hover.showing = { showing }
    c.hover.onShow = { k, _ in shown.append(k); showing = k }
    let rest = Theme.List.hover.number("open_ms") / 1000
    c.hover.tick()
    t += rest / 3
    c.hover.tick()
    // A poll: another agent's state changes, every row is drawn again.
    var next = ps
    next[0].agent_status = "working"
    fleet.apply(panes: AllPanes(panes: next))
    idle()
    t += rest / 3
    c.hover.tick()
    #expect(shown.isEmpty && c.model.hovered == ps[4].key)
    t += rest / 3 + 0.003
    c.hover.tick()
    #expect(shown == [ps[4].key] && c.model.hovered == ps[4].key)
}

/// A pointer moving fast across rows lights one row at a time, the one it
/// is on, and previews none until it rests; then the row it rests on.
@MainActor @Test func fastMovesLightOneRowAndPreviewWhereTheyRest() async throws {
    let (c, _, ps) = await column(12)
    defer { c.close() }
    let centers = try (0..<10).map { try #require(centre(c, ps[$0].key)) }
    var t = Date(timeIntervalSince1970: 1000)
    var at = centers[0]
    var shown: [String] = []
    var showing: String?
    c.hover.pointer = { at }
    c.hover.now = { t }
    c.hover.showing = { showing }
    c.hover.onShow = { k, _ in shown.append(k); showing = k }
    func lit() -> [String] { ps.map(\.key).filter { c.model.isLit($0, key: $0) } }
    for i in 0..<10 {
        at = centers[i]
        c.hover.tick()
        #expect(lit() == [ps[i].key], "row \(i + 1)")
        t += 0.05
    }
    #expect(shown.isEmpty)
    for _ in 0..<3 {
        c.hover.tick()
        t += 0.05
    }
    #expect(shown == [ps[9].key] && lit() == [ps[9].key])
}

/// A click opens the row's window (selecting it) and puts its preview away:
/// one row stays lit (the pointer's), and the preview stays away until the
/// pointer has left the row. The keyboard takes the highlight with a key and
/// gives it back when the pointer moves.
@MainActor @Test func oneRowIsLitClickOrKey() async throws {
    let (c, _, ps) = await column(6)
    defer { c.close() }
    let a = try #require(centre(c, ps[1].key))
    let b = try #require(centre(c, ps[3].key))
    var t = Date(timeIntervalSince1970: 1000)
    var at = a
    var shown: [String] = []
    var showing: String?
    c.hover.pointer = { at }
    c.hover.now = { t }
    c.hover.showing = { showing }
    c.hover.onShow = { k, _ in shown.append(k); showing = k }
    func lit() -> [String] { ps.map(\.key).filter { c.model.isLit($0, key: $0) } }
    for _ in 0..<4 {
        c.hover.tick()
        t += 0.05
    }
    #expect(shown == [ps[1].key])
    // The click: the window opens, the preview goes.
    c.model.open(ps[1])
    showing = nil
    for _ in 0..<6 {
        c.hover.tick()
        t += 0.05
    }
    #expect(shown == [ps[1].key] && lit() == [ps[1].key])
    // To another row: that one is lit, not the selected one too.
    at = b
    for _ in 0..<4 {
        c.hover.tick()
        t += 0.05
    }
    #expect(lit() == [ps[3].key] && shown == [ps[1].key, ps[3].key])
    // ↓: the keyboard's row is lit, the pointer's is not, while it is still.
    c.model.move(1)
    c.hover.keyboardMoved()
    c.hover.tick()
    #expect(lit() == [ps[2].key] && c.model.keyboardLit)
    // The pointer moves: its row has the highlight again.
    at = NSPoint(x: b.x + 3, y: b.y)
    c.hover.tick()
    #expect(lit() == [ps[3].key] && !c.model.keyboardLit)
}
