import AppKit
import Combine
import SwiftUI

/// An agent's own window, opened by a click on its row: it floats over every
/// app (full-screen ones included) and follows the user across Spaces, moves
/// by its header, resizes by its corner, and collapses to its header.
final class AgentWindowPanel: RailPanel {
    init(content: NSView, frame: NSRect) {
        super.init(content: content, size: frame.size)
        setFrame(frame, display: false)
        level = .floating
        collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary, .ignoresCycle]
        hasShadow = true
    }
}

/// The window's own state: collapsed or not, and the moves its chrome asks for.
@MainActor
final class AgentWindowChrome: ObservableObject {
    @Published var collapsed = false
    var onToggle: () -> Void = {}
    var onClose: () -> Void = {}
    var onDrag: (_ ended: Bool) -> Void = { _ in }
    var onResize: (_ ended: Bool) -> Void = { _ in }
}

struct AgentWindowView: View {
    @ObservedObject var model: CardModel
    @ObservedObject var chrome: AgentWindowChrome
    /// The header and the phase read the fleet's live row (`model.current`),
    /// which changes without the card publishing anything.
    @ObservedObject var fleet: FleetModel

    init(model: CardModel, chrome: AgentWindowChrome) {
        self.model = model
        self.chrome = chrome
        fleet = model.fleet
    }

    var body: some View {
        let w = Theme.Agent.window
        VStack(spacing: 0) {
            AgentHeader(pane: model.current, model: model) { buttons }
                .contentShape(Rectangle())
                .gesture(
                    DragGesture(minimumDistance: Theme.Rail.drag.number("min_distance"), coordinateSpace: .global)
                        .onChanged { _ in chrome.onDrag(false) }
                        .onEnded { _ in chrome.onDrag(true) })
                .parityAnchor("agent.header")
            if !chrome.collapsed {
                Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair"))
                AgentConversation(
                    entries: ConversationEntry.fold(model.chat?.items.suffix(160).map { $0 } ?? [], cwd: model.chat?.cwd),
                    pending: model.pending, phase: model.phase, outgoing: model.outgoing, host: model.mediaHost)
                .killingDim(model)
                StepList(model: model)
                    .opacity(model.killing ? Theme.Card.killing.number("dim") : 1)
                AgentComposer(model: model, showsKill: true)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        .background(PanelBackground())
        .clipShape(RoundedRectangle(cornerRadius: w.number("radius"), style: .continuous))
        .overlay(alignment: .bottomTrailing) { if !chrome.collapsed { grip } }
        .parityAnchor(chrome.collapsed ? "agent.window.collapsed" : "agent.window")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("\(model.current.displayName), agent window")
    }

    private var buttons: some View {
        HStack(spacing: Theme.space("xs")) {
            if model.canShowInHerdr {
                WindowButton(symbol: "terminal", label: "Show \(model.current.displayName) in herdr") { model.showInHerdr() }
                    .parityAnchor("agent.herdr")
            }
            WindowButton(symbol: chrome.collapsed ? "arrow.up.left.and.arrow.down.right" : "minus",
                         label: chrome.collapsed ? "Expand" : "Minimize") { chrome.onToggle() }
                .parityAnchor("agent.minimize")
            WindowButton(symbol: "xmark", label: "Close") { chrome.onClose() }
                .parityAnchor("agent.close")
        }
    }

    /// The corner to resize by.
    private var grip: some View {
        Image(systemName: "line.diagonal")
            .font(.system(size: Theme.icon.number("control")))
            .foregroundStyle(Theme.Text.secondary.color)
            .frame(width: Theme.Agent.window.number("button"), height: Theme.Agent.window.number("button"))
            .contentShape(Rectangle())
            .gesture(
                DragGesture(minimumDistance: 0, coordinateSpace: .global)
                    .onChanged { _ in chrome.onResize(false) }
                    .onEnded { _ in chrome.onResize(true) })
            .accessibilityHidden(true)
    }
}

/// A plain glyph button in the window's header (no circle, as the reference).
struct WindowButton: View {
    let symbol: String
    let label: String
    let action: () -> Void

    var body: some View {
        let w = Theme.Agent.window
        Button(action: action) {
            Image(systemName: symbol)
                .font(.system(size: w.number("icon"), weight: .medium))
                .foregroundStyle(Theme.Text.primary.color)
                .frame(width: w.number("button"), height: w.number("button"))
                .contentShape(Rectangle())
        }
        .buttonStyle(RailPress())
        .accessibilityLabel(label)
        .help(label)
    }
}

/// One agent's window: open, move, resize, collapse, close. Where it was is
/// remembered per agent (UserDefaults `agentWindowFrames`).
@MainActor
final class AgentWindowController {
    let model: CardModel
    let panel: AgentWindowPanel
    private let chrome = AgentWindowChrome()
    var onClosed: () -> Void = {}
    private var dragFrom: (pointer: NSPoint, frame: NSRect)?
    /// The full size to come back to from collapsed.
    private var expandedFrame: NSRect

    static let framesKey = "agentWindowFrames"

    init(model: CardModel, near anchor: NSRect, media: MediaSource?) {
        self.model = model
        let w = Theme.Agent.window
        let size = w.size2("size")
        let vis = NSScreen.main?.visibleFrame ?? .zero
        var frame = NSRect(
            x: anchor.minX - Theme.Agent.preview.number("gap") - size.width, y: anchor.maxY - size.height,
            width: size.width, height: size.height)
        if let saved = (UserDefaults.standard.dictionary(forKey: Self.framesKey)?[model.pane.key] as? String).map(NSRectFromString),
           saved.width > 0, vis.intersects(saved)
        {
            frame = saved
        }
        frame.origin.x = max(vis.minX, min(frame.origin.x, vis.maxX - frame.width))
        frame.origin.y = max(vis.minY, min(frame.origin.y, vis.maxY - frame.height))
        expandedFrame = frame
        let host = NSHostingView(rootView: AgentWindowView(model: model, chrome: chrome).environment(\.mediaSource, media))
        host.frame = NSRect(origin: .zero, size: frame.size)
        panel = AgentWindowPanel(content: host, frame: frame)
        panel.onKey = { [weak self] e in self?.handle(e) ?? false }
        // A kill that went through closes the window (CardModel.resolved).
        model.onClose = { [weak self] in self?.close() }
        chrome.onToggle = { [weak self] in self?.toggle() }
        chrome.onClose = { [weak self] in self?.close() }
        chrome.onDrag = { [weak self] ended in self?.drag(ended, resize: false) }
        chrome.onResize = { [weak self] ended in self?.drag(ended, resize: true) }
    }

    /// Bring it forward with the keyboard. `focusReply` puts the cursor in
    /// the composer, as L does: a window opened on a new agent is for
    /// writing to it, even while it is still starting.
    func show(focusReply: Bool = false) {
        model.startPolling()
        panel.makeKeyAndOrderFront(nil)
        if focusReply { model.focusComposer() }
    }

    func close() {
        model.stopPolling()
        remember()
        panel.orderOut(nil)
        onClosed()
    }

    /// Collapse to the header, or back to the size it had, top edge fixed.
    func toggle() {
        let f = panel.frame
        if chrome.collapsed {
            let e = NSRect(x: f.minX, y: f.maxY - expandedFrame.height, width: expandedFrame.width, height: expandedFrame.height)
            chrome.collapsed = false
            panel.setFrame(e, display: true, animate: false)
        } else {
            expandedFrame = f
            let c = Theme.Agent.collapsed.size2("size")
            chrome.collapsed = true
            panel.setFrame(NSRect(x: f.minX, y: f.maxY - c.height, width: c.width, height: c.height), display: true, animate: false)
        }
    }

    /// Header drags move it; the corner drags resize it, top-left fixed.
    private func drag(_ ended: Bool, resize: Bool) {
        let p = NSEvent.mouseLocation
        if ended {
            dragFrom = nil
            if !chrome.collapsed { expandedFrame = panel.frame }
            remember()
            return
        }
        if dragFrom == nil { dragFrom = (p, panel.frame) }
        guard let from = dragFrom else { return }
        let dx = p.x - from.pointer.x
        let dy = p.y - from.pointer.y
        if resize, !chrome.collapsed {
            let min = Theme.Agent.window.size2("min")
            let width = max(min.width, from.frame.width + dx)
            let height = max(min.height, from.frame.height - dy)
            panel.setFrame(NSRect(x: from.frame.minX, y: from.frame.maxY - height, width: width, height: height), display: true)
        } else {
            panel.setFrameOrigin(NSPoint(x: from.frame.minX + dx, y: from.frame.minY + dy))
        }
    }

    private func remember() {
        var frames = UserDefaults.standard.dictionary(forKey: Self.framesKey) ?? [:]
        frames[model.pane.key] = NSStringFromRect(chrome.collapsed ? expandedFrame : panel.frame)
        UserDefaults.standard.set(frames, forKey: Self.framesKey)
    }

    /// The card's keys, minus paging and Acknowledge (CardKeys.windowSet):
    /// 1–9 highlight a choice, → takes it, ↳ edits it, ⏎ to the composer (or
    /// send), L to the composer, R renames, U attaches, ⌘. stops, X X kills.
    func handle(_ e: NSEvent) -> Bool {
        guard let a = CardKeys.route(e, editing: model.composerHasKeys || panel.editingText, among: CardKeys.windowSet)
        else { return false }
        return model.perform(a)
    }
}
