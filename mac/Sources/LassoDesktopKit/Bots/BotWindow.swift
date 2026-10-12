import AppKit
import Combine
import SwiftUI

/// A bot's window (PLAN 23): its chat (ChatModel, the bot chat's own routes)
/// and what the web's Bots view does for one bot: start and stop it, start
/// or resume a conversation, switch an omp bot's model. Creating, deleting
/// and configuring stay in the web app (`openWeb`).
@MainActor
public final class BotWindowModel: ObservableObject {
    public let chat: ChatModel
    public let actions: BotActions
    public let fleet: FleetModel
    @Published public private(set) var sessions: [BotSession] = []
    @Published public private(set) var jobs: Int?
    @Published public private(set) var avatar: NSImage?
    @Published public private(set) var models: [String] = []
    @Published public private(set) var busy = false
    /// Open one of the web app's pages for this lasso (`/bots/<name>/settings`…).
    public var openWeb: (String) -> Void = { _ in }
    /// The model suggestions for a runtime (agent-config's harness).
    public var suggestions: (BotView) async -> [String] = { _ in [] }
    private var subs: Set<AnyCancellable> = []

    public init(chat: ChatModel, actions: BotActions, fleet: FleetModel) {
        self.chat = chat
        self.actions = actions
        self.fleet = fleet
        // The window reads the bot's live row; the chat follows it too.
        fleet.$bots
            .sink { [weak self] bots in
                guard let self, let b = bots.first(where: { $0.name == chat.bot.name }) else { return }
                chat.update(bot: b)
                self.objectWillChange.send()
            }
            .store(in: &subs)
        chat.objectWillChange.sink { [weak self] _ in self?.objectWillChange.send() }.store(in: &subs)
    }

    public var bot: BotView { chat.bot }
    /// The bot's standing, asking too when its chat holds an open request
    /// the fleet's row has not caught up with.
    public var standing: BotStanding {
        let s = bot.standing
        return s > .asking && s != .stopped && chat.pending != nil ? .asking : s
    }
    public var running: Bool { standing != .stopped }

    /// The header's line: state, runtime, model, jobs.
    public var statusLine: String {
        [standing.label, bot.runtime, bot.runtime == "omp" ? nil : bot.modelName].compactMap { $0 }.joined(separator: " · ")
    }

    public func load() async {
        avatar = await actions.avatar(bot)
        jobs = try? await actions.jobCount(bot)
        sessions = (try? await actions.sessions(bot)) ?? []
        models = await suggestions(bot)
    }

    /// A refusal's words: lasso refuses a switch mid-turn (409), and says why.
    static func why(_ what: String, _ error: Error) -> String {
        if case .http(409, let body) = error as? LassoError {
            return "Not now: \(body.trimmingCharacters(in: .whitespacesAndNewlines))"
        }
        return "\(what) failed: \(error)"
    }

    private func run(_ what: String, _ body: @escaping () async throws -> String?) {
        guard !busy else { return }
        busy = true
        Task {
            defer { busy = false }
            do {
                let said = try await body()
                chat.say(said)
            } catch {
                chat.say(Self.why(what, error))
            }
            await chat.load()
        }
    }

    public func start() { run("Start") { [self] in try await actions.lifecycle(bot, "start"); return "Starting \(bot.name)…" } }
    public func stop() { run("Stop") { [self] in try await actions.lifecycle(bot, "stop"); return "Stopping \(bot.name)…" } }
    public func restart() { run("Restart") { [self] in try await actions.lifecycle(bot, "restart"); return "Restarting \(bot.name)…" } }

    public func newConversation() {
        run("New conversation") { [self] in
            try await actions.newSession(bot)
            sessions = (try? await actions.sessions(bot)) ?? sessions
            return "A new conversation"
        }
    }

    public func resume(_ s: BotSession) {
        guard s.current != true else { return }
        run("Resume") { [self] in
            try await actions.resume(bot, session: s.id)
            sessions = (try? await actions.sessions(bot)) ?? sessions
            return "Back to \(s.name)"
        }
    }

    /// An omp bot switches at once; a stopped one, or "the default", takes
    /// effect at its next start.
    public func setModel(_ m: String) {
        run("Model") { [self] in
            let restart = try await actions.setModel(bot, m)
            let name = m.isEmpty ? "the default model" : m
            return restart ? "\(name) takes effect after a restart" : "Now on \(name)"
        }
    }

    public func refreshSessions() {
        Task { sessions = (try? await actions.sessions(bot)) ?? sessions }
    }

    /// The conversation as the agent window folds it.
    var phase: ConversationPhase {
        if chat.chat == nil { return chat.loadError == nil ? .loading : .idle }
        if standing == .working { return bot.state == "starting" ? .starting(bot.runtime) : .working(bot.name) }
        return .idle
    }

    func web(_ tail: String) { openWeb("/bots/\(bot.name)\(tail)") }
}

/// A bot's picture, else its initial on the accent.
struct BotAvatarView: View {
    let name: String
    var image: NSImage?
    let standing: BotStanding
    var size: CGFloat = Theme.List.row.number("avatar")

    var body: some View {
        let a = Theme.avatar
        ZStack {
            if let image {
                Image(nsImage: image).resizable().scaledToFill()
            } else {
                Theme.Control.round.color("fill")
                Text(String(name.prefix(1)).uppercased())
                    .font(.system(size: size * a.number("glyph"), weight: .semibold))
                    .foregroundStyle(Theme.Text.primary.color)
            }
        }
        .frame(width: size, height: size)
        .clipShape(RoundedRectangle(cornerRadius: size * a.number("radius")))
        .overlay(alignment: .bottomTrailing) {
            Circle()
                .fill(standing == .stopped ? Color.clear : StateStyle.color(standing.state))
                .overlay(Circle().strokeBorder(standing == .stopped ? StateStyle.color(.idle) : Theme.Panel.surface.color, lineWidth: a.number("ring")))
                .frame(width: size * a.number("dot"), height: size * a.number("dot"))
                .offset(x: size * a.number("dot_offset"), y: size * a.number("dot_offset"))
        }
        .accessibilityHidden(true)
    }
}

/// The bot's choices, numbered, the selected one with Answer (as an agent
/// window's StepList).
struct BotChoices: View {
    @ObservedObject var model: ChatModel

    var body: some View {
        let opts = model.pendingOptions
        if !opts.isEmpty {
            VStack(alignment: .leading, spacing: Theme.space("2xs")) {
                ForEach(Array(opts.enumerated()), id: \.offset) { i, label in
                    let on = (model.highlighted ?? 0) == i
                    HStack(spacing: Theme.space("m")) {
                        Button { model.highlighted = i } label: {
                            HStack(spacing: Theme.space("m")) {
                                Text("\(i + 1).").foregroundStyle(Theme.Text.secondary.color)
                                Text(label).foregroundStyle(Theme.Text.primary.color).lineLimit(1)
                                Spacer(minLength: Theme.space("m"))
                            }
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel("Choose: \(label)")
                        .accessibilityAddTraits(on ? .isSelected : [])
                        if on {
                            PillButton(title: "Answer", primary: true, label: "Answer: \(label)") {
                                model.highlighted = i
                                model.takeStep()
                            }
                            .parityAnchor("bot.choices.answer")
                        }
                    }
                    .font(Theme.Fonts.body.font)
                    .padding(.vertical, Theme.space("2xs"))
                    .padding(.horizontal, Theme.space("m"))
                    .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt).fill(on ? Theme.Row.selected.color : .clear))
                }
            }
            .padding(.horizontal, Theme.Panel.inset.pt)
            .padding(.bottom, Theme.space("m"))
            .parityAnchor("bot.choices")
        }
    }
}

struct BotWindowView: View {
    @ObservedObject var model: BotWindowModel
    @ObservedObject var chat: ChatModel
    @ObservedObject var chrome: AgentWindowChrome

    init(model: BotWindowModel, chrome: AgentWindowChrome) {
        self.model = model
        chat = model.chat
        self.chrome = chrome
    }

    var body: some View {
        let w = Theme.Agent.window
        VStack(spacing: 0) {
            header
                .contentShape(Rectangle())
                .gesture(
                    DragGesture(minimumDistance: Theme.Rail.drag.number("min_distance"), coordinateSpace: .global)
                        .onChanged { _ in chrome.onDrag(false) }
                        .onEnded { _ in chrome.onDrag(true) })
                .parityAnchor("bot.header")
            if !chrome.collapsed {
                Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair"))
                if !model.running { stoppedBanner }
                AgentConversation(
                    entries: ConversationEntry.fold(chat.chat?.items.suffix(160).map { $0 } ?? [], cwd: chat.chat?.cwd),
                    pending: chat.pending, phase: model.phase, host: model.bot.host)
                BotChoices(model: chat)
                composer
                footer
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        .background(PanelBackground())
        .clipShape(RoundedRectangle(cornerRadius: w.number("radius"), style: .continuous))
        .overlay(alignment: .bottomTrailing) { if !chrome.collapsed { grip } }
        .parityAnchor(chrome.collapsed ? "bot.window.collapsed" : "bot.window")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("\(model.bot.name), bot window")
    }

    private var header: some View {
        let w = Theme.Agent.window
        let b = model.bot
        return HStack(spacing: Theme.space("l")) {
            BotAvatarView(name: b.name, image: model.avatar, standing: model.standing, size: w.number("avatar"))
            VStack(alignment: .leading, spacing: Theme.space("2xs")) {
                Text(b.name)
                    .font(Theme.Fonts.title.font)
                    .foregroundStyle(Theme.Text.primary.color)
                    .lineLimit(1)
                    .parityAnchor("bot.title")
                HStack(spacing: Theme.space("s")) {
                    Text(model.statusLine)
                    if b.runtime == "omp" { modelMenu }
                    if let n = model.jobs, n > 0 {
                        Button("· \(n) \(n == 1 ? "job" : "jobs")") { model.web("/settings") }
                            .buttonStyle(.plain)
                            .accessibilityLabel("\(n) jobs: open them in the web app")
                    }
                }
                .font(Theme.Fonts.small.font)
                .foregroundStyle(Theme.Text.secondary.color)
                .lineLimit(1)
            }
            Spacer(minLength: Theme.space("m"))
            WindowButton(symbol: chrome.collapsed ? "arrow.up.left.and.arrow.down.right" : "minus",
                         label: chrome.collapsed ? "Expand" : "Minimize") { chrome.onToggle() }
            WindowButton(symbol: "xmark", label: "Close") { chrome.onClose() }
                .parityAnchor("bot.close")
        }
        .padding(.horizontal, Theme.Panel.inset.pt)
        .frame(height: w.number("header_h"))
    }

    /// An omp bot's model, switched live (POST /api/bots/<name>/model).
    private var modelMenu: some View {
        let current = model.bot.modelName ?? ""
        return Menu {
            Button("Default") { model.setModel("") }
            ForEach(model.models.filter { $0 != current }, id: \.self) { m in Button(m) { model.setModel(m) } }
        } label: {
            Text("· \(current.isEmpty ? "default model" : current)")
        }
        .menuStyle(.borderlessButton)
        .fixedSize()
        .accessibilityLabel("Model: \(current.isEmpty ? "default" : current). Switch it")
        .parityAnchor("bot.model")
    }

    private var stoppedBanner: some View {
        HStack(spacing: Theme.space("l")) {
            Text("\(model.bot.name) isn't running.")
                .font(Theme.Fonts.body.font)
                .foregroundStyle(Theme.Text.secondary.color)
            Spacer(minLength: Theme.space("m"))
            PillButton(title: "Start \(model.bot.name)", primary: true, enabled: !model.busy, label: "Start \(model.bot.name)") {
                model.start()
            }
        }
        .padding(.horizontal, Theme.Panel.inset.pt)
        .padding(.vertical, Theme.space("l"))
        .background(alignment: .bottom) { Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair")) }
        .parityAnchor("bot.stopped")
    }

    private var composer: some View {
        VStack(alignment: .leading, spacing: Theme.space("m")) {
            if let n = chat.notice {
                Text(n)
                    .font(Theme.Fonts.small.font)
                    .foregroundStyle(Theme.Text.secondary.color)
                    .lineLimit(2)
                    .padding(.horizontal, Theme.Panel.inset.pt)
                    .parityAnchor("bot.notice")
            }
            ComposerRow(model: chat, attach: chat.attachFile)
                .padding(.horizontal, Theme.Panel.inset.pt)
                // A stopped bot's conversation is read-only until it starts.
                .disabled(!model.running)
                .opacity(model.running ? 1 : Theme.NewAgent.layout.number("disabled"))
        }
        .padding(.top, Theme.space("l"))
        .padding(.bottom, Theme.space("l"))
        .background(alignment: .top) { Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair")) }
    }

    private var footer: some View {
        HStack(spacing: Theme.space("l")) {
            Button { model.newConversation() } label: { Label("New conversation", systemImage: "arrow.clockwise") }
                .buttonStyle(.plain)
                .disabled(!model.running || model.busy)
                .accessibilityLabel("Start a new conversation with \(model.bot.name)")
            Menu {
                if model.sessions.isEmpty { Text("No other conversations") }
                ForEach(model.sessions) { s in
                    Button((s.current == true ? "✓ " : "") + s.name) { model.resume(s) }
                }
            } label: {
                Text("Conversations")
            }
            .menuStyle(.borderlessButton)
            .fixedSize()
            .disabled(!model.running)
            .accessibilityLabel("Earlier conversations: resume one")
            Spacer(minLength: Theme.space("m"))
            Button { model.running ? model.stop() : model.start() } label: {
                Label(model.running ? "Stop bot" : "Start bot", systemImage: model.running ? "stop.fill" : "play.fill")
            }
            .buttonStyle(.plain)
            .disabled(model.busy)
            .accessibilityLabel(model.running ? "Stop \(model.bot.name)" : "Start \(model.bot.name)")
            .parityAnchor("bot.lifecycle")
            Menu {
                Button("Restart") { model.restart() }.disabled(!model.running)
                Button("Jobs ↗") { model.web("/settings") }
                Button("Settings ↗") { model.web("/settings") }
                Button("Manage bots ↗") { model.openWeb("/bots/manage") }
            } label: {
                Image(systemName: "ellipsis")
            }
            .menuStyle(.borderlessButton)
            .menuIndicator(.hidden)
            .fixedSize()
            .accessibilityLabel("More: restart, jobs, settings, manage bots")
        }
        .font(Theme.Fonts.small.font)
        .foregroundStyle(Theme.Text.primary.color)
        .padding(.leading, Theme.Panel.inset.pt)
        // Clear of the resize corner.
        .padding(.trailing, Theme.Agent.window.number("button"))
        .padding(.vertical, Theme.space("l"))
        .background(alignment: .top) { Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair")) }
        .parityAnchor("bot.footer")
    }

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

/// One bot's window: an agent window's panel and behaviour (floats over
/// full-screen apps, follows across Spaces, moves by its header, resizes by
/// its corner, collapses to its header), its frame kept per bot
/// (UserDefaults `botWindowFrames`).
@MainActor
final class BotWindowController {
    let model: BotWindowModel
    let panel: AgentWindowPanel
    private let chrome = AgentWindowChrome()
    var onClosed: () -> Void = {}
    private var dragFrom: (pointer: NSPoint, frame: NSRect)?
    private var expandedFrame: NSRect

    static let framesKey = "botWindowFrames"

    init(model: BotWindowModel, near anchor: NSRect, media: MediaSource?) {
        self.model = model
        let size = Theme.Agent.window.size2("size")
        let vis = NSScreen.main?.visibleFrame ?? .zero
        var frame = NSRect(
            x: anchor.minX - Theme.Agent.preview.number("gap") - size.width, y: anchor.maxY - size.height,
            width: size.width, height: size.height)
        if let saved = (UserDefaults.standard.dictionary(forKey: Self.framesKey)?[model.bot.name] as? String).map(NSRectFromString),
           saved.width > 0, vis.intersects(saved)
        {
            frame = saved
        }
        frame.origin.x = max(vis.minX, min(frame.origin.x, vis.maxX - frame.width))
        frame.origin.y = max(vis.minY, min(frame.origin.y, vis.maxY - frame.height))
        expandedFrame = frame
        let host = NSHostingView(rootView: BotWindowView(model: model, chrome: chrome).environment(\.mediaSource, media))
        host.frame = NSRect(origin: .zero, size: frame.size)
        panel = AgentWindowPanel(content: host, frame: frame)
        panel.onKey = { [weak self] e in self?.handle(e) ?? false }
        chrome.onToggle = { [weak self] in self?.toggle() }
        chrome.onClose = { [weak self] in self?.close() }
        chrome.onDrag = { [weak self] ended in self?.drag(ended, resize: false) }
        chrome.onResize = { [weak self] ended in self?.drag(ended, resize: true) }
    }

    func show() {
        model.chat.startPolling()
        panel.makeKeyAndOrderFront(nil)
        Task { await model.load() }
    }

    func close() {
        model.chat.stopPolling()
        remember()
        panel.orderOut(nil)
        onClosed()
    }

    private func toggle() {
        let f = panel.frame
        if chrome.collapsed {
            chrome.collapsed = false
            panel.setFrame(
                NSRect(x: f.minX, y: f.maxY - expandedFrame.height, width: expandedFrame.width, height: expandedFrame.height),
                display: true, animate: false)
        } else {
            expandedFrame = f
            let c = Theme.Agent.collapsed.size2("size")
            chrome.collapsed = true
            panel.setFrame(NSRect(x: f.minX, y: f.maxY - c.height, width: c.width, height: c.height), display: true, animate: false)
        }
    }

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
        frames[model.bot.name] = NSStringFromRect(chrome.collapsed ? expandedFrame : panel.frame)
        UserDefaults.standard.set(frames, forKey: Self.framesKey)
    }

    /// The chat's keys (CardKeys.botWindowSet): 1–9 and → answer, ↳ edits
    /// a choice, ⏎ and L to the composer (or send), U attaches, ⌘. stops.
    func handle(_ e: NSEvent) -> Bool {
        let m = model.chat
        switch CardKeys.route(e, editing: m.composerHasKeys || panel.editingText, among: CardKeys.botWindowSet) {
        case .stop?:
            guard m.isWorking else { return false }
            m.stop()
        case .digit(let n)?: m.highlight(n)
        case .attach?: m.attachFile()
        case .send?: if m.composer.isEmpty { m.focusComposer() } else { m.send() }
        case .reply?: m.focusComposer()
        case .editStep?: m.editStep()
        case .takeStep?: m.takeStep()
        default: return false
        }
        return true
    }
}
