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

    public init(model: RailModel) {
        self.model = model
        let size = NSSize(
            width: RailMetrics.panelWidth,
            height: RailMetrics.expandedHeight(Theme.Rail.maxSlots.int) + 2 * RailMetrics.margin)
        let host = NSHostingView(rootView: RailView(model: model))
        host.frame = NSRect(origin: .zero, size: size)
        panel = RailPanel(content: host, size: size)
        panel.onKey = { [weak self] e in self?.handle(e) ?? false }
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
        guard let screen = NSScreen.main else { return }
        let v = screen.visibleFrame
        let f = panel.frame
        panel.setFrameOrigin(NSPoint(x: (v.maxX - f.width).rounded(), y: (v.midY - f.height / 2).rounded()))
    }

    /// Screen rects the pointer can be in: the peek's sliver at rest, the
    /// rail's own column when revealed.
    private func hotRect() -> NSRect {
        let f = panel.frame
        if model.expanded {
            return NSRect(x: f.minX, y: f.minY, width: f.width + 2, height: f.height)
        }
        let p = Theme.Rail.peek
        let peekTop = RailMetrics.margin + RailMetrics.outer / 2 - p.number("height") / 2
        return NSRect(
            x: f.maxX - p.number("visible") - 2, y: f.maxY - peekTop - p.number("height"),
            width: p.number("visible") + 4, height: p.number("height"))
    }

    private func pollHover() {
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
