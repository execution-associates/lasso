import AppKit
import SwiftUI

// The herdr window and the Browser window (PLAN 22, 24) are agent windows in
// everything but their page (PLAN 19): the same panel (AgentWindowPanel:
// non-activating, at the floating level, on every Space and over full-screen
// apps), shown through RailPanel.orderIn, with the same header buttons
// (minimize, which collapses to the header, and close), moved by the header,
// resized by the corner (a double-click puts the default size back), and
// their frame remembered. A panel on every Space cannot go full screen, so
// neither window can.

/// SwiftUI over a page that comes and goes (the status covering the page
/// until it is up, the resize corner, hidden while collapsed): hidden, it
/// takes no clicks. The app is linked against the macOS 14 SDK, and under
/// that SDK's rule a hidden NSHostingView still answers hitTest: the Browser
/// window's status, hidden over its live page, took the page's every press,
/// so the release never reached WebKit and no click on the page acted (a
/// press still focused a field), and the herdr window's took xterm's.
final class OverlayHostingView<Content: View>: NSHostingView<Content> {
    override func hitTest(_ point: NSPoint) -> NSView? {
        isHiddenOrHasHiddenAncestor ? nil : super.hitTest(point)
    }
}

/// A lasso page's window: a header over its page, in an agent window's panel.
@MainActor
final class PageWindow {
    private(set) var panel: AgentWindowPanel
    let chrome = AgentWindowChrome()
    /// Where its frame is kept (UserDefaults `<name>Frame`).
    let name: String
    private let size: NSSize
    private let minSize: NSSize
    private let header: CGFloat
    private let pageBox: NSView
    private let grip: NSView
    /// What takes the keyboard when it is shown (its web view).
    weak var focus: NSView?
    /// It closed (its close button, ⌘W, or `close()`).
    var onClosed: () -> Void = {}
    /// Asked before the close button or ⌘W closes it: false keeps it open
    /// (a file window with unsaved edits asks first). `close()` never asks.
    var shouldClose: () -> Bool = { true }
    /// It took the keyboard.
    var onBecameKey: () -> Void = {}
    nonisolated(unsafe) private var keyWatch: NSObjectProtocol?
    private var dragFrom: (pointer: NSPoint, frame: NSRect)?
    private var dragMoved = false
    /// The full size to come back to from collapsed.
    private var expandedFrame: NSRect
    /// The frame was forgotten (`defaultSize`) and not moved since.
    private var forgotten = false

    /// `header` draws the window's header from its chrome; `page` fills the rest.
    /// `frame` places it instead of where it was left (a file window cascades
    /// from the last one).
    init<Header: View>(
        name: String, size: NSSize, min: NSSize, headerHeight: CGFloat, page: NSView, frame placed: NSRect? = nil,
        header makeHeader: (AgentWindowChrome) -> Header
    ) {
        self.name = name
        self.size = size
        minSize = min
        header = headerHeight
        let frame = placed.map { Self.onScreen($0) } ?? Self.initialFrame(name: name, size: size)
        expandedFrame = frame
        let bounds = NSRect(origin: .zero, size: frame.size)

        let root = NSView(frame: bounds)
        root.wantsLayer = true
        root.layer?.backgroundColor = Theme.Panel.surface.nsColor.cgColor
        root.layer?.cornerRadius = Theme.Agent.window.number("radius")
        root.layer?.cornerCurve = .continuous
        root.layer?.masksToBounds = true
        root.layer?.borderWidth = 1
        root.layer?.borderColor = Theme.Panel.border.nsColor.cgColor

        pageBox = NSView(frame: NSRect(x: 0, y: 0, width: bounds.width, height: bounds.height - headerHeight))
        pageBox.autoresizingMask = [.width, .height]
        page.frame = pageBox.bounds
        page.autoresizingMask = [.width, .height]
        pageBox.addSubview(page)
        root.addSubview(pageBox)

        let top = NSHostingView(rootView: makeHeader(chrome))
        top.frame = NSRect(x: 0, y: bounds.height - headerHeight, width: bounds.width, height: headerHeight)
        top.autoresizingMask = [.width, .minYMargin]
        root.addSubview(top)

        let side = Theme.Agent.window.number("button")
        let corner = OverlayHostingView(rootView: WindowGrip(chrome: chrome))
        corner.frame = NSRect(x: bounds.width - side, y: 0, width: side, height: side)
        corner.autoresizingMask = [.minXMargin, .maxYMargin]
        root.addSubview(corner)
        grip = corner

        panel = AgentWindowPanel(content: root, frame: frame)
        panel.isReleasedWhenClosed = false
        panel.onCloseKey = { [weak self] in self?.requestClose() }
        chrome.onToggle = { [weak self] in self?.toggle() }
        chrome.onClose = { [weak self] in self?.requestClose() }
        chrome.onDrag = { [weak self] ended in self?.drag(ended, resize: false) }
        chrome.onResize = { [weak self] ended in self?.drag(ended, resize: true) }
        chrome.onDefaultSize = { [weak self] in self?.defaultSize() }
        keyWatch = NotificationCenter.default.addObserver(forName: NSWindow.didBecomeKeyNotification, object: nil, queue: .main) {
            [weak self] n in
            let w = n.object as? NSWindow
            MainActor.assumeIsolated {
                guard let self, w === self.panel else { return }
                self.onBecameKey()
            }
        }
    }

    deinit {
        if let keyWatch { NotificationCenter.default.removeObserver(keyWatch) }
    }

    var isOpen: Bool { panel.isVisible }
    var collapsed: Bool { chrome.collapsed }

    /// Where it opens: where it was left, on whichever screen that is while
    /// that screen is there, else `size` centred on the screen under the
    /// pointer. Kept on the screen's visible frame.
    static func initialFrame(name: String, size: NSSize) -> NSRect {
        let frame: NSRect
        if let saved = storedFrame(name), saved.width > 0, NSScreen.screens.contains(where: { $0.visibleFrame.intersects(saved) }) {
            frame = saved
        } else {
            let vis = Screens.underPointer
            frame = NSRect(x: vis.midX - size.width / 2, y: vis.midY - size.height / 2, width: size.width, height: size.height)
        }
        return onScreen(frame)
    }

    /// `frame` kept on the visible frame of the screen it is on.
    static func onScreen(_ frame: NSRect) -> NSRect {
        var frame = frame
        let vis = Screens.visibleFrame(for: frame)
        frame.size.width = min(frame.width, vis.width)
        frame.size.height = min(frame.height, vis.height)
        frame.origin.x = max(vis.minX, min(frame.minX, vis.maxX - frame.width)).rounded()
        frame.origin.y = max(vis.minY, min(frame.minY, vis.maxY - frame.height)).rounded()
        return frame
    }

    static func frameKey(_ name: String) -> String { name + "Frame" }

    /// The remembered frame: ours, else the one AppKit kept for the titled
    /// window of the same name (its first four numbers).
    static func storedFrame(_ name: String) -> NSRect? {
        if let s = UserDefaults.standard.string(forKey: frameKey(name)) { return NSRectFromString(s) }
        guard let s = UserDefaults.standard.string(forKey: "NSWindow Frame " + name) else { return nil }
        let n = s.split(separator: " ").prefix(4).compactMap { Double($0) }
        return n.count == 4 ? NSRect(x: n[0], y: n[1], width: n[2], height: n[3]) : nil
    }

    /// Bring it forward on the Space the user is on; with the keyboard, its
    /// page takes it.
    func show(takeKey: Bool) {
        panel.orderIn(takeKey: takeKey) { if let p = $0 as? AgentWindowPanel { panel = p } }
        if takeKey, let focus { panel.makeFirstResponder(focus) }
    }

    /// The close button or ⌘W: closed, unless `shouldClose` keeps it.
    func requestClose() {
        if shouldClose() { close() }
    }

    /// Close it: put away, its frame remembered, then `onClosed`.
    func close() {
        guard panel.isVisible else { return }
        remember()
        panel.orderOut(nil)
        onClosed()
    }

    /// Collapse to the header, or back to the size it had, top edge fixed.
    /// Collapsed, the page is hidden (a hidden page stops the Browser's
    /// screencast, as a hidden tab does).
    func toggle() {
        let f = panel.frame
        if chrome.collapsed {
            let e = NSRect(x: f.minX, y: f.maxY - expandedFrame.height, width: expandedFrame.width, height: expandedFrame.height)
            chrome.collapsed = false
            pageBox.isHidden = false
            grip.isHidden = false
            panel.setFrame(e, display: true, animate: false)
            if let focus, panel.isKeyWindow { panel.makeFirstResponder(focus) }
        } else {
            expandedFrame = f
            let width = Theme.Agent.collapsed.size2("size").width
            chrome.collapsed = true
            pageBox.isHidden = true
            grip.isHidden = true
            panel.setFrame(NSRect(x: f.minX, y: f.maxY - header, width: width, height: header), display: true, animate: false)
        }
    }

    /// The corner's double-click: back to the size it opens at, its top-left
    /// kept and the whole window kept on its screen, and the remembered
    /// frame forgotten until it is moved or resized again. `animate` false
    /// in tests.
    func defaultSize(animate: Bool = true) {
        guard !chrome.collapsed else { return }
        let f = panel.frame
        let vis = Screens.visibleFrame(for: f)
        var r = NSRect(x: f.minX, y: f.maxY - size.height, width: size.width, height: size.height)
        r.origin.x = max(vis.minX, min(r.minX, vis.maxX - r.width))
        r.origin.y = max(vis.minY, min(r.minY, vis.maxY - r.height))
        expandedFrame = r
        panel.setFrame(r, display: true, animate: animate)
        UserDefaults.standard.removeObject(forKey: Self.frameKey(name))
        UserDefaults.standard.removeObject(forKey: "NSWindow Frame " + name)
        forgotten = true
    }

    /// Header drags move it; the corner drags resize it, top-left fixed.
    private func drag(_ ended: Bool, resize: Bool) {
        let p = NSEvent.mouseLocation
        if ended {
            if dragMoved { forgotten = false }
            dragMoved = false
            dragFrom = nil
            if !chrome.collapsed { expandedFrame = panel.frame }
            remember()
            return
        }
        if dragFrom == nil { dragFrom = (p, panel.frame) }
        guard let from = dragFrom else { return }
        let dx = p.x - from.pointer.x
        let dy = p.y - from.pointer.y
        guard dx != 0 || dy != 0 || dragMoved else { return }
        dragMoved = true
        if resize, !chrome.collapsed {
            let width = max(minSize.width, from.frame.width + dx)
            let height = max(minSize.height, from.frame.height - dy)
            panel.setFrame(NSRect(x: from.frame.minX, y: from.frame.maxY - height, width: width, height: height), display: true)
        } else {
            panel.setFrameOrigin(NSPoint(x: from.frame.minX + dx, y: from.frame.minY + dy))
        }
    }

    private func remember() {
        guard !forgotten else { return }
        let f = chrome.collapsed ? NSRect(x: panel.frame.minX, y: panel.frame.maxY - expandedFrame.height, width: expandedFrame.width, height: expandedFrame.height) : panel.frame
        UserDefaults.standard.set(NSStringFromRect(f), forKey: Self.frameKey(name))
    }
}

/// A page window's header: what it is at the leading end (`leading`), the
/// window's minimize and close at the trailing end, laid out first so a long
/// title truncates and never pushes them out. Dragging it moves the window.
struct PageWindowHeader<Leading: View>: View {
    @ObservedObject var chrome: AgentWindowChrome
    /// "terminal" or "browser": the parity anchors' prefix.
    let anchor: String
    let height: CGFloat
    @ViewBuilder var leading: () -> Leading

    var body: some View {
        HStack(spacing: Theme.space("m")) {
            leading()
            Spacer(minLength: Theme.space("m"))
            WindowControls(chrome: chrome, anchor: anchor)
                .fixedSize()
                .layoutPriority(1)
        }
        .padding(.horizontal, Theme.Panel.inset.pt)
        .frame(height: height)
        .frame(maxWidth: .infinity)
        .background(Theme.Panel.surface.color)
        .overlay(alignment: .bottom) {
            if !chrome.collapsed { Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair")) }
        }
        .contentShape(Rectangle())
        .gesture(
            DragGesture(minimumDistance: Theme.Rail.drag.number("min_distance"), coordinateSpace: .global)
                .onChanged { _ in chrome.onDrag(false) }
                .onEnded { _ in chrome.onDrag(true) })
        .accessibilityElement(children: .contain)
    }
}
