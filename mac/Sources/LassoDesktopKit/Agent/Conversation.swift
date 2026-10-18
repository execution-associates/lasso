import AppKit
import Combine
import SwiftUI

/// What an agent's or a bot's window and hover preview show and do (PLAN
/// 19, 23). One window, one preview and one composer draw either: an agent's
/// `CardModel` and a bot's `BotWindowModel` conform, and what only one of
/// them has comes in through the slots at the end (a bot's picture, its
/// stopped banner and its footer of model, conversations and lifecycle; an
/// agent's rename-in-place title, its folder and model and its Kill).
@MainActor
public protocol Conversation: ComposerModel {
    associatedtype Avatar: View
    associatedtype Title: View
    associatedtype Above: View
    associatedtype Choices: View
    associatedtype Footer: View

    /// "agent" or "bot": the parity anchors' prefix (`agent.header`, `bot.close`…).
    var anchor: String { get }
    /// What to call it, as its header does.
    var title: String { get }
    /// The header's second line.
    var statusLine: String { get }
    /// Its dot's state.
    var dotState: AgentState { get }

    /// The transcript, its folder (for relative image paths) and the
    /// machine whose files it names.
    var items: [ChatItem] { get }
    var cwd: String? { get }
    var mediaHost: String { get }
    /// Everything read of the conversation: whether there is more above,
    /// whether it is on its way, whether a preview shows past its latest turn.
    var history: ChatHistory { get }
    /// The reader reached the top: read the page above.
    func loadOlder()
    /// A preview's reader scrolled above its latest turn: show what came before.
    func revealEarlier()
    /// An omp agent's or bot's open request.
    var pendingUI: ChatPayload.PendingUI? { get }
    var phase: ConversationPhase { get }
    /// Your message on its way, shown at once.
    var outgoing: String? { get }
    /// One line of feedback over the composer.
    var notice: String? { get }
    /// The composer takes a message (a stopped bot's does not).
    var canCompose: Bool { get }
    /// Something lasso is waiting on that dims the conversation (a kill).
    var waiting: String? { get }

    /// The header's terminal button: herdr's window focused on its pane.
    var canShowInHerdr: Bool { get }
    /// The button's label: what it does, or why it cannot.
    var herdrLabel: String { get }
    func showInHerdr()

    var hasDraft: Bool { get }
    /// An open question its keys answer (1–9, ⏎): its window opens on the
    /// question, not in the composer.
    var asksQuestion: Bool { get }
    var composerHasKeys: Bool { get }
    func focusComposer()
    func attachFile()
    func startPolling()
    func stopPolling()
    /// Do what a routed key asks; false hands it back to the system.
    @discardableResult func perform(_ action: CardKeyAction) -> Bool
    /// A kill went through (agents), or anything else that ends it: its window closes.
    var onClose: () -> Void { get set }
    /// A rename in progress, kept when its preview goes.
    func settle()

    /// The keys its window and its clicked hover preview route (CardKeys).
    static var windowKeys: [KeyBinding] { get }
    static var previewKeys: [KeyBinding] { get }
    /// Where its windows' frames are kept (UserDefaults), and its key there.
    static var framesKey: String { get }
    var windowKey: String { get }

    @ViewBuilder func avatarView(size: CGFloat) -> Avatar
    @ViewBuilder func titleView() -> Title
    /// Between the header and the conversation.
    @ViewBuilder func aboveView() -> Above
    /// Under the conversation: the open question's choices.
    @ViewBuilder func choicesView() -> Choices
    /// Under the composer; `window` false in a hover preview.
    @ViewBuilder func footerView(window: Bool) -> Footer
}

/// The header: avatar, title, status line, and whatever the surface puts at
/// the end (a window's buttons, a preview's state dot). The trailing end is
/// laid out first, so a long title or status line truncates and never
/// pushes the window's buttons out of it.
struct ConversationHeader<M: Conversation, Trailing: View>: View {
    @ObservedObject var model: M
    @ViewBuilder var trailing: () -> Trailing

    var body: some View {
        let w = Theme.Agent.window
        HStack(spacing: Theme.space("l")) {
            model.avatarView(size: w.number("avatar"))
            VStack(alignment: .leading, spacing: Theme.space("2xs")) {
                model.titleView()
                Text(model.statusLine)
                    .font(Theme.Fonts.small.font)
                    .foregroundStyle(Theme.Text.secondary.color)
                    .lineLimit(1)
                    .truncationMode(.tail)
            }
            Spacer(minLength: Theme.space("m"))
            trailing()
                .fixedSize()
                .layoutPriority(1)
        }
        .padding(.horizontal, Theme.Panel.inset.pt)
        .frame(height: w.number("header_h"))
        .accessibilityElement(children: .contain)
    }
}

/// The conversation as a window or a preview shows it: everything read in
/// the window, the latest turn in a preview. Scrolling to the top reads
/// further back: a preview first shows what came before its latest turn,
/// then, like a window, the page above.
struct ConversationBody<M: Conversation>: View {
    @ObservedObject var model: M
    let window: Bool

    var body: some View {
        let items = model.items
        let start = window || model.history.earlier ? 0 : ConversationEntry.turnStart(items)
        let entries = ConversationEntry.fold(Array(items[start...]), cwd: model.cwd)
        let back = ScrollBack(model, more: start > 0 || model.history.hasMore, fill: window) { [model] in
            if start > 0 { model.revealEarlier() } else { model.loadOlder() }
        }
        AgentConversation(
            entries: entries, pending: model.pendingUI, phase: model.phase, outgoing: model.outgoing, host: model.mediaHost,
            back: back
        )
        .opacity(model.waiting != nil ? Theme.Card.killing.number("dim") : 1)
        .overlay(alignment: .bottomLeading) {
            if let w = model.waiting { WaitingRow(text: w).padding(Theme.Panel.inner.number("pad")) }
        }
    }
}

/// The notice, the composer and the footer at the bottom of a window or preview.
struct ConversationComposer<M: Conversation>: View {
    @ObservedObject var model: M
    let window: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.space("m")) {
            if let n = model.notice {
                Text(n)
                    .font(Theme.Fonts.small.font)
                    .foregroundStyle(Theme.Text.secondary.color)
                    .lineLimit(window ? 2 : 1)
                    .padding(.horizontal, Theme.Panel.inset.pt)
                    .parityAnchor("\(model.anchor).notice")
            }
            ComposerRow(model: model) { model.attachFile() }
                .padding(.horizontal, Theme.Panel.inset.pt)
                .disabled(!model.canCompose)
                .opacity(model.canCompose ? 1 : Theme.NewAgent.layout.number("disabled"))
            model.footerView(window: window)
        }
        .padding(.top, Theme.space("l"))
        .padding(.bottom, Theme.space("2xl"))
        .background(alignment: .top) { Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair")) }
    }
}

/// An agent's or a bot's own window: header (avatar, title, status, the
/// terminal, minimize and close), the conversation, the choices, the
/// composer and the footer.
struct ConversationWindowView<M: Conversation>: View {
    @ObservedObject var model: M
    @ObservedObject var chrome: AgentWindowChrome

    var body: some View {
        let w = Theme.Agent.window
        let a = model.anchor
        VStack(spacing: 0) {
            ConversationHeader(model: model) { buttons }
                .contentShape(Rectangle())
                .gesture(
                    DragGesture(minimumDistance: Theme.Rail.drag.number("min_distance"), coordinateSpace: .global)
                        .onChanged { _ in chrome.onDrag(false) }
                        .onEnded { _ in chrome.onDrag(true) })
                .parityAnchor("\(a).header")
            if !chrome.collapsed {
                Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair"))
                model.aboveView()
                ConversationBody(model: model, window: true)
                model.choicesView()
                    .opacity(model.waiting != nil ? Theme.Card.killing.number("dim") : 1)
                ConversationComposer(model: model, window: true)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        .background(PanelBackground())
        .clipShape(RoundedRectangle(cornerRadius: w.number("radius"), style: .continuous))
        .overlay(alignment: .bottomTrailing) { if !chrome.collapsed { WindowGrip(chrome: chrome) } }
        .parityAnchor(chrome.collapsed ? "\(a).window.collapsed" : "\(a).window")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("\(model.title), \(a) window")
    }

    private var buttons: some View {
        let a = model.anchor
        return HStack(spacing: Theme.space("xs")) {
            WindowButton(glyph: .lucide(.squareTerminal), label: model.herdrLabel, enabled: model.canShowInHerdr) { model.showInHerdr() }
                .parityAnchor("\(a).herdr")
            WindowControls(chrome: chrome, anchor: a)
        }
    }
}

/// The hover preview: resting on a row of the agents or the Bots column
/// shows its latest turn beside the column. It stays put (a click on the row
/// opens the window to move around), and it answers like a window once it is
/// clicked: the composer takes typing, and the choices their answer.
struct ConversationPreviewView<M: Conversation>: View {
    @ObservedObject var model: M

    var body: some View {
        VStack(spacing: 0) {
            ConversationHeader(model: model) {
                // The window's terminal button, so a preview reaches the pane
                // without opening the window first.
                HStack(spacing: Theme.space("m")) {
                    WindowButton(glyph: .lucide(.squareTerminal), label: model.herdrLabel, enabled: model.canShowInHerdr) {
                        model.showInHerdr()
                    }
                    .parityAnchor("preview.herdr")
                    Circle()
                        .fill(StateStyle.color(model.dotState))
                        .frame(width: Theme.space("l"), height: Theme.space("l"))
                        .parityAnchor("preview.state")
                        .accessibilityLabel(model.dotState.label)
                }
            }
            model.aboveView()
            ConversationBody(model: model, window: false)
            model.choicesView()
            ConversationComposer(model: model, window: false)
        }
        .panelChrome(Theme.Agent.preview.size2("size"), anchor: model.anchor == "agent" ? "preview" : "bots.preview")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("\(model.title), preview")
    }
}

extension ConversationEntry {
    /// The other side of the latest turn: everything after the last message
    /// you sent, folded. A background task's notice is not one of yours.
    static func latestTurn(_ items: [ChatItem], cwd: String? = nil) -> [ConversationEntry] {
        fold(Array(items[turnStart(items)...]), cwd: cwd)
    }

    /// Where the latest turn starts: after the last message you sent, or at
    /// the last row when nothing follows it.
    static func turnStart(_ items: [ChatItem]) -> Int {
        let start = items.lastIndex { $0.kind == "user" && TaskNotification.parse($0.text ?? "") == nil }.map { $0 + 1 } ?? 0
        return start < items.count ? start : max(0, items.count - 1)
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
    var onDefaultSize: () -> Void = {}
}

/// A window's minimize (expand, once collapsed) and close, as its header
/// ends: an agent's or a bot's window, the herdr window and the Browser
/// window alike (`<anchor>.minimize`, `<anchor>.close`).
struct WindowControls: View {
    @ObservedObject var chrome: AgentWindowChrome
    let anchor: String

    var body: some View {
        HStack(spacing: Theme.space("xs")) {
            WindowButton(glyph: .window(chrome.collapsed ? .expand : .minimize),
                         label: chrome.collapsed ? "Expand" : "Minimize") { chrome.onToggle() }
                .parityAnchor("\(anchor).minimize")
            WindowButton(glyph: .window(.close), label: "Close") { chrome.onClose() }
                .parityAnchor("\(anchor).close")
        }
    }
}

/// The corner a window resizes by; a double-click puts the default size back.
struct WindowGrip: View {
    @ObservedObject var chrome: AgentWindowChrome

    var body: some View {
        WindowControl.resize.image(size: Theme.icon.number("control"), weight: .regular)
            .foregroundStyle(Theme.Text.secondary.color)
            .frame(width: Theme.Agent.window.number("button"), height: Theme.Agent.window.number("button"))
            .contentShape(Rectangle())
            .gesture(
                DragGesture(minimumDistance: 0, coordinateSpace: .global)
                    .onChanged { _ in chrome.onResize(false) }
                    .onEnded { _ in chrome.onResize(true) })
            .simultaneousGesture(TapGesture(count: 2).onEnded { chrome.onDefaultSize() })
            .accessibilityElement()
            .accessibilityLabel("Resize")
            .accessibilityHint("Drag to resize; double-click for the default size")
            .accessibilityAction(named: "Default size") { chrome.onDefaultSize() }
            .help("Drag to resize; double-click for the default size")
    }
}

/// A plain glyph button in the window's header (no circle, as the
/// reference). One that cannot act now is drawn dimmed and says why.
struct WindowButton: View {
    let glyph: Glyph
    let label: String
    var enabled = true
    let action: () -> Void

    var body: some View {
        let w = Theme.Agent.window
        Button {
            if enabled { action() }
        } label: {
            glyph.view(size: w.number("icon"), weight: .medium)
                .foregroundStyle(enabled ? Theme.Text.primary.color : Theme.Text.placeholder.color)
                .frame(width: w.number("button"), height: w.number("button"))
                .contentShape(Rectangle())
        }
        .buttonStyle(RailPress())
        .accessibilityLabel(label)
        .accessibilityAddTraits(enabled ? [] : .isStaticText)
        .help(label)
    }
}

/// An agent's or a bot's window panel, and the herdr window's and the
/// Browser window's (PageWindow): it floats over every app (full-screen ones
/// included) and follows the user across Spaces. ⌘W closes it, menu bar or
/// not.
final class AgentWindowPanel: RailPanel {
    var onCloseKey: () -> Void = {}

    init(content: NSView, frame: NSRect) {
        super.init(content: content, size: frame.size)
        setFrame(frame, display: false)
        level = .floating
        collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary, .ignoresCycle]
        hasShadow = true
    }

    override func replacement() -> RailPanel { AgentWindowPanel(content: NSView(), frame: frame) }

    override func handOver(to p: RailPanel) {
        super.handOver(to: p)
        (p as? AgentWindowPanel)?.onCloseKey = onCloseKey
    }

    /// ⌘W, whatever has the keyboard inside (the composer included).
    static func isCloseKey(_ e: NSEvent) -> Bool {
        e.type == .keyDown && e.modifierFlags.intersection([.command, .shift, .option, .control]) == .command
            && e.charactersIgnoringModifiers?.lowercased() == "w"
    }

    override func performKeyEquivalent(with e: NSEvent) -> Bool {
        if Self.isCloseKey(e) {
            onCloseKey()
            return true
        }
        return super.performKeyEquivalent(with: e)
    }

    override func sendEvent(_ event: NSEvent) {
        // A borderless panel has no close button for AppKit's own ⌘W, and
        // an accessory app no Window menu: take it before anything else.
        if Self.isCloseKey(event) {
            onCloseKey()
            return
        }
        super.sendEvent(event)
    }
}

/// One agent's or bot's window: open, move, resize, collapse, close. Where
/// it was is remembered per agent or bot (UserDefaults `agentWindowFrames`,
/// `botWindowFrames`).
@MainActor
final class ConversationWindowController<M: Conversation> {
    let model: M
    private(set) var panel: AgentWindowPanel
    private let chrome = AgentWindowChrome()
    var onClosed: () -> Void = {}
    private var dragFrom: (pointer: NSPoint, frame: NSRect)?
    /// The full size to come back to from collapsed.
    private var expandedFrame: NSRect

    init(model: M, near anchor: NSRect, media: MediaSource?) {
        self.model = model
        let size = Theme.Agent.window.size2("size")
        var vis = Screens.visibleFrame(for: anchor)
        var frame = NSRect(
            x: anchor.minX - Theme.Agent.preview.number("gap") - size.width, y: anchor.maxY - size.height,
            width: size.width, height: size.height)
        // Where it was left, on whichever screen that is, while that screen is there.
        if let saved = (UserDefaults.standard.dictionary(forKey: M.framesKey)?[model.windowKey] as? String).map(NSRectFromString),
           saved.width > 0, NSScreen.screens.contains(where: { $0.visibleFrame.intersects(saved) })
        {
            frame = saved
            vis = Screens.visibleFrame(for: saved)
        }
        frame.origin.x = max(vis.minX, min(frame.origin.x, vis.maxX - frame.width))
        frame.origin.y = max(vis.minY, min(frame.origin.y, vis.maxY - frame.height))
        expandedFrame = frame
        let host = NSHostingView(rootView: ConversationWindowView(model: model, chrome: chrome).environment(\.mediaSource, media))
        host.frame = NSRect(origin: .zero, size: frame.size)
        panel = AgentWindowPanel(content: host, frame: frame)
        panel.onKey = { [weak self] e in self?.handle(e) ?? false }
        panel.onCloseKey = { [weak self] in self?.close() }
        // A kill that went through closes the window (CardModel.resolved).
        self.model.onClose = { [weak self] in self?.close() }
        chrome.onToggle = { [weak self] in self?.toggle() }
        chrome.onClose = { [weak self] in self?.close() }
        chrome.onDrag = { [weak self] ended in self?.drag(ended, resize: false) }
        chrome.onResize = { [weak self] ended in self?.drag(ended, resize: true) }
        chrome.onDefaultSize = { [weak self] in self?.defaultSize() }
    }

    var collapsed: Bool { chrome.collapsed }

    /// The grip's double-click: back to the size a fresh window opens at
    /// (`agent.window.size`), its top-left kept as a resize keeps it, and
    /// kept on its screen. The remembered frame is forgotten, so the next
    /// open is the default too. `animate` false in tests.
    func defaultSize(animate: Bool = true) {
        guard !chrome.collapsed else { return }
        let size = Theme.Agent.window.size2("size")
        let f = panel.frame
        let vis = Screens.visibleFrame(for: f)
        var r = NSRect(x: f.minX, y: f.maxY - size.height, width: size.width, height: size.height)
        r.origin.x = max(vis.minX, min(r.minX, vis.maxX - r.width))
        r.origin.y = max(vis.minY, min(r.minY, vis.maxY - r.height))
        expandedFrame = r
        panel.setFrame(r, display: true, animate: animate)
        var frames = UserDefaults.standard.dictionary(forKey: M.framesKey) ?? [:]
        frames[model.windowKey] = nil
        UserDefaults.standard.set(frames, forKey: M.framesKey)
        forgotten = true
    }

    /// The frame was forgotten (`defaultSize`) and not moved since: a close
    /// remembers nothing, so the next open is a fresh window's.
    private var forgotten = false
    private var dragMoved = false

    /// Bring it forward with the keyboard and the cursor in the composer,
    /// as L does, so typing starts at once (a new agent's too, while it is
    /// still starting). An open question keeps the keys for answering it,
    /// and a composer that takes no message (a stopped bot's) gets no cursor.
    func show() {
        model.startPolling()
        panel.orderIn(takeKey: true) { if let p = $0 as? AgentWindowPanel { panel = p } }
        if model.canCompose, !model.asksQuestion { model.focusComposer() }
    }

    func close() {
        model.settle()
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
            // A real move or resize is remembered again; a click (a
            // double-click's) is not.
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
            let min = Theme.Agent.window.size2("min")
            let width = max(min.width, from.frame.width + dx)
            let height = max(min.height, from.frame.height - dy)
            panel.setFrame(NSRect(x: from.frame.minX, y: from.frame.maxY - height, width: width, height: height), display: true)
        } else {
            panel.setFrameOrigin(NSPoint(x: from.frame.minX + dx, y: from.frame.minY + dy))
        }
    }

    private func remember() {
        guard !forgotten else { return }
        var frames = UserDefaults.standard.dictionary(forKey: M.framesKey) ?? [:]
        frames[model.windowKey] = NSStringFromRect(chrome.collapsed ? expandedFrame : panel.frame)
        UserDefaults.standard.set(frames, forKey: M.framesKey)
    }

    /// Its keys (`M.windowKeys`): 1–9 highlight a choice, → takes it, ↳
    /// edits it, ⏎ to the composer (or send), L to the composer, U attaches,
    /// T shows it in herdr, ⌘. stops; an agent's R renames and X X kills.
    func handle(_ e: NSEvent) -> Bool {
        guard let a = CardKeys.route(e, editing: model.composerHasKeys || panel.editingText, among: M.windowKeys)
        else { return false }
        return model.perform(a)
    }
}

typealias AgentWindowController = ConversationWindowController<CardModel>
typealias BotWindowController = ConversationWindowController<BotWindowModel>

@MainActor
final class PreviewHolder<M: Conversation>: ObservableObject {
    @Published var model: M?
}

struct PreviewHost<M: Conversation>: View {
    @ObservedObject var presence: CardPresence
    @ObservedObject var holder: PreviewHolder<M>
    let media: MediaSource?

    var body: some View {
        Group { if let m = holder.model { ConversationPreviewView(model: m).environment(\.mediaSource, media) } }
            .opacity(presence.shown ? 1 : 0)
            .offset(x: presence.shown ? 0 : Theme.Motion.cardOpen.number("slide"))
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }
}

/// The hover preview's panel, left of its column with its top on the hovered
/// row: shown without the keyboard (the pointer brought it, not a click),
/// and the user's once clicked, as a window is. One for the agents column
/// (`AgentPreviewController`), one for the Bots column.
@MainActor
final class ConversationPreviewController<M: Conversation> {
    private(set) var panel: RailPanel
    private let presence = CardPresence()
    private let holder = PreviewHolder<M>()
    /// The model for a row's key, nil when the row is gone.
    var make: (String) -> M? = { _ in nil }
    /// Unsent replies by row, kept when the preview goes or moves on.
    private var drafts: [String: ComposerDraft] = [:]

    init(media: MediaSource?) {
        let size = Theme.Agent.preview.size2("size")
        let host = NSHostingView(rootView: PreviewHost(presence: presence, holder: holder, media: media))
        host.frame = NSRect(origin: .zero, size: size)
        panel = RailPanel(content: host, size: size)
        panel.hasShadow = true
        panel.onKey = { [weak self] e in self?.handle(e) ?? false }
    }

    var isOpen: Bool { presence.shown }
    /// The row the preview shows, while it is open.
    private(set) var key: String?
    var showing: String? { isOpen ? key : nil }
    var model: M? { holder.model }

    /// Show `key`'s preview beside `column`, top level with `row` (screen rects).
    func show(_ key: String, beside column: NSRect, row: NSRect) {
        if self.key != key || holder.model == nil {
            keepDraft()
            holder.model?.stopPolling()
            guard let m = make(key) else { return }
            m.draft = drafts[key]
            holder.model = m
            self.key = key
        }
        // Back from a close still fading out: its polling stopped with it.
        holder.model?.startPolling()
        let size = Theme.Agent.preview.size2("size")
        let vis = Screens.visibleFrame(for: column)
        let x = max(vis.minX, column.minX - Theme.Agent.preview.number("gap") - size.width)
        let y = min(max(vis.minY, row.maxY - size.height), vis.maxY - size.height)
        panel.setFrameOrigin(NSPoint(x: x.rounded(), y: y.rounded()))
        // In from hidden: the content is in place first and the window
        // fades. A SwiftUI animation started as the window is ordered in
        // stalls on its first frame (opacity 0) until something else
        // redraws it, which left the first row hovered with an empty preview.
        let fading = !presence.shown || !panel.isVisible
        presence.shown = true
        if fading { panel.alphaValue = 0 }
        panel.orderIn(takeKey: false) { panel = $0 }
        if fading {
            let panel = panel
            NSAnimationContext.runAnimationGroup { c in
                c.duration = Theme.Motion.cardOpen.value["duration"]?.double ?? 0
                c.timingFunction = CAMediaTimingFunction(name: .easeOut)
                panel.animator().alphaValue = 1
            }
        }
    }

    func close() {
        guard presence.shown else { return }
        holder.model?.settle()
        keepDraft()
        if panel.isKeyWindow { panel.resignKey() }
        holder.model?.stopPolling()
        withAnimationToken(Theme.Motion.cardClose) { presence.shown = false }
        let delay = Theme.Motion.cardClose.value["duration"]?.double ?? 0
        Task { @MainActor [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(delay * 1e9) + 20_000_000)
            guard let self, !self.presence.shown else { return }
            self.panel.orderOut(nil)
            self.holder.model = nil
            self.key = nil
        }
    }

    private func keepDraft() {
        guard let m = holder.model, let key else { return }
        drafts[key] = m.draft
    }

    /// Its keys (`M.previewKeys`) once a click made it the user's: a
    /// window's, minus Kill, and esc closes it.
    func handle(_ e: NSEvent) -> Bool {
        guard let m = holder.model,
              let a = CardKeys.route(e, editing: m.composerHasKeys || panel.editingText, among: M.previewKeys)
        else { return false }
        if a == .close {
            close()
            return true
        }
        return m.perform(a)
    }
}

typealias AgentPreviewController = ConversationPreviewController<CardModel>
typealias BotPreviewController = ConversationPreviewController<BotWindowModel>
