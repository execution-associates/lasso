import Combine
import Foundation

/// The side effects a card can cause, all through lasso's API (LiveCardActions)
/// or recorded as no-ops (the parity fixtures).
@MainActor
public protocol CardActions: AnyObject {
    func chat(_ pane: HostPane) async throws -> ChatPayload
    func send(_ pane: HostPane, text: String) async throws -> Outcome
    /// Answer `question` (as captured when it was drawn) with an option, or
    /// with free text in the dialog's own Other row (S4).
    func answer(_ pane: HostPane, question: ChatTool.Question, option: Int?, custom: String?) async throws -> Outcome
    /// An omp bot's open request (`pending_ui`), answered by its id.
    func answer(_ pane: HostPane, pending: ChatPayload.PendingUI, value: String) async throws -> Outcome
    func kill(_ pane: HostPane) async throws -> String
    /// Acknowledge the pane's inbox entry (S1, ui_state card_dismiss):
    /// nothing reaches the agent, and it returns when it has something new.
    func acknowledge(_ pane: HostPane) async throws
    /// Bring the pane forward in herdr: how a question lasso cannot read is answered.
    func focus(_ pane: HostPane) async throws
    /// Upload a file to the agent's machine; the path to put in the message.
    func attach(_ file: URL, to pane: HostPane) async throws -> String
    /// Whether the server keeps shared acknowledgements (S1).
    var canAcknowledge: Bool { get }
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
    /// Whether the card has the keyboard, and how (PLAN 13.7a): it decides
    /// which keys the card shows and answers.
    @Published public var keyMode: KeyMode = .none

    public enum KeyMode: Sendable { case none, lent, full }

    /// The keys the card shows as keycaps on its controls right now.
    public var shownKeys: Set<String>? {
        switch keyMode {
        case .none: nil
        case .lent: Set(CardKeys.hoverSet.map(\.id))
        case .full: Set(activeBindings.map(\.id))
        }
    }
    /// E once armed Acknowledge (a keyboard the pointer lent asks twice).
    @Published public private(set) var ackArmed = false
    @Published public var highlighted: Int?
    /// The open question as it was when drawn (PLAN 13.4): an answer is
    /// built from this, never from the chat re-read at send time, so one
    /// cannot land on a question the agent has moved past.
    @Published public private(set) var captured: ChatTool.Question?
    private var capturedID: String?
    /// The composer is answering the question in its own words (Other).
    @Published public var answeringCustom = false
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
    /// acknowledged. The open card is always in it.
    public var pages: [HostPane] {
        if let inbox {
            let list = inbox.panes
            return list.contains(where: { $0.key == pane.key }) ? list : [pane] + list
        }
        let acked = fleet.uiState.dismissed_cards ?? [:]
        let inbox = fleet.agents.filter { p in
            // A bot's pane opens the bot's chat, not a card.
            guard p.state == .blocked || p.state == .done, fleet.bot(for: p) == nil else { return false }
            if let at = acked[p.key], (p.transcript_at ?? 0) <= at { return false }
            return true
        }
        return inbox.contains(where: { $0.key == pane.key }) ? inbox : [pane] + inbox
    }

    /// The live row for this pane (the fleet refreshes underneath the card).
    public var current: HostPane { fleet.panes.first { $0.key == pane.key } ?? pane }

    /// The machine whose files the conversation names: the one its
    /// transcript was read from (served_by), else the pane's.
    public var mediaHost: String { chat?.served_by ?? chat?.host ?? pane.host }

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
        return (captured?.options ?? []).prefix(9).enumerated().map {
            NextStep(index: $0.offset, label: $0.element.label, detail: $0.element.description)
        }
    }

    /// The bindings live in this card's current state: what the keycaps on
    /// screen may show (the parity harness checks every rendered keycap is
    /// in here).
    public var activeBindings: Set<KeyBinding> {
        var b: Set<KeyBinding> = [Keys.esc, Keys.attach, Keys.send, Keys.next, Keys.previous]
        if !isBot { b.insert(Keys.kill) }
        if actions.canAcknowledge { b.insert(Keys.acknowledge) }
        if !nextSteps.isEmpty { b.formUnion([Keys.editStep, Keys.takeStep]) }
        return b
    }

    public func show(chat: ChatPayload?) {
        self.chat = chat
        recapture()
    }

    /// Take the question as it is drawn now, if it is a new one: a changed
    /// question drops a pick in progress.
    private func recapture() {
        guard let item = chat?.items.last(where: { $0.tool?.ask != nil }), item.tool?.state != "completed",
              item.tool?.state != "error", let q = item.tool?.ask?.questions.first
        else {
            if captured != nil { captured = nil; capturedID = nil; answeringCustom = false }
            return
        }
        let id = item.id + "\u{0}" + (q.question ?? "")
        guard id != capturedID else { return }
        captured = q
        capturedID = id
        highlighted = nil
        answeringCustom = false
    }

    /// The dialog has an Other row lasso can type into: claude lays out a
    /// question with previews side by side, with none (S4).
    public var allowsCustom: Bool {
        if let p = pending { return p.method == "input" || p.method == "editor" }
        guard let q = captured else { return false }
        return !(q.options ?? []).contains { !($0.preview ?? "").isEmpty }
    }

    /// Blocked with nothing lasso can read to answer (PLAN 13.5): the human
    /// answers in the terminal; lasso never types blind keys.
    public var blockedWithoutChoices: Bool {
        current.state == .blocked && chat != nil && captured == nil && pending == nil
    }

    public func focusInHerdr() {
        run("Focus") { [self] in
            try await actions.focus(pane)
            return "Focused \(pane.displayName) in herdr: answer it there"
        }
    }

    /// Answer in your own words: the composer sends to the dialog's Other row.
    public func answerInOwnWords() {
        guard allowsCustom else { return }
        answeringCustom = true
        focusComposer()
    }

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
            recapture()
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
        if answeringCustom {
            answer(custom: text)
            return
        }
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
        guard let q = captured else { return }
        run("Answer") { [self] in
            let o = try await actions.answer(pane, question: q, option: i, custom: nil)
            highlighted = nil
            await load()
            return o.ok ? "Answered: \(nextSteps.first { $0.index == i }?.label ?? "")" : "Refused: \(o.detail ?? "")"
        }
    }

    /// Send the composer's text as the answer itself.
    private func answer(custom text: String) {
        if let p = pending {
            run("Answer") { [self] in
                let o = try await actions.answer(pane, pending: p, value: text)
                if o.ok { composer = ""; answeringCustom = false }
                await load()
                return o.ok ? "Answered" : "Refused: \(o.detail ?? "")"
            }
            return
        }
        guard let q = captured else { return }
        run("Answer") { [self] in
            let o = try await actions.answer(pane, question: q, option: nil, custom: text)
            if o.ok { composer = ""; answeringCustom = false }
            await load()
            return o.ok ? "Answered in your words" : "Refused: \(o.detail ?? "")"
        }
    }

    /// ↳ : put the highlighted step into the composer to edit.
    public func editStep() {
        let i = highlighted ?? 0
        guard let step = nextSteps.first(where: { $0.index == i }) else { return }
        composer = step.label
        // Edited, it is the answer in your own words where the dialog takes one.
        answeringCustom = allowsCustom
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
        if killArmed || ackArmed {
            killArmed = false
            ackArmed = false
            notice = nil
        }
    }

    /// E while the pointer, not a click, lent the keyboard: the first press
    /// arms, the second acknowledges, so a word typed for another app that
    /// starts with e cannot acknowledge anything (PLAN 13.7a).
    public func acknowledgeTwice() {
        guard ackArmed else {
            ackArmed = true
            notice = "Press E again to acknowledge \(pane.displayName)"
            return
        }
        ackArmed = false
        acknowledge()
    }

    public func acknowledge() {
        guard actions.canAcknowledge else {
            notice = "Acknowledge needs a lasso with shared acknowledgements"
            return
        }
        run("Acknowledge") { [self] in
            try await actions.acknowledge(pane)
            page(1)
            return "Acknowledged"
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

    public var composerPlaceholder: String {
        guard answeringCustom else { return "Reply to \(current.displayName)" }
        return "Your answer: \(captured?.question ?? pending?.title ?? "")"
    }

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
        ackArmed = false
        highlighted = nil
        notice = nil
        Task { await load() }
    }
}
