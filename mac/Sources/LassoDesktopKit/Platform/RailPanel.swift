import AppKit
import Combine
import SwiftUI

/// The borderless, non-activating panel the rail lives in: on every Space,
/// over full-screen apps, transparent (clicks on empty pixels fall through to
/// the window below), and able to take key status for keyboard navigation
/// without activating the app or stealing the user's main window.
public class RailPanel: NSPanel {
    /// Gets every key-down first; returns true when it handled the event.
    public var onKey: ((NSEvent) -> Bool)?
    /// Gets every key-up (push-to-talk ends on V's release).
    public var onKeyUp: ((NSEvent) -> Bool)?

    public init(content: NSView, size: NSSize) {
        super.init(
            contentRect: NSRect(origin: .zero, size: size), styleMask: [.borderless, .nonactivatingPanel],
            backing: .buffered, defer: false)
        isOpaque = false
        backgroundColor = .clear
        hasShadow = false
        level = .statusBar
        collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary, .stationary, .ignoresCycle]
        isMovable = false
        hidesOnDeactivate = false
        becomesKeyOnlyIfNeeded = false
        contentView = content
    }

    public override var canBecomeKey: Bool { true }
    public override var canBecomeMain: Bool { false }

    /// A text field is being typed in: its field editor is the first
    /// responder. One-letter keys belong to it then, typed or not yet.
    public var editingText: Bool { firstResponder is NSText }

    /// A click inside the panel.
    public var onMouseDown: (() -> Void)?

    public override func sendEvent(_ event: NSEvent) {
        if event.type == .leftMouseDown { onMouseDown?() }
        if event.type == .keyDown, onKey?(event) == true { return }
        if event.type == .keyUp, onKeyUp?(event) == true { return }
        super.sendEvent(event)
    }

    // MARK: Spaces (PLAN 16)

    /// Whether a panel just ordered in is on the Space the user is on (a
    /// seam for tests).
    static var onActiveSpace: (NSWindow) -> Bool = { $0.isOnActiveSpace }

    /// Order the panel in on the Space the user is on, with the keyboard or
    /// without. Every panel of the app's is shown through here.
    ///
    /// When a Lasso window's full screen ends (the herdr window leaving it,
    /// or closing in it), macOS pins every Lasso window that is off screen
    /// at that moment to one ordinary Space: `.canJoinAllSpaces` stops
    /// applying to it, setting the behaviour again does not undo it, and it
    /// orders in there, out of sight of another app's full screen. A new
    /// window is not pinned, so a panel found off the active Space is
    /// replaced by one carrying its content, settings and handlers.
    /// `replaced` gets the new panel before it is shown, to hold it from now on.
    func orderIn(takeKey: Bool, replaced: (RailPanel) -> Void) {
        orderFrontRegardless()
        var shown = self
        if !Self.onActiveSpace(self) {
            let fresh = replacement()
            orderOut(nil)
            handOver(to: fresh)
            replaced(fresh)
            fresh.orderFrontRegardless()
            shown = fresh
            Trace.event("a panel was pinned to another Space: replaced")
        }
        if takeKey { shown.makeKeyAndOrderFront(nil) }
    }

    /// An empty panel of this kind, for `orderIn` (subclasses with their
    /// own initializer make their own).
    func replacement() -> RailPanel { RailPanel(content: NSView(), size: frame.size) }

    /// Give `p` this panel's settings, handlers, frame and content.
    func handOver(to p: RailPanel) {
        p.level = level
        p.collectionBehavior = collectionBehavior
        p.hasShadow = hasShadow
        p.isMovable = isMovable
        p.alphaValue = alphaValue
        p.appearance = appearance
        p.title = title
        p.onKey = onKey
        p.onKeyUp = onKeyUp
        p.onMouseDown = onMouseDown
        p.setFrame(frame, display: false)
        if let content = contentView {
            contentView = NSView(frame: content.frame)
            p.contentView = content
        }
    }
}

/// Key codes the rail and card bind (Carbon virtual key codes, layout
/// independent for these).
public enum KeyCode {
    public static let returnKey: UInt16 = 36
    public static let keypadEnter: UInt16 = 76
    public static let space: UInt16 = 49
    public static let escape: UInt16 = 53
    public static let up: UInt16 = 126
    public static let down: UInt16 = 125
    public static let delete: UInt16 = 51
}

/// Which screen a window goes on: the one its anchor (the rail, the column,
/// the pointer) is on. Never `NSScreen.main`, which follows the key window:
/// with none key (the herdr window just closed, out of full screen or not,
/// in an app that is otherwise all non-activating panels) it is nil, and
/// every pop-out placed against `.zero` landed below the screen, out of
/// sight but open: the rail stayed out for them and nothing showed.
enum Screens {
    /// Of `screens` (frame, visible frame), the visible frame of the one
    /// holding `rect`'s centre, else the one most of it overlaps, else the
    /// nearest; nil only with no screens at all.
    nonisolated static func visibleFrame(for rect: NSRect, in screens: [(frame: NSRect, visible: NSRect)]) -> NSRect? {
        let c = NSPoint(x: rect.midX, y: rect.midY)
        if let s = screens.first(where: { $0.frame.contains(c) }) { return s.visible }
        func overlap(_ f: NSRect) -> CGFloat {
            let i = f.intersection(rect)
            return i.isNull ? 0 : i.width * i.height
        }
        if let s = screens.max(by: { overlap($0.frame) < overlap($1.frame) }), overlap(s.frame) > 0 { return s.visible }
        func distance(_ f: NSRect) -> CGFloat { hypot(f.midX - c.x, f.midY - c.y) }
        return screens.min { distance($0.frame) < distance($1.frame) }?.visible
    }

    /// The visible frame of the screen `rect` is on, now.
    @MainActor static func visibleFrame(for rect: NSRect) -> NSRect {
        visibleFrame(for: rect, in: NSScreen.screens.map { ($0.frame, $0.visibleFrame) })
            ?? .zero
    }

    /// The visible frame of the screen under the pointer.
    @MainActor static var underPointer: NSRect { visibleFrame(for: NSRect(origin: NSEvent.mouseLocation, size: .zero)) }
}

/// Where a panel beside the rail goes: on the rail's leading side, top-aligned
/// with it, kept on the rail's screen (Theme.Card.placement).
@MainActor
func originBesideRail(_ rail: NSRect, size: NSSize) -> NSPoint {
    originBesideRail(rail, size: size, visible: Screens.visibleFrame(for: rail))
}

@MainActor
func originBesideRail(_ rail: NSRect, size: NSSize, visible vis: NSRect) -> NSPoint {
    let gap = Theme.Card.placement.number("gap")
    let margin = Theme.Rail.panelMargin.pt
    let x = max(vis.minX, rail.minX + margin - gap - size.width)
    let y = min(max(vis.minY, rail.maxY - margin - size.height), vis.maxY - size.height)
    return NSPoint(x: x.rounded(), y: y.rounded())
}

/// Hosts the rail at the trailing screen edge, revealed by hover: resting
/// on the peek opens it, leaving it (with nothing open beside it) closes it.
/// The pointer is polled rather than tracked, because a non-activating panel
/// of an app in the background gets no hover events from AppKit.
@MainActor
public final class RailController {
    public let model: RailModel
    private(set) var panel: RailPanel
    private var screenObserver: NSObjectProtocol?
    private var keyObserver: NSObjectProtocol?
    private var fleetChange: AnyCancellable?
    /// The rail height the panel was last placed for.
    private var placedHeight: CGFloat = 0
    private var hoverTimer: Timer?
    private var inSince: Date?
    private var outSince: Date?
    /// Where the pointer was at the last poll: a move off the rail ends the
    /// keyboard's hold on it.
    private var lastPointer: NSPoint?
    /// Something beside the rail is open (card, list, chat): stay revealed.
    var keepOpen: () -> Bool = { false }
    /// Every hover poll, with the pointer (screen coordinates): the
    /// coordinator closes a panel a hover opened once the pointer leaves it.
    var onPointer: (NSPoint) -> Void = { _ in }
    /// The rail was dragged: whatever is open beside it follows.
    var onMoved: () -> Void = {}
    /// When the pointer came to rest on `model.hovered`, and whether that
    /// visit has opened it already (one open per visit).
    private var hoverSince: Date?
    private var hoverOpened = false
    /// Where the drag started: the pointer's and the panel's y.
    private var dragFrom: (pointer: CGFloat, origin: CGFloat)?

    /// The rail's place on the edge, as UserDefaults `railPosition`: 0 puts it
    /// at the top of the screen's visible frame, 1 at the bottom, 0.5 (unset)
    /// centred. A fraction rather than points, so it survives a screen change.
    /// It places the rail as it is now, not the panel: the panel is as tall
    /// as the tallest rail, with the rail at its top and the rest empty.
    static let positionKey = "railPosition"

    public init(model: RailModel) {
        self.model = model
        let size = NSSize(
            width: RailMetrics.panelWidth,
            height: RailMetrics.expandedHeight(Theme.Rail.maxSlots.int) + 2 * RailMetrics.margin)
        let host = NSHostingView(rootView: RailView(model: model))
        host.frame = NSRect(origin: .zero, size: size)
        panel = RailPanel(content: host, size: size)
        panel.onKey = { [weak self] e in self?.handle(e) ?? false }
        model.onDrag = { [weak self] phase in self?.drag(phase) }
        place()
        screenObserver = NotificationCenter.default.addObserver(
            forName: NSApplication.didChangeScreenParametersNotification, object: nil, queue: .main
        ) { [weak self] _ in
            MainActor.assumeIsolated { self?.place() }
        }
        // Slots and the Bots button come and go: keep the rail where it was
        // along the edge, the middle staying the middle.
        fleetChange = model.fleet.objectWillChange.receive(on: DispatchQueue.main).sink { [weak self] _ in
            MainActor.assumeIsolated {
                guard let self, self.dragFrom == nil, self.railHeight != self.placedHeight else { return }
                self.place()
                self.onMoved()
            }
        }
        // The keyboard went elsewhere: its focus ring goes with it.
        keyObserver = NotificationCenter.default.addObserver(
            forName: NSWindow.didResignKeyNotification, object: nil, queue: .main
        ) { [weak self] n in
            let window = n.object.map { ObjectIdentifier($0 as AnyObject) }
            MainActor.assumeIsolated {
                guard let self, window == ObjectIdentifier(self.panel) else { return }
                self.model.focusVisible = false
            }
        }
    }

    public func show() {
        panel.orderIn(takeKey: false) { panel = $0 }
        hoverTimer = Timer.scheduledTimer(withTimeInterval: 1.0 / 20, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.pollHover() }
        }
    }

    /// Expand the rail and give it keyboard focus (the summon hotkey).
    public func summon() {
        model.setExpanded(true)
        model.focus = .inbox
        model.focusVisible = true
        panel.orderIn(takeKey: true) { panel = $0 }
    }

    /// The visible frame of the screen the rail is on.
    private var screen: NSRect { Screens.visibleFrame(for: panel.frame) }

    /// The revealed rail's height as it is now (its slots, the Bots button
    /// while lasso has bots), with the panel's margins: the part of the
    /// panel, from its top, that the rail can fill.
    var railHeight: CGFloat {
        RailMetrics.expandedHeight(model.slots.count, bots: model.hasBots) + 2 * RailMetrics.margin
    }

    private func place() {
        let v = screen
        let f = panel.frame
        let stored = UserDefaults.standard.object(forKey: Self.positionKey) as? Double ?? 0.5
        placedHeight = railHeight
        panel.setFrameOrigin(
            NSPoint(x: (v.maxX - f.width).rounded(), y: Self.y(stored, visible: v, height: placedHeight, panel: f.height)))
    }

    /// The panel's origin y with a rail `height` tall (at the panel's top;
    /// `panel` defaults to the rail's own height) at `fraction` of the travel
    /// down `visible`'s edge (0 the top, 0.5 the middle, a fresh install's
    /// place, 1 the bottom).
    nonisolated static func y(_ fraction: Double, visible v: NSRect, height: CGFloat, panel: CGFloat? = nil) -> CGFloat {
        let travel = max(0, v.height - height)
        return (v.maxY - travel * CGFloat(min(max(fraction, 0), 1)) - (panel ?? height)).rounded()
    }

    /// Whether a panel at `y` holds its rail in the vertical middle of
    /// `visible`, within a point.
    nonisolated static func isCentred(_ y: CGFloat, visible: NSRect, height: CGFloat, panel: CGFloat? = nil) -> Bool {
        abs(y - Self.y(0.5, visible: visible, height: height, panel: panel)) <= 1
    }

    /// The rail is in the vertical middle of its screen.
    public var isCentred: Bool {
        Self.isCentred(panel.frame.minY, visible: screen, height: railHeight, panel: panel.frame.height)
    }

    /// Settings › Info's Center vertically: back to the middle of the edge,
    /// where a fresh install puts it (nothing stored), and what is open
    /// beside it follows.
    public func centre() {
        UserDefaults.standard.removeObject(forKey: Self.positionKey)
        let v = screen
        var f = panel.frame
        placedHeight = railHeight
        f.origin = NSPoint(x: (v.maxX - f.width).rounded(), y: Self.y(0.5, visible: v, height: placedHeight, panel: f.height))
        panel.setFrame(f, display: true, animate: true)
        onMoved()
        Trace.event("rail centred")
    }

    /// Move the rail to `y` (the panel's origin), kept so the revealed rail
    /// fits on screen (the empty rest of the panel may hang below it), and
    /// remember where it is.
    private func move(toY y: CGFloat) {
        let v = screen
        let f = panel.frame
        let h = railHeight
        placedHeight = h
        let top = min(max(v.minY + h, y + f.height), v.maxY)
        panel.setFrameOrigin(NSPoint(x: f.minX, y: (top - f.height).rounded()))
        let travel = v.height - h
        if travel > 0 { UserDefaults.standard.set(Double((v.maxY - top) / travel), forKey: Self.positionKey) }
        onMoved()
    }

    /// Dragging the peek or the rail slides it along the edge. While a drag
    /// is on, hover neither reveals nor hides it.
    private func drag(_ phase: RailModel.DragPhase) {
        let pointer = NSEvent.mouseLocation.y
        switch phase {
        case .changed:
            if dragFrom == nil { dragFrom = (pointer, panel.frame.minY) }
            if let from = dragFrom { move(toY: from.origin + pointer - from.pointer) }
        case .ended:
            dragFrom = nil
            Trace.event("rail moved")
        }
    }

    /// Screen rects the pointer can be in: the peek's sliver at rest, the
    /// rail's own column when revealed.
    private func hotRect() -> NSRect {
        let f = panel.frame
        if model.expanded {
            let h = min(railHeight, f.height)
            return NSRect(x: f.minX, y: f.maxY - h, width: f.width + 2, height: h)
        }
        let p = Theme.Rail.peek
        return NSRect(
            x: f.maxX - p.number("width") - 2, y: f.maxY - RailMetrics.peekTop - p.number("height"),
            width: p.number("width") + 4, height: p.number("height"))
    }

    /// The revealed control under the pointer, if any.
    private func itemUnderPointer() -> RailModel.Item? {
        guard model.expanded else { return nil }
        let f = panel.frame
        let m = NSEvent.mouseLocation
        let p = CGPoint(x: m.x - f.minX, y: f.maxY - m.y)
        return RailMetrics.itemFrames(model.slots.count, bots: model.hasBots).first { $0.1.contains(p) }?.0
    }

    /// Resting on a control for `item_open_ms` opens it.
    private func pollItemHover() {
        let item = itemUnderPointer()
        if item != model.hovered {
            model.hovered = item
            hoverSince = item == nil ? nil : Date()
            hoverOpened = false
            // The pointer took over from the keyboard.
            if item != nil { model.focusVisible = false }
        }
        guard let item, !hoverOpened, let since = hoverSince,
              Date().timeIntervalSince(since) * 1000 >= Theme.Rail.hover.number("item_open_ms")
        else { return }
        hoverOpened = true
        model.focus = item
        model.onHoverOpen(item)
        Trace.event("hover opened \(item)")
    }

    private func pollHover() {
        guard dragFrom == nil else { return }
        pollItemHover()
        onPointer(NSEvent.mouseLocation)
        let at = NSEvent.mouseLocation
        let inside = hotRect().contains(at)
        // The pointer moving elsewhere takes over from the keyboard: the
        // ring goes, and with it the hold that keeps a summoned rail out.
        if let last = lastPointer, last != at, !inside, model.focusVisible { model.focusVisible = false }
        lastPointer = at
        let h = Theme.Rail.hover
        let now = Date()
        if inside {
            outSince = nil
            if inSince == nil { inSince = now }
            if !model.expanded, now.timeIntervalSince(inSince!) * 1000 >= h.number("open_ms") {
                model.setExpanded(true)
                Trace.event("rail revealed (hover)")
            }
        } else {
            inSince = nil
            guard model.expanded, !keepOpen(), !panel.isKeyWindow || !model.focusVisible else {
                outSince = nil
                return
            }
            if outSince == nil { outSince = now }
            if now.timeIntervalSince(outSince!) * 1000 >= h.number("close_ms") {
                outSince = nil
                model.setExpanded(false)
                Trace.event("rail hidden (pointer left)")
            }
        }
    }

    private func handle(_ e: NSEvent) -> Bool {
        // ⌥↑/⌥↓ slide the rail along the edge: the drag, from the keyboard.
        if e.modifierFlags.contains(.option), e.keyCode == KeyCode.up || e.keyCode == KeyCode.down {
            let step = Theme.Rail.drag.number("nudge")
            move(toY: panel.frame.minY + (e.keyCode == KeyCode.up ? step : -step))
            return true
        }
        switch e.keyCode {
        case KeyCode.up:
            model.moveFocus(-1)
        case KeyCode.down:
            model.moveFocus(1)
        case KeyCode.returnKey, KeyCode.keypadEnter, KeyCode.space:
            model.focusVisible = true
            model.activate(model.focus ?? .inbox)
        case KeyCode.escape:
            if model.expanded {
                model.setExpanded(false)
                model.focusVisible = false
            } else {
                panel.resignKey()
            }
        default:
            return false
        }
        return true
    }
}
