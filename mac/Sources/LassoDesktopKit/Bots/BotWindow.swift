import AppKit
import Combine
import SwiftUI

/// A bot in its window and its hover preview (PLAN 23), the shared
/// Conversation an agent's are drawn by: its chat (ChatModel, the bot chat's
/// own routes) and what the web's Bots view does for one bot: start and stop
/// it, start or resume a conversation, switch an omp bot's model, show its
/// pane in herdr's window. Creating, deleting and configuring stay in the web
/// app (`openWeb`).
@MainActor
public final class BotWindowModel: ObservableObject {
    public let chat: ChatModel
    public let actions: BotActions
    public let fleet: FleetModel
    @Published public private(set) var sessions: [BotSession] = []
    @Published public private(set) var jobs: Int?
    /// Its picture: the column's copy at first (`BotAvatars`), then its own load.
    @Published public internal(set) var avatar: NSImage?
    @Published public private(set) var models: [String] = []
    @Published public private(set) var busy = false
    /// Open one of the web app's pages for this lasso (`/bots/<name>/settings`…).
    public var openWeb: (String) -> Void = { _ in }
    /// The model suggestions for a runtime (agent-config's harness).
    public var suggestions: (BotView) async -> [String] = { _ in [] }
    /// Show a pane in herdr's window (PLAN 22); nil where there is none.
    public var onShowInHerdr: ((HostPane) -> Void)?
    public var onClose: () -> Void = {}
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
        // Its pane (the terminal button) comes and goes with the fleet's.
        fleet.$panes.sink { [weak self] _ in self?.objectWillChange.send() }.store(in: &subs)
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

    /// The header's line, as an agent's: state, harness, jobs.
    public var statusLine: String {
        let jobs = jobs.flatMap { $0 > 0 ? "\($0) \($0 == 1 ? "job" : "jobs")" : nil }
        return [standing.label, "\(harnessName(bot.runtime)) bot", jobs].compactMap { $0 }.joined(separator: " · ")
    }

    /// The herdr pane the bot runs in (an omp bot's `mise run` and a claude
    /// bot's alike), while it runs: the fleet's row for it (lasso's
    /// `pane_id`, else the pane on its host carrying its agent name), else
    /// one made from lasso's `pane_id`.
    public var pane: HostPane? {
        guard running else { return nil }
        let id = bot.pane_id ?? ""
        if let p = fleet.panes.first(where: { $0.host == bot.host && !id.isEmpty && $0.pane_id == id }) { return p }
        if let p = fleet.panes.first(where: { $0.host == bot.host && $0.agent_label == bot.name }) { return p }
        guard !id.isEmpty else { return nil }
        return HostPane(
            host: bot.host, host_label: bot.host, pane_id: id, workspace_id: "", workspace_label: bot.name, tab_id: "",
            tab_label: bot.name, cwd: bot.dir_path ?? "", agent: bot.runtime, agent_status: bot.state, has_agent: true,
            focused: false)
    }

    public var canShowInHerdr: Bool { onShowInHerdr != nil && pane != nil }

    public var herdrLabel: String {
        if canShowInHerdr { return "Show \(bot.name) in herdr" }
        if !running { return "\(bot.name) isn't running: start it to see its terminal" }
        return onShowInHerdr == nil ? "No herdr window for this lasso" : "\(bot.name) has no pane yet"
    }

    /// The header's terminal button, or T: herdr's window on the bot's pane.
    public func showInHerdr() {
        guard let p = pane, let show = onShowInHerdr else { return }
        show(p)
    }

    public func load() async {
        if let a = await actions.avatar(bot) { avatar = a }
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

    public func startBot() { run("Start") { [self] in try await actions.lifecycle(bot, "start"); return "Starting \(bot.name)…" } }
    public func stopBot() { run("Stop") { [self] in try await actions.lifecycle(bot, "stop"); return "Stopping \(bot.name)…" } }
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
    public var phase: ConversationPhase {
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

/// A bot in the shared window and preview (Conversation.swift). The composer
/// is its chat's; its header is its picture and name; a stopped bot shows
/// Start above the conversation; its footer is its model, its conversations,
/// Start or Stop and ⋯.
extension BotWindowModel: Conversation {
    public var anchor: String { "bot" }
    public var title: String { bot.name }
    public var dotState: AgentState { standing.state }
    public var items: [ChatItem] { chat.chat?.items ?? [] }
    public var cwd: String? { chat.chat?.cwd }
    public var history: ChatHistory { chat.history }
    public func loadOlder() { chat.loadOlder() }
    public func revealEarlier() { chat.revealEarlier() }
    public var mediaHost: String { chat.chat?.served_by ?? chat.chat?.host ?? bot.host }
    public var pendingUI: ChatPayload.PendingUI? { chat.pending }
    public var outgoing: String? { nil }
    public var notice: String? { chat.notice }
    /// A stopped bot's conversation is read-only until it starts.
    public var canCompose: Bool { running }
    public var waiting: String? { nil }

    // The composer is the chat's.
    public var composer: String {
        get { chat.composer }
        set { chat.composer = newValue }
    }
    public var attachments: [ComposerAttachment] {
        get { chat.attachments }
        set { chat.attachments = newValue }
    }
    public var composerFocused: Bool {
        get { chat.composerFocused }
        set { chat.composerFocused = newValue }
    }
    public var focusRequest: Int { chat.focusRequest }
    public var composerPlaceholder: String { chat.composerPlaceholder }
    public var showsStop: Bool { chat.showsStop }
    public var stopping: Bool { chat.stopping }
    public var stopLabel: String { chat.stopLabel }
    public var hasDraft: Bool { !chat.composerIsEmpty }
    public var composerHasKeys: Bool { chat.composerHasKeys }
    public var asksQuestion: Bool { !chat.pendingOptions.isEmpty }
    public func send() { chat.send() }
    public func stop() { chat.stop() }
    public func focusComposer() { chat.focusComposer() }
    public func attachFile() { chat.attachFile() }
    public func attach(_ files: [URL], at caret: Int?) { chat.attach(files, at: caret) }
    public func say(_ s: String?) { chat.say(s) }

    /// The chat polls; the picture, jobs, conversations and models load once.
    public func startPolling() {
        chat.startPolling()
        Task { await load() }
    }

    public func stopPolling() { chat.stopPolling() }
    public func settle() {}

    /// The keys live now (the parity harness checks each is routed).
    public var activeBindings: Set<KeyBinding> {
        var b = chat.activeBindings
        if canShowInHerdr { b.insert(Keys.herdr) }
        return b
    }

    @discardableResult
    public func perform(_ action: CardKeyAction) -> Bool {
        let m = chat
        switch action {
        case .stop:
            guard m.isWorking else { return false }
            m.stop()
        case .digit(let n): m.highlight(n)
        case .attach: m.attachFile()
        case .send: if m.composerIsEmpty { m.focusComposer() } else { m.send() }
        case .reply: m.focusComposer()
        case .editStep: m.editStep()
        case .takeStep: m.takeStep()
        case .herdr:
            guard canShowInHerdr else { return false }
            showInHerdr()
        default: return false
        }
        return true
    }

    public static var windowKeys: [KeyBinding] { CardKeys.botWindowSet }
    public static var previewKeys: [KeyBinding] { CardKeys.botPreviewSet }
    public static var framesKey: String { "botWindowFrames" }
    public var windowKey: String { bot.name }

    public func avatarView(size: CGFloat) -> some View {
        BotAvatarView(name: bot.name, image: avatar, standing: standing, size: size)
    }

    public func titleView() -> some View {
        Text(bot.name)
            .font(Theme.Fonts.title.font)
            .foregroundStyle(Theme.Text.primary.color)
            .lineLimit(1)
            .parityAnchor("bot.title")
    }

    public func aboveView() -> some View { BotStoppedBanner(model: self) }

    public func choicesView() -> some View { BotChoices(model: chat) }

    public func footerView(window: Bool) -> some View { BotFooter(model: self, window: window) }
}

/// "<name> isn't running." with Start, over a stopped bot's last conversation.
struct BotStoppedBanner: View {
    @ObservedObject var model: BotWindowModel

    var body: some View {
        if !model.running {
            HStack(spacing: Theme.space("l")) {
                Text("\(model.bot.name) isn't running.")
                    .font(Theme.Fonts.body.font)
                    .foregroundStyle(Theme.Text.secondary.color)
                Spacer(minLength: Theme.space("m"))
                PillButton(title: "Start \(model.bot.name)", primary: true, enabled: !model.busy, label: "Start \(model.bot.name)") {
                    model.startBot()
                }
            }
            .padding(.horizontal, Theme.Panel.inset.pt)
            .padding(.vertical, Theme.space("l"))
            .background(alignment: .bottom) { Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair")) }
            .parityAnchor("bot.stopped")
        }
    }
}

/// Under a bot's composer, in an agent footer's place and style. A window:
/// its model (an omp bot's switches live), its conversations (a new one, or
/// resume one), Start or Stop, and ⋯ (Restart, Jobs ↗, Settings ↗, Manage
/// bots ↗). A preview: its folder and model, as an agent's.
struct BotFooter: View {
    @ObservedObject var model: BotWindowModel
    let window: Bool

    var body: some View {
        let b = model.bot
        HStack(spacing: Theme.space("l")) {
            if window {
                modelControl
                    .layoutPriority(-1)
                Spacer(minLength: Theme.space("m"))
                Group {
                    conversations
                    Button { model.running ? model.stopBot() : model.startBot() } label: {
                        Label(model.running ? "Stop bot" : "Start bot", lucide: model.running ? .square : .play, size: Theme.Fonts.small.number("size"))
                    }
                    .buttonStyle(.plain)
                    .disabled(model.busy)
                    .accessibilityLabel(model.running ? "Stop \(b.name)" : "Start \(b.name)")
                    .parityAnchor("bot.lifecycle")
                    more
                }
                .fixedSize()
            } else {
                let dir = b.dir_path.map { ($0 as NSString).lastPathComponent } ?? ""
                if !dir.isEmpty { Label(dir, lucide: .folder, size: Theme.Fonts.small.number("size")).lineLimit(1) }
                if let m = modelName(b.modelName) {
                    if !dir.isEmpty {
                        Rectangle().fill(Theme.Panel.divider.color).frame(width: Theme.space("hair"), height: Theme.space("2xl"))
                    }
                    Label(m, lucide: .cpu, size: Theme.Fonts.small.number("size")).lineLimit(1).truncationMode(.middle)
                }
                Spacer(minLength: 0)
            }
        }
        .font(Theme.Fonts.small.font)
        .foregroundStyle(Theme.Text.secondary.color)
        .padding(.horizontal, Theme.Panel.inset.pt + Theme.space("xl"))
        .parityAnchor("bot.footer")
    }

    /// An omp bot's model, switched live (POST /api/bots/<name>/model); a
    /// claude bot's, as an agent's footer shows it.
    @ViewBuilder private var modelControl: some View {
        let current = model.bot.modelName ?? ""
        let shown = modelName(current) ?? "Default model"
        if model.bot.runtime == "omp" {
            Menu {
                Button("Default") { model.setModel("") }
                ForEach(model.models.filter { $0 != current }, id: \.self) { m in Button(m) { model.setModel(m) } }
            } label: {
                Label(shown, lucide: .cpu, size: Theme.Fonts.small.number("size")).lineLimit(1).truncationMode(.middle)
            }
            .menuStyle(.borderlessButton)
            .menuIndicator(.hidden)
            .accessibilityLabel("Model: \(current.isEmpty ? "default" : current). Switch it")
            .parityAnchor("bot.model")
        } else {
            Label(shown, lucide: .cpu, size: Theme.Fonts.small.number("size")).lineLimit(1).truncationMode(.middle)
        }
    }

    private var conversations: some View {
        Menu {
            Button("New conversation") { model.newConversation() }
                .disabled(!model.running || model.busy)
            Divider()
            if model.sessions.isEmpty { Text("No other conversations") }
            ForEach(model.sessions) { s in
                Button((s.current == true ? "✓ " : "") + s.name) { model.resume(s) }
            }
        } label: {
            Text("Conversations")
        }
        .menuStyle(.borderlessButton)
        .disabled(!model.running)
        .accessibilityLabel("Conversations: start a new one or resume one")
        .parityAnchor("bot.conversations")
    }

    private var more: some View {
        Menu {
            Button("Restart") { model.restart() }.disabled(!model.running)
            Button(model.jobs.map { $0 > 0 ? "Jobs (\($0)) ↗" : "Jobs ↗" } ?? "Jobs ↗") { model.web("/settings") }
            Button("Settings ↗") { model.web("/settings") }
            Button("Manage bots ↗") { model.openWeb("/bots/manage") }
        } label: {
            LucideIcon.image(.ellipsis, size: Theme.Fonts.small.number("size"))
        }
        .menuStyle(.borderlessButton)
        .menuIndicator(.hidden)
        .accessibilityLabel("More: restart, jobs, settings, manage bots")
    }
}
