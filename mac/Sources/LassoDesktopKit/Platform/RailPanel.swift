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

/// Hosts the rail at the trailing screen edge and wires keys to its model.
@MainActor
public final class RailController {
    public let model: RailModel
    let panel: RailPanel
    private var screenObserver: NSObjectProtocol?

    public init(model: RailModel) {
        self.model = model
        let margin = Theme.Rail.panelMargin.pt
        let size = NSSize(
            width: RailMetrics.outer + 2 * margin,
            height: RailMetrics.expandedHeight(Theme.Rail.maxSlots.int) + 2 * margin)
        let root = RailView(model: model)
            .padding(margin)
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topTrailing)
        let host = NSHostingView(rootView: root)
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

    public func show() { panel.orderFrontRegardless() }

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
        let margin = Theme.Rail.panelMargin.pt
        let x = v.maxX - Theme.Rail.edgeInset.pt - RailMetrics.outer - margin
        panel.setFrameOrigin(NSPoint(x: x.rounded(), y: (v.midY - f.height / 2).rounded()))
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
            } else {
                panel.resignKey()
            }
        default:
            return false
        }
        return true
    }
}
