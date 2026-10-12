import AppKit
import SwiftUI

/// A column of rows the pointer hovers (the agents column, the Bots column):
/// the rows' frames, the row the pointer is on, and the keyboard's row.
@MainActor
protocol HoverColumn: AnyObject {
    /// Each row's frame in the column's scrolled content (`ColumnRows.space`),
    /// by row id: where it is laid out, which scrolling does not change.
    var rowFrames: [String: CGRect] { get set }
    /// The row id under the pointer: the one hover highlight.
    var hovered: String? { get set }
    /// The keyboard moved the selection last (J/K, ↑/↓): it, not the
    /// pointer, has the highlight until the pointer moves.
    var keyboardLit: Bool { get set }
    /// Called when `rowFrames` changes (a layout landed): the hover looks
    /// again at once.
    var rowFramesChanged: () -> Void { get set }
}

/// The scrolled content's coordinate space the rows report their frames in.
enum ColumnRows {
    static let space = "column.rows"
}

/// The rows' frames in the column's scrolled content, for its hover.
struct ColumnRowFrames: PreferenceKey {
    static let defaultValue: [String: CGRect] = [:]
    static func reduce(value: inout [String: CGRect], nextValue: () -> [String: CGRect]) {
        value.merge(nextValue()) { $1 }
    }
}

extension View {
    /// Report this row's frame (in `ColumnRows.space`) under `id`.
    func columnRow(_ id: String) -> some View {
        background(
            GeometryReader { g in
                Color.clear.preference(key: ColumnRowFrames.self, value: [id: g.frame(in: .named(ColumnRows.space))])
            })
    }
}

/// The hover of a column and its preview (PLAN 16, 19): one 20 Hz poll maps
/// the pointer to a row and that row is both the highlight and what the
/// preview follows, so the two can never disagree.
///
/// The map is AppKit's, read on every tick: the pointer goes through the
/// column's scroll view into its document (`NSView.convert`), where each row
/// reported the frame it is laid out at. Those frames do not move when the
/// column scrolls, so no SwiftUI geometry has to catch up with a scroll
/// (momentum, the elastic bounce, `scrollTo`) before the map is right again.
/// The timer runs in the common modes, so a scroll or a menu's tracking does
/// not stop it.
///
/// It also looks at once, not at the next tick, whenever what it maps
/// through changes while the column is open: when the column opens, when a
/// layout lands new row frames, and when the panel is replaced. A pointer
/// already resting on a row starts that row's rest then, with no move.
@MainActor
final class ColumnHover {
    /// The column's panel (its controller swaps in a new one, `RailPanel.orderIn`).
    var panel: NSPanel {
        didSet {
            guard panel !== oldValue else { return }
            scroll = nil
            if running { tick() }
        }
    }
    private let column: HoverColumn
    /// The pointer, in screen coordinates (a seam for tests).
    var pointer: () -> NSPoint = { NSEvent.mouseLocation }
    var now: () -> Date = Date.init
    /// The key a row id previews (a pinned copy of a row previews its agent).
    var keyFor: (String) -> String = { $0 }
    /// What the preview shows now, nil when it is away: read every tick,
    /// since esc, a click on the row (its window opens) or a click elsewhere
    /// put it away without the column knowing.
    var showing: () -> String? = { nil }
    /// The pointer is on the preview: it stays.
    var holds: () -> Bool = { false }
    /// Show a key's preview, level with its row (screen rect).
    var onShow: (String, NSRect) -> Void = { _, _ in }
    var onHide: () -> Void = {}
    /// Off in tests, which tick it themselves.
    var timerEnabled = true

    private var timer: Timer?
    /// Between `start` and `stop`: the column is open.
    private(set) var running = false
    private var intent = PreviewHover()
    private var lastPointer: NSPoint?
    private weak var scroll: NSScrollView?
    /// A look asked for by a layout, not yet taken.
    private var lookPending = false

    init(panel: NSPanel, column: HoverColumn) {
        self.panel = panel
        self.column = column
        column.rowFramesChanged = { [weak self] in self?.framesChanged() }
    }

    /// The column opened: look now, then every tick.
    func start() {
        timer?.invalidate()
        timer = nil
        running = true
        if timerEnabled {
            let t = Timer(timeInterval: 1.0 / 20, repeats: true) { [weak self] _ in
                MainActor.assumeIsolated { self?.tick() }
            }
            RunLoop.main.add(t, forMode: .common)
            timer = t
        }
        tick()
    }

    /// New row frames: look on the next turn of the run loop. Not inside
    /// SwiftUI's preference callback, which is in the middle of a view
    /// update that must not publish the highlight.
    private func framesChanged() {
        guard running, !lookPending else { return }
        lookPending = true
        RunLoop.main.perform(inModes: [.common]) { [weak self] in
            MainActor.assumeIsolated {
                guard let self else { return }
                self.lookPending = false
                if self.running { self.tick() }
            }
        }
    }

    /// The column closed: its preview goes, and the next open starts fresh.
    func stop() {
        timer?.invalidate()
        timer = nil
        running = false
        if showing() != nil { onHide() }
        intent = PreviewHover()
        column.hovered = nil
        column.keyboardLit = false
        lastPointer = nil
    }

    /// The column's scroll view (SwiftUI's ScrollView is an NSScrollView).
    private func scrollView() -> NSScrollView? {
        if let s = scroll, s.window === panel { return s }
        func find(_ v: NSView) -> NSScrollView? {
            if let s = v as? NSScrollView { return s }
            for sub in v.subviews { if let s = find(sub) { return s } }
            return nil
        }
        scroll = panel.contentView.flatMap(find)
        return scroll
    }

    /// A point in screen coordinates in the rows' space, nil off the
    /// column's visible rows (outside the panel, or over a row scrolled out
    /// of sight under its edges), and nil before SwiftUI has made the
    /// column's scroll view: the rows are not laid out yet, and the panel's
    /// top is not where they start (the Bots column's start below its
    /// header).
    func contentPoint(_ screen: NSPoint) -> CGPoint? {
        guard panel.frame.contains(screen), let s = scrollView(), let doc = s.documentView else { return nil }
        let inWindow = panel.convertPoint(fromScreen: screen)
        let p = doc.convert(inWindow, from: nil)
        guard s.documentVisibleRect.contains(p) else { return nil }
        return CGPoint(x: p.x, y: doc.isFlipped ? p.y : doc.bounds.height - p.y)
    }

    /// A row's frame in the rows' space, on screen.
    func screenRect(_ r: CGRect) -> NSRect {
        guard let s = scrollView(), let doc = s.documentView else {
            let f = panel.frame
            return NSRect(x: f.minX + r.minX, y: f.maxY - r.maxY, width: r.width, height: r.height)
        }
        let inDoc = doc.isFlipped ? r : NSRect(x: r.minX, y: doc.bounds.height - r.maxY, width: r.width, height: r.height)
        return panel.convertToScreen(doc.convert(inDoc, to: nil))
    }

    /// The row id at a point in the rows' space: the topmost row containing it.
    nonisolated static func row(at p: CGPoint, in frames: [String: CGRect]) -> String? {
        frames.filter { $0.value.contains(p) }.min { a, b in
            a.value.minY == b.value.minY ? a.key < b.key : a.value.minY < b.value.minY
        }?.key
    }

    /// The row under the pointer now, with its screen rect.
    func rowUnderPointer() -> (id: String, rect: NSRect)? {
        guard let p = contentPoint(pointer()), let id = Self.row(at: p, in: column.rowFrames),
              let r = column.rowFrames[id]
        else { return nil }
        return (id, screenRect(r))
    }

    /// One poll: the highlight follows the pointer's row (the keyboard's,
    /// while the pointer has not moved since a key moved it), and the
    /// preview opens on a row rested on for rail.hover's item_open_ms and goes
    /// panel_close_ms after the pointer has left the row and the preview.
    func tick() {
        let h = Theme.Rail.hover
        let at = pointer()
        let moved = lastPointer.map { $0 != at } ?? true
        lastPointer = at
        let row = rowUnderPointer()
        if moved, column.keyboardLit { column.keyboardLit = false }
        let lit = column.keyboardLit ? nil : row?.id
        if column.hovered != lit { column.hovered = lit }
        let action = intent.tick(
            row: row.map { keyFor($0.id) }, showing: showing(), holds: holds(), now: now(),
            openAfter: h.number("item_open_ms") / 1000, closeAfter: h.number("panel_close_ms") / 1000)
        switch action {
        case .none: break
        case .hide: onHide()
        case .show(let key):
            guard let row else { return }
            onShow(key, row.rect)
        }
    }

    /// J/K moved the selection: the keyboard has the highlight until the
    /// pointer moves.
    func keyboardMoved() {
        column.keyboardLit = true
        column.hovered = nil
        lastPointer = pointer()
    }
}

/// When a column's hover preview opens, moves and goes, from the row under
/// the pointer and what the preview shows now (PLAN 16). Every row the
/// pointer rests on for `openAfter` previews, moving to another row swaps
/// it after the same rest, and the rest is timed per row key, so a fleet
/// poll redrawing the rows does not restart it.
struct PreviewHover {
    enum Action: Equatable {
        case none
        case show(String)
        case hide
    }

    private(set) var resting: (key: String, since: Date)?
    private var away: Date?
    /// The row the column last previewed.
    private var asked: String?
    /// A row whose preview something else put away (esc, a click) while the
    /// pointer stayed on it: not again until the pointer leaves that row.
    private(set) var spent: String?

    mutating func tick(
        row: String?, showing: String?, holds: Bool, now: Date, openAfter: Double, closeAfter: Double
    ) -> Action {
        if let a = asked, showing != a {
            // Put away (or replaced) from elsewhere.
            if showing == nil, row == a { spent = a }
            asked = showing
        }
        guard let row else {
            resting = nil
            spent = nil
            guard showing != nil, !holds else {
                away = nil
                return .none
            }
            if away == nil { away = now }
            guard now.timeIntervalSince(away!) >= closeAfter else { return .none }
            away = nil
            asked = nil
            return .hide
        }
        away = nil
        if spent != row { spent = nil }
        if resting?.key != row { resting = (row, now) }
        guard showing != row, spent != row, let since = resting?.since, now.timeIntervalSince(since) >= openAfter else {
            return .none
        }
        asked = row
        return .show(row)
    }
}
