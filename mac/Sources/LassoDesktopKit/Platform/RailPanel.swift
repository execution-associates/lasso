import AppKit
import SwiftUI

/// The borderless, non-activating panel the rail lives in: on every Space,
/// over full-screen apps, transparent (clicks on empty pixels fall through to
/// the window below), and able to take key status for keyboard navigation
/// without activating the app or stealing the user's main window.
public final class RailPanel: NSPanel {
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

    public override func sendEvent(_ event: NSEvent) {
        if event.type == .keyDown, onKey?(event) == true { return }
        if event.type == .keyUp, onKeyUp?(event) == true { return }
        super.sendEvent(event)
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
}

/// Where a panel beside the rail goes: on the rail's leading side, top-aligned
/// with it, kept on screen (Theme.Card.placement).
@MainActor
func originBesideRail(_ rail: NSRect, size: NSSize) -> NSPoint {
    let gap = Theme.Card.placement.number("gap")
    let margin = Theme.Rail.panelMargin.pt
    let vis = NSScreen.main?.visibleFrame ?? .zero
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
    let panel: RailPanel
    private var screenObserver: NSObjectProtocol?
    private var hoverTimer: Timer?
    private var inSince: Date?
    private var outSince: Date?
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
    }

    public func show() {
        panel.orderFrontRegardless()
        hoverTimer = Timer.scheduledTimer(withTimeInterval: 1.0 / 20, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.pollHover() }
        }
    }

    /// Expand the rail and give it keyboard focus (the summon hotkey).
    public func summon() {
        model.setExpanded(true)
        model.focus = .inbox
        model.focusVisible = true
        panel.makeKeyAndOrderFront(nil)
    }

    private func place() {
        guard let v = NSScreen.main?.visibleFrame else { return }
        let f = panel.frame
        let stored = UserDefaults.standard.object(forKey: Self.positionKey) as? Double ?? 0.5
        let travel = max(0, v.height - f.height)
        let y = v.maxY - f.height - travel * CGFloat(min(max(stored, 0), 1))
        panel.setFrameOrigin(NSPoint(x: (v.maxX - f.width).rounded(), y: y.rounded()))
    }

    /// Move the rail to `y` (the panel's origin), kept so the revealed rail
    /// fits on screen, and remember where it is.
    private func move(toY y: CGFloat) {
        guard let v = NSScreen.main?.visibleFrame else { return }
        let f = panel.frame
        let clamped = min(max(v.minY, y), v.maxY - f.height)
        panel.setFrameOrigin(NSPoint(x: f.minX, y: clamped.rounded()))
        let travel = v.height - f.height
        if travel > 0 { UserDefaults.standard.set(Double((v.maxY - f.height - clamped) / travel), forKey: Self.positionKey) }
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
            return NSRect(x: f.minX, y: f.minY, width: f.width + 2, height: f.height)
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
        return RailMetrics.itemFrames(model.slots.count).first { $0.1.contains(p) }?.0
    }

    /// Resting on a control for `item_open_ms` opens it.
    private func pollItemHover() {
        let item = itemUnderPointer()
        if item != model.hovered {
            model.hovered = item
            hoverSince = item == nil ? nil : Date()
            hoverOpened = false
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
        let inside = hotRect().contains(NSEvent.mouseLocation)
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
