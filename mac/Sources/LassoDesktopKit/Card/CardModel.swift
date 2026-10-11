import Combine
import Foundation

/// The side effects a card can cause, all through lasso's API (LiveCardActions)
/// or recorded as no-ops (the parity fixtures).
@MainActor
public protocol CardActions: AnyObject {
    func chat(_ pane: HostPane) async throws -> ChatPayload
    func send(_ pane: HostPane, text: String) async throws -> Outcome
    func answer(_ pane: HostPane, question: ChatTool.Question, option: Int) async throws -> Outcome
    /// An omp bot's open request (`pending_ui`), answered by its id.
    func answer(_ pane: HostPane, pending: ChatPayload.PendingUI, value: String) async throws -> Outcome
    func kill(_ pane: HostPane) async throws -> String
    func dismiss(_ pane: HostPane) async throws
    /// Upload a file to the agent's machine; the path to put in the message.
    func attach(_ file: URL, to pane: HostPane) async throws -> String
    /// Whether the server keeps shared card dismissals (S1).
    var canDismiss: Bool { get }
}

/// lasso's answer to a send/answer/stop: confirmed | sent | refused | uncertain.
public struct Outcome: Decodable, Sendable, Equatable {
    public var outcome: String
    public var detail: String?

    public init(outcome: String, detail: String? = nil) {
        self.outcome = outcome
        self.detail = detail
    }

    public var ok: Bool { outcome == "confirmed" || outcome == "sent" }
}

/// A suggested next step: an option of the agent's open question.
public struct NextStep: Equatable, Sendable, Identifiable {
    public var id: Int { index }
    public var index: Int
    public var label: String
    public var detail: String?
}

/// One card: an agent's conversation, the composer, its next steps, and the
/// pages (J/K) around it.
@MainActor
public final class CardModel: ComposerModel {
    @Published public private(set) var pane: HostPane
    @Published public private(set) var chat: ChatPayload?
    @Published public private(set) var loadError: String?
    @Published public var composer = ""
    @Published public var composerFocused = false
    /// Bumped to ask the composer field to take focus.
    @Published public private(set) var focusRequest = 0
    @Published public private(set) var busy = false
    /// One line of feedback in the footer (sent, refused, kill armed…).
    @Published public private(set) var notice: String?
    @Published public private(set) var killArmed = false
    @Published public var highlighted: Int?
    /// Cards seen this session, for the unread bar.
    @Published public private(set) var read: Set<String> = []
    /// +1 paging forward, -1 back: the direction the content slides.
    @Published public private(set) var pageDirection = 0

    public let fleet: FleetModel
    public let actions: CardActions
    /// The inbox the card pages through, in its order; nil pages the fleet's
    /// blocked and finished agents (the fixtures' cards).
    public var inbox: InboxModel?
    public let now: () -> Date
    public var onClose: () -> Void = {}
    /// Set by the parity harness only (see ProbedAnimation).
    public var animationProbe: MotionProbe?
    private var poll: Task<Void, Never>?

    public init(pane: HostPane, fleet: FleetModel, actions: CardActions, now: @escaping () -> Date = Date.init) {
        self.pane = pane
        self.fleet = fleet
        self.actions = actions
        self.now = now
        read.insert(pane.key)
    }

    /// The inbox the card pages through: what waits on the human (blocked)
    /// and what finished unseen (done), most urgent first, minus the
    /// dismissed. The open card is always in it.
    public var pages: [HostPane] {
        if let inbox {
            let list = inbox.panes
            return list.contains(where: { $0.key == pane.key }) ? list : [pane] + list
        }
        let dismissed = fleet.uiState.dismissed_cards ?? [:]
        let inbox = fleet.agents.filter { p in
            // A bot's pane opens the bot's chat, not a card.
            guard p.state == .blocked || p.state == .done, fleet.bot(for: p) == nil else { return false }
            if let at = dismissed[p.key], (p.transcript_at ?? 0) <= at { return false }
            return true
        }
        return inbox.contains(where: { $0.key == pane.key }) ? inbox : [pane] + inbox
    }

    /// The live row for this pane (the fleet refreshes underneath the card).
    public var current: HostPane { fleet.panes.first { $0.key == pane.key } ?? pane }

    /// The open question, if the agent is asking one: the last tool row with
    /// an ask that is still running.
    public var openQuestion: ChatTool.Question? {
        guard let item = chat?.items.last(where: { $0.tool?.ask != nil }), item.tool?.state != "completed",
              item.tool?.state != "error"
        else { return nil }
        return item.tool?.ask?.questions.first
    }

    /// A bot's pane: lasso keeps it running, so the card offers no Kill.
    public var isBot: Bool { fleet.bot(for: current) != nil }

    /// What an omp bot waits on: its first open request.
    public var pending: ChatPayload.PendingUI? { chat?.pending_ui?.first }

    private var pendingOptions: [String] {
        switch pending?.method {
        case "select": pending?.options ?? []
        case "confirm": ["Yes", "No"]
        default: []
        }
    }

    public var nextSteps: [NextStep] {
        if pending != nil {
            return pendingOptions.prefix(9).enumerated().map { NextStep(index: $0.offset, label: $0.element, detail: nil) }
        }
        return (openQuestion?.options ?? []).prefix(9).enumerated().map {
            NextStep(index: $0.offset, label: $0.element.label, detail: $0.element.description)
        }
    }

    /// The bindings live in this card's current state: what the keycaps on
    /// screen may show (the parity harness checks every rendered keycap is
    /// in here).
    public var activeBindings: Set<KeyBinding> {
        var b: Set<KeyBinding> = [Keys.esc, Keys.attach, Keys.send, Keys.next, Keys.previous]
        if !isBot { b.insert(Keys.kill) }
        if actions.canDismiss { b.insert(Keys.discard) }
        if !nextSteps.isEmpty { b.formUnion([Keys.editStep, Keys.takeStep]) }
        return b
    }

    public func show(chat: ChatPayload?) { self.chat = chat }

    // MARK: loading

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
            let c = try await actions.chat(pane)
            if c != chat { chat = c }
            loadError = nil
        } catch {
            loadError = String(describing: error)
        }
    }

    // MARK: actions

    private func run(_ what: String, _ body: @escaping () async throws -> String?) {
        guard !busy else { return }
        busy = true
        Task {
            defer { busy = false }
            do {
                notice = try await body()
            } catch {
                notice = "\(what) failed: \(error)"
            }
        }
    }

    public func send() {
        let text = composer.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        run("Send") { [self] in
            let o = try await actions.send(pane, text: text)
            if o.ok { composer = "" }
            await load()
            return o.ok ? "Sent" : "\(o.outcome.capitalized): \(o.detail ?? "")"
        }
    }

    /// → : take the highlighted step (or the first): answer the open question
    /// with that option.
    public func takeStep() {
        let i = highlighted ?? 0
        guard nextSteps.indices.contains(i) else { return }
        if let p = pending {
            let value = pendingOptions[i]
            run("Answer") { [self] in
                let o = try await actions.answer(pane, pending: p, value: value)
                highlighted = nil
                await load()
                return o.ok ? "Answered: \(value)" : "Refused: \(o.detail ?? "")"
            }
            return
        }
        guard let q = openQuestion else { return }
        run("Answer") { [self] in
            let o = try await actions.answer(pane, question: q, option: i)
            highlighted = nil
            await load()
            return o.ok ? "Answered: \(nextSteps.first { $0.index == i }?.label ?? "")" : "Refused: \(o.detail ?? "")"
        }
    }

    /// ↳ : put the highlighted step into the composer to edit.
    public func editStep() {
        let i = highlighted ?? 0
        guard let step = nextSteps.first(where: { $0.index == i }) else { return }
        composer = step.label
        focusComposer()
    }

    public func highlight(_ n: Int) {
        guard nextSteps.indices.contains(n - 1) else { return }
        highlighted = n - 1
    }

    /// X once arms, X again kills: killing an agent is not undoable.
    public func kill() {
        guard !isBot else {
            notice = "\(pane.displayName) is a bot: stop it from lasso's Bots view"
            return
        }
        guard killArmed else {
            killArmed = true
            notice = "Press X again to kill \(pane.displayName)"
            return
        }
        killArmed = false
        run("Kill") { [self] in try await actions.kill(pane) }
    }

    public func disarm() {
        if killArmed {
            killArmed = false
            notice = nil
        }
    }

    public func discard() {
        guard actions.canDismiss else {
            notice = "Discard needs a lasso with shared card dismissals"
            return
        }
        run("Discard") { [self] in
            try await actions.dismiss(pane)
            page(1)
            return "Discarded"
        }
    }

    /// The paperclip (A): choose files, upload them to the agent's machine,
    /// and put their paths in the message.
    public func attachFile() {
        let files = FilePicker.choose()
        guard !files.isEmpty else { return }
        run("Attach") { [self] in
            for f in files {
                let path = try await actions.attach(f, to: pane)
                composer = composer.isEmpty ? path : composer + "\n" + path
            }
            focusComposer()
            return files.count == 1 ? "Attached \(files[0].lastPathComponent)" : "Attached \(files.count) files"
        }
    }

    public func focusComposer() { focusRequest += 1 }

    public var composerPlaceholder: String { "Reply to \(current.displayName)" }

    public func say(_ s: String?) { notice = s }

    /// J/K: the next or previous card in the inbox.
    public func page(_ delta: Int) {
        let list = pages
        guard list.count > 1, let i = list.firstIndex(where: { $0.key == pane.key }) else { return }
        let next = list[(i + delta + list.count) % list.count]
        pageDirection = delta
        withAnimationToken(Theme.Motion.cardPaging, probe: animationProbe) { [self] in
            pane = next
            chat = nil
            read.insert(next.key)
        }
        inbox?.markSeen(next)
        killArmed = false
        highlighted = nil
        notice = nil
        Task { await load() }
    }
}
