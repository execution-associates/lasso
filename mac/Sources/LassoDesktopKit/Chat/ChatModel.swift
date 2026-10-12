import Combine
import Foundation

/// A bot chat's side effects. An omp bot is driven through its own
/// routes (/api/bots/<name>/chat…); a claude bot runs in a herdr pane, so
/// its chat is the pane chat the card uses (/api/chat… on its pane).
@MainActor
public protocol BotChatActions: AnyObject {
    func chat(_ bot: BotView) async throws -> ChatPayload
    func send(_ bot: BotView, text: String) async throws -> Outcome
    func answer(_ bot: BotView, pending: ChatPayload.PendingUI, value: String) async throws -> Outcome
    func stop(_ bot: BotView) async throws -> Outcome
    func attach(_ file: URL, to bot: BotView) async throws -> String
}

/// The chat with a lasso bot: the same composer and keys as a card.
@MainActor
public final class ChatModel: ComposerModel {
    @Published public private(set) var bot: BotView
    @Published public private(set) var chat: ChatPayload?
    @Published public private(set) var loadError: String?
    @Published public var composer = ""
    @Published public var composerFocused = false
    @Published public private(set) var focusRequest = 0
    @Published public private(set) var busy = false
    /// Stop was pressed: its button waits (dimmed) until the turn ends.
    @Published public private(set) var stopping = false
    @Published public private(set) var notice: String?
    @Published public var highlighted: Int?

    public let actions: BotChatActions
    public var onClose: () -> Void = {}
    private var poll: Task<Void, Never>?

    public init(bot: BotView, actions: BotChatActions) {
        self.bot = bot
        self.actions = actions
    }

    public var composerPlaceholder: String { "Message \(bot.name)…" }

    public func update(bot: BotView) { if bot != self.bot { self.bot = bot } }
    public func show(chat: ChatPayload?) { self.chat = chat }

    /// The bot's open question or approval, as numbered options.
    public var pending: ChatPayload.PendingUI? { chat?.pending_ui?.first }

    public var pendingOptions: [String] {
        guard let p = pending else { return [] }
        switch p.method {
        case "select": return p.options ?? []
        case "confirm": return ["Yes", "No"]
        default: return []
        }
    }

    public var activeBindings: Set<KeyBinding> {
        var b: Set<KeyBinding> = [Keys.esc, Keys.attach, Keys.send, Keys.reply]
        if isWorking { b.insert(Keys.stop) }
        if !pendingOptions.isEmpty { b.formUnion([Keys.editStep, Keys.takeStep]) }
        return b
    }

    public func startPolling() {
        poll?.cancel()
        poll = Task { [weak self] in
            while !Task.isCancelled {
                await self?.load()
                try? await Task.sleep(nanoseconds: UInt64(Theme.Live.pollChat.seconds * 1e9))
            }
        }
    }

    public func stopPolling() { poll?.cancel() }

    public func load() async {
        do {
            let c = try await actions.chat(bot)
            if c != chat { chat = c }
            loadError = nil
            if stopping, !isWorking { stopping = false }
        } catch {
            loadError = String(describing: error)
        }
    }

    private func run(_ what: String, _ body: @escaping () async throws -> String?) {
        guard !busy else { return }
        busy = true
        Task {
            defer { busy = false }
            do { notice = try await body() } catch { notice = "\(what) failed: \(error)" }
        }
    }

    public func send() {
        let text = composer.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        run("Send") { [self] in
            let o = try await actions.send(bot, text: text)
            if o.ok { composer = "" }
            await load()
            return o.ok ? "Sent" : "\(o.outcome.capitalized): \(o.detail ?? "")"
        }
    }

    public func takeStep() {
        guard let p = pending else { return }
        let opts = pendingOptions
        let i = highlighted ?? 0
        guard opts.indices.contains(i) else { return }
        run("Answer") { [self] in
            let o = try await actions.answer(bot, pending: p, value: opts[i])
            highlighted = nil
            await load()
            return o.ok ? "Answered: \(opts[i])" : "Refused: \(o.detail ?? "")"
        }
    }

    public func editStep() {
        let opts = pendingOptions
        let i = highlighted ?? 0
        guard opts.indices.contains(i) else { return }
        composer = opts[i]
        focusComposer()
    }

    public func highlight(_ n: Int) {
        guard pendingOptions.indices.contains(n - 1) else { return }
        highlighted = n - 1
    }

    /// The bot is on its turn: Stop can interrupt it.
    public var isWorking: Bool {
        guard pending == nil else { return false }
        return chat?.running == true || bot.state == "working"
    }

    /// Stop takes Send's place while the bot works and the composer is empty.
    public var showsStop: Bool { isWorking && composer.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }

    public var stopLabel: String { "Stop \(bot.name)" }

    /// Stop (⌘.), as the card's: the button stays down until the turn ends.
    public func stop() {
        guard isWorking, !stopping else { return }
        stopping = true
        notice = nil
        let b = bot
        Task {
            do {
                let o = try await actions.stop(b)
                notice = StopNotice.text(o, name: b.name)
                if o.outcome == "refused" { stopping = false }
            } catch {
                notice = "Stop failed: \(error)"
                stopping = false
            }
            await load()
        }
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(Theme.Live.stopWait.seconds * 1e9))
            self?.stopping = false
        }
    }

    public func attachFile() { attach(FilePicker.choose(), at: nil) }

    /// Upload files (the paperclip's, or pasted or dropped on the message box)
    /// to the agent's machine and put their paths in the message at `caret`
    /// (UTF-16), or at its end.
    public func attach(_ files: [URL], at caret: Int?) {
        guard !files.isEmpty else { return }
        guard !busy else {
            files.forEach(Attachments.discardIfScratch)
            notice = "Wait a moment: \(files.count == 1 ? files[0].lastPathComponent : "these files") can go once this finishes"
            return
        }
        notice = Attachments.progress(files)
        run("Attach") { [self] in
            let n = try await Attachments.upload(files, at: caret, into: \.composer, of: self) {
                try await actions.attach($0, to: bot)
            }
            if caret == nil { focusComposer() }
            return n
        }
    }

    public func focusComposer() { focusRequest += 1 }
    public func say(_ s: String?) { notice = s }
}

/// A bot's chat through lasso's API.
@MainActor
public final class LiveBotChatActions: BotChatActions {
    let client: LassoClient
    let card: LiveCardActions

    public init(client: LassoClient, card: LiveCardActions) {
        self.client = client
        self.card = card
    }

    struct NotRunning: Error, CustomStringConvertible {
        let name: String
        var description: String { "\(name) is not running" }
    }

    /// A claude bot's pane, as the card's HostPane, so the pane chat routes apply.
    private func pane(_ bot: BotView) throws -> HostPane {
        guard let id = bot.pane_id, !id.isEmpty else { throw NotRunning(name: bot.name) }
        return HostPane(
            host: bot.host, host_label: bot.host, pane_id: id, workspace_id: "", workspace_label: bot.name, tab_id: "",
            tab_label: "", cwd: "", agent: "claude", agent_status: bot.state, has_agent: true, focused: false)
    }

    private func path(_ bot: BotView, _ tail: String) -> String {
        "/api/bots/\(bot.name.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? bot.name)/\(tail)"
    }

    public func chat(_ bot: BotView) async throws -> ChatPayload {
        if bot.runtime == "omp" { return try await client.get(path(bot, "chat")) }
        return try await card.chat(pane(bot))
    }

    struct Send: Encodable { var text: String }

    public func send(_ bot: BotView, text: String) async throws -> Outcome {
        if bot.runtime == "omp" { return try await client.post(path(bot, "chat/send"), json: Send(text: text)) }
        return try await card.send(pane(bot), text: text)
    }

    struct Answer: Encodable {
        var id: String
        var value: String?
        var confirmed: Bool?
    }

    public func answer(_ bot: BotView, pending: ChatPayload.PendingUI, value: String) async throws -> Outcome {
        let body =
            pending.method == "confirm"
            ? Answer(id: pending.id, value: nil, confirmed: value == "Yes") : Answer(id: pending.id, value: value, confirmed: nil)
        return try await client.post(path(bot, "chat/answer"), json: body)
    }

    struct Empty: Encodable {}

    public func stop(_ bot: BotView) async throws -> Outcome {
        if bot.runtime == "omp" { return try await client.post(path(bot, "chat/stop"), json: Empty()) }
        return try await card.stop(pane(bot))
    }

    public func attach(_ file: URL, to bot: BotView) async throws -> String {
        try await card.upload(file, host: bot.host)
    }
}

/// The fixtures' chat: the payload from the fixture, side effects recorded.
@MainActor
public final class FixtureBotChatActions: BotChatActions {
    let payload: ChatPayload?
    public private(set) var calls: [String] = []

    public init(payload: ChatPayload?) { self.payload = payload }

    public func chat(_ bot: BotView) async throws -> ChatPayload {
        payload ?? ChatPayload(pane_id: "bot:\(bot.name)", agent: bot.runtime, host: bot.host, items: [])
    }

    public func send(_ bot: BotView, text: String) async throws -> Outcome {
        calls.append("send")
        return Outcome(outcome: "confirmed")
    }

    public func answer(_ bot: BotView, pending: ChatPayload.PendingUI, value: String) async throws -> Outcome {
        calls.append("answer")
        return Outcome(outcome: "sent")
    }

    public func stop(_ bot: BotView) async throws -> Outcome {
        calls.append("stop")
        return Outcome(outcome: "sent")
    }

    public func attach(_ file: URL, to bot: BotView) async throws -> String {
        calls.append("attach")
        return "/fixture/" + file.lastPathComponent
    }
}
