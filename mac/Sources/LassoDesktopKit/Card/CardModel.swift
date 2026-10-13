import Combine
import Foundation

/// The side effects a card can cause, all through lasso's API (LiveCardActions)
/// or recorded as no-ops (the parity fixtures).
@MainActor
public protocol CardActions: AnyObject {
    /// The pane's conversation: its newest page, or with `before` the page
    /// that ends at that transcript offset (the page above one on screen).
    func chat(_ pane: HostPane, before: Int64?) async throws -> ChatPayload
    func send(_ pane: HostPane, text: String) async throws -> Outcome
    /// Answer `question` (as captured when it was drawn) with an option, or
    /// with free text in the dialog's own Other row (S4).
    func answer(_ pane: HostPane, question: ChatTool.Question, option: Int?, custom: String?) async throws -> Outcome
    /// An omp bot's open request (`pending_ui`), answered by its id.
    func answer(_ pane: HostPane, pending: ChatPayload.PendingUI, value: String) async throws -> Outcome
    /// Interrupt the agent's turn (one Escape; lasso refuses unless it is working).
    func stop(_ pane: HostPane) async throws -> Outcome
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
    /// herdr's custom agent label; "" clears it.
    func rename(_ pane: HostPane, to name: String) async throws
}

extension CardActions {
    /// The newest page of the pane's conversation.
    public func chat(_ pane: HostPane) async throws -> ChatPayload { try await chat(pane, before: nil) }
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

/// What a Stop's answer puts in the notice line (the web chat's words).
enum StopNotice {
    static func text(_ o: Outcome, name: String) -> String {
        switch o.outcome {
        case "refused": o.detail.map { "Not stopped: \($0)" } ?? "\(name) was not stopped"
        case "uncertain": "\(o.detail ?? "Stop unconfirmed"): check the terminal"
        default: "Stopping \(name)…"
        }
    }
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
    /// Everything read of this agent's conversation, older pages included.
    @Published public internal(set) var history = ChatHistory()
    /// The conversation as drawn: the newest page with every row read so far.
    public var chat: ChatPayload? { history.payload }
    @Published public private(set) var loadError: String?
    @Published public var composer = ""
    @Published public var attachments: [ComposerAttachment] = []
    @Published public var composerFocused = false
    /// Bumped to ask the composer field to take focus.
    @Published public private(set) var focusRequest = 0
    @Published public private(set) var busy = false
    /// Stop was pressed: its button waits (dimmed) until the turn ends.
    @Published public private(set) var stopping = false
    /// One line of feedback in the footer (sent, refused, kill armed…).
    @Published public private(set) var notice: String?
    /// Your message, sent and not yet in the transcript: shown at once, faded.
    @Published public private(set) var outgoing: String?
    /// The title is a field: the agent is being renamed.
    @Published public private(set) var renaming = false
    @Published public var renameDraft = ""
    @Published public private(set) var killArmed = false
    /// A kill lasso has not answered yet (closing a pane can take seconds):
    /// that pane's key. The parity harness sets it to draw the wait.
    @Published public internal(set) var killingKey: String?
    /// This card's agent is being killed: its Kill reads "Killing…", the
    /// conversation dims, and every key but paging (and esc) waits.
    public var killing: Bool { killingKey == pane.key }
    /// Why the last kill of this card's agent failed, until the next X or
    /// a page away (the card shows no notice line under its next steps).
    @Published public private(set) var killFailure: (key: String, reason: String)?
    public var killFailed: String? { killFailure?.key == pane.key ? killFailure?.reason : nil }
    /// Whether the card has the keyboard, and how (PLAN 13.7a): it decides
    /// which keys the card answers.
    @Published public var keyMode: KeyMode = .none

    public enum KeyMode: Sendable { case none, lent, full }
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
    /// Show the agent in lasso's herdr, its pane focused (the herdr window,
    /// PLAN 22). Nil where there is no such window: a fixture app, the hover
    /// preview.
    public var onShowInHerdr: ((HostPane) -> Void)?
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

    /// Whether this agent can be shown in the herdr window: there is one, and
    /// the agent has a pane to focus.
    public var canShowInHerdr: Bool { onShowInHerdr != nil && !current.pane_id.isEmpty }

    /// The agent's own chat window (the header's chat button): set by the
    /// inbox's card, which hands the agent over to it.
    var onOpenWindow: ((HostPane) -> Void)?
    public var canOpenWindow: Bool { onOpenWindow != nil }
    public func openWindow() { onOpenWindow?(current) }

    /// T, or the header's terminal button.
    public func showInHerdr() {
        guard canShowInHerdr else { return }
        onShowInHerdr?(current)
    }

    /// A bot's pane: lasso keeps it running, so the card offers no Kill.
    public var isBot: Bool { fleet.bot(for: current) != nil }

    // MARK: renaming

    /// A bot is found by its herdr name, so it is renamed in its settings.
    public var canRename: Bool { !isBot }

    public func startRename() {
        guard canRename else { return }
        // An agent without a label starts empty, its shown name the placeholder:
        // a click away with nothing typed changes nothing.
        renameDraft = current.agent_label ?? ""
        renaming = true
    }

    public func cancelRename() { renaming = false }

    /// Keep the typed name (herdr's agent label); empty clears it.
    public func commitRename() {
        guard renaming else { return }
        renaming = false
        let p = current
        let typed = renameDraft.trimmingCharacters(in: .whitespacesAndNewlines)
        let name = AgentName.from(typed)
        guard name != (p.agent_label ?? "") else { return }
        guard !(name.isEmpty && !typed.isEmpty) else {
            notice = "An agent name needs a letter: lowercase letters, digits, - and _"
            return
        }
        run("Rename") { [self] in
            try await actions.rename(p, to: name)
            return nil
        }
    }

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

    /// The bindings live in this card's current state (the parity harness
    /// checks each is routed by the surface's key handler).
    public var activeBindings: Set<KeyBinding> {
        var b: Set<KeyBinding> = [Keys.esc, Keys.attach, Keys.send, Keys.reply, Keys.next, Keys.previous]
        if !isBot, !killing { b.insert(Keys.kill) }
        if canRename { b.insert(Keys.rename) }
        if isWorking { b.insert(Keys.stop) }
        if canShowInHerdr { b.insert(Keys.herdr) }
        if canAcknowledgeNow { b.insert(Keys.acknowledge) }
        if !nextSteps.isEmpty { b.formUnion([Keys.editStep, Keys.takeStep]) }
        return b
    }

    public func show(chat: ChatPayload?) {
        var h = ChatHistory()
        if let chat { h.take(chat, key: pane.key) }
        history = h
        recapture()
    }

    /// Reaching the top of the conversation: the page above it.
    public func loadOlder() { Task { await readOlder() } }

    /// A hover preview scrolled above the latest turn shows what came before.
    public func revealEarlier() { if !history.earlier { history.revealEarlier() } }

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
        // With a herdr window, answer it there: the window opens on the pane.
        if canShowInHerdr { return showInHerdr() }
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

    /// What the conversation waits on: its first load, an agent still
    /// booting (no transcript yet, or an empty one), or the agent's turn.
    /// herdr's state is read from `current`, the fleet's live row: `pane` is
    /// the row this card opened on, so a window opened mid-turn would say
    /// "working" for as long as it stays open.
    public var phase: ConversationPhase {
        let h = harnessName(pane.agent)
        let state = current.state
        guard let c = chat else { return loadError == nil ? .loading : .idle }
        if c.items.isEmpty {
            return c.starting == true || c.unavailable != nil || state == .working || outgoing != nil ? .starting(h) : .idle
        }
        // Waiting on the human is not working.
        if state == .blocked || pending != nil || captured != nil { return .idle }
        return c.running == true || state == .working || outgoing != nil ? .working(h) : .idle
    }

    /// The agent is on its turn (or booting): Stop can interrupt it.
    public var isWorking: Bool {
        switch phase {
        case .working, .starting: true
        case .loading, .idle: false
        }
    }

    /// Stop takes Send's place while the agent works and the composer is
    /// empty; a draft brings Send back (the web chat's rule).
    public var showsStop: Bool { isWorking && composerIsEmpty }

    public var stopLabel: String { "Stop \(current.displayName)" }

    /// Stop (⌘.): one Escape to the agent through lasso. The button stays
    /// down until a poll shows the turn over, or `live.stop_wait` passes.
    public func stop() {
        guard isWorking, !stopping else { return }
        stopping = true
        notice = nil
        let p = pane
        let name = current.displayName
        Task {
            do {
                let o = try await actions.stop(p)
                notice = StopNotice.text(o, name: name)
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
        let p = pane
        do {
            let c = try await actions.chat(p)
            // Read for the card before a page to another (J/K): not this one's.
            guard p.key == pane.key else { return }
            var h = history
            h.take(c, key: p.key)
            if h != history { history = h }
            if let o = outgoing,
                c.items.contains(where: { ($0.kind == "user" || $0.kind == "incoming") && $0.text?.trimmingCharacters(in: .whitespacesAndNewlines) == o })
            {
                outgoing = nil
            }
            recapture()
            loadError = nil
            if stopping, !isWorking { stopping = false }
        } catch {
            guard p.key == pane.key else { return }
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
        guard canSend else { return }
        let text = composedMessage
        let sent = Set(attachments.map(\.id))
        if answeringCustom {
            answer(custom: text, attached: sent)
            return
        }
        outgoing = text
        let p = pane
        let order = pages.map(\.key)
        run("Send") { [self] in
            let o: Outcome
            do { o = try await actions.send(p, text: text) } catch {
                outgoing = nil
                throw error
            }
            if o.ok { clear(sent) } else { outgoing = nil }
            await load()
            guard o.ok else { return "\(o.outcome.capitalized): \(o.detail ?? "")" }
            // In the inbox, a reply is the answer to the entry: it is
            // acknowledged, and the card moves on to the next.
            guard inbox != nil, actions.canAcknowledge else { return "Sent" }
            let entry = fleet.inbox.first { $0.id == p.key }
            try await actions.acknowledge(p)
            if let entry { fleet.markAcknowledged(entry) }
            resolved(p, killed: false, order: order)
            return "Sent and acknowledged"
        }
    }

    /// The inbox card's Return leaves the composer: J/K/A act again while
    /// the reply goes.
    public var leavesFieldOnSend: Bool { inbox != nil }

    /// A message went: its words go, and the attachments it carried.
    private func clear(_ sent: Set<ComposerAttachment.ID>) {
        composer = ""
        attachments.removeAll { sent.contains($0.id) }
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

    /// Send the composer's message as the answer itself.
    private func answer(custom text: String, attached sent: Set<ComposerAttachment.ID>) {
        if let p = pending {
            run("Answer") { [self] in
                let o = try await actions.answer(pane, pending: p, value: text)
                if o.ok { clear(sent); answeringCustom = false }
                await load()
                return o.ok ? "Answered" : "Refused: \(o.detail ?? "")"
            }
            return
        }
        guard let q = captured else { return }
        run("Answer") { [self] in
            let o = try await actions.answer(pane, question: q, option: nil, custom: text)
            if o.ok { clear(sent); answeringCustom = false }
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
        guard !killing else { return }
        killFailure = nil
        guard killArmed else {
            killArmed = true
            notice = "Press X again to kill \(pane.displayName)"
            return
        }
        killArmed = false
        guard !busy else { return }
        let p = pane
        let order = pages.map(\.key)
        killingKey = p.key
        run("Kill") { [self] in
            // Answered either way: a failure's reason goes in the notice.
            defer { killingKey = nil }
            let said: String
            do { said = try await actions.kill(p) } catch {
                killFailure = (p.key, "Kill failed: \(error)")
                throw error
            }
            resolved(p, killed: true, order: order)
            return said
        }
    }

    public func disarm() {
        if killArmed {
            killArmed = false
            notice = nil
        }
    }


    /// A reply is being written: Acknowledge waits until it is sent or
    /// cleared, so a draft is never left behind on an entry that goes away.
    public var hasDraft: Bool { !composerIsEmpty }

    /// Acknowledge can be pressed now (A, or its button).
    public var canAcknowledgeNow: Bool { actions.canAcknowledge && !hasDraft && !killing }

    public func acknowledge() {
        guard actions.canAcknowledge else {
            notice = "Acknowledge needs a lasso with shared acknowledgements"
            return
        }
        guard !hasDraft else { return }
        let p = pane
        let entry = fleet.inbox.first { $0.id == p.key }
        let order = pages.map(\.key)
        run("Acknowledge") { [self] in
            try await actions.acknowledge(p)
            if let entry { fleet.markAcknowledged(entry) }
            resolved(p, killed: false, order: order)
            return "Acknowledged \(p.displayName)"
        }
    }

    /// Kill or Acknowledge succeeded: the entry has left the inbox (the
    /// fleet hides it at once, and a stale poll cannot bring it back). The
    /// inbox card moves to the next entry in Inbox order, the previous one
    /// when it was the last, or the empty inbox when none remain; a window
    /// (no inbox) closes after a kill.
    /// `order` is the Inbox order when the action was pressed.
    func resolved(_ p: HostPane, killed: Bool, order before: [String]) {
        if killed { fleet.markKilled(p.key) }
        guard pane.key == p.key else { return }
        guard inbox != nil else {
            if killed { onClose() }
            return
        }
        let rest = pages.filter { $0.key != p.key }
        guard !rest.isEmpty else {
            onInboxEmpty()
            return
        }
        let i = before.firstIndex(of: p.key) ?? 0
        let after = before[(i + 1)...].compactMap { k in rest.first { $0.key == k } }
        let ahead = before[..<i].reversed().compactMap { k in rest.first { $0.key == k } }
        show(after.first ?? ahead.first ?? rest[0], direction: after.isEmpty ? -1 : 1)
    }

    /// The inbox card resolved its last entry: show the empty inbox.
    public var onInboxEmpty: () -> Void = {}

    /// The paperclip (U): choose files, upload them to the agent's machine,
    /// and attach them to the message.
    public func attachFile() { attach(chooseFiles(), at: nil) }

    /// The paperclip's chooser (a seam for tests, which cannot click one).
    var chooseFiles: () -> [URL] = { FilePicker.choose() }

    /// Upload files (the paperclip's, or pasted or dropped on the message box)
    /// to the agent's machine as attachments: previews over the field, their
    /// paths added to the message on send, never typed into it.
    public func attach(_ files: [URL], at caret: Int?) {
        guard !files.isEmpty else { return }
        guard !busy else {
            files.forEach(Attachments.discardIfScratch)
            notice = "Wait a moment: \(files.count == 1 ? files[0].lastPathComponent : "these files") can go once this finishes"
            return
        }
        notice = Attachments.progress(files)
        run("Attach") { [self] in
            let n = try await Attachments.upload(files, into: \.attachments, of: self) {
                try await actions.attach($0, to: pane)
            }
            if caret == nil { focusComposer() }
            return n
        }
    }

    public func focusComposer() {
        focusRequest += 1
        focusAskedAt = Date()
    }

    /// When the composer was last asked for the cursor. The field takes it on
    /// the next turn of the run loop, so a key typed in between (the first
    /// letter of a reply in a window that just opened on it) is its text too.
    private var focusAskedAt: Date?

    /// Keys are the composer's text: it has the cursor, or was just asked to
    /// take it (`live.focus_grace`).
    public var composerHasKeys: Bool {
        composerFocused || focusAskedAt.map { Date().timeIntervalSince($0) < Theme.Live.focusGrace.seconds } == true
    }

    /// Do what a routed key asks, in the inbox card or an agent window
    /// (CardKeys). Any key but X disarms a kill; while a kill is in flight
    /// only J/K act and the rest are swallowed. Returns false for a key that
    /// does nothing here (the window's own keys, esc and the unbound, are
    /// its caller's), so the system gets it.
    @discardableResult
    public func perform(_ action: CardKeyAction) -> Bool {
        // A kill in flight: only paging moves; the rest waits for lasso.
        if killing, action != .next, action != .previous { return true }
        if action != .kill { disarm() }
        switch action {
        case .digit(let n): highlight(n)
        case .attach: attachFile()
        // ⏎: to the composer, or send what it holds.
        case .send: if composerIsEmpty { focusComposer() } else { send() }
        case .reply: focusComposer()
        case .rename:
            guard canRename else { return false }
            startRename()
        case .kill: kill()
        case .acknowledge: acknowledge()
        case .next: page(1)
        case .previous: page(-1)
        case .editStep: editStep()
        case .takeStep: takeStep()
        case .stop:
            guard isWorking else { return false }
            stop()
        case .herdr:
            guard canShowInHerdr else { return false }
            showInHerdr()
        case .close, .unbound, .handBack: return false
        }
        return true
    }

    public var composerPlaceholder: String {
        guard answeringCustom else { return "Reply to \(current.displayName)" }
        return "Your answer: \(captured?.question ?? pending?.title ?? "")"
    }

    public func say(_ s: String?) { notice = s }

    /// J/K: the next or previous card in the inbox.
    public func page(_ delta: Int) {
        let list = pages
        guard list.count > 1, let i = list.firstIndex(where: { $0.key == pane.key }) else { return }
        show(list[(i + delta + list.count) % list.count], direction: delta)
    }

    private func show(_ next: HostPane, direction delta: Int) {
        pageDirection = delta
        withAnimationToken(Theme.Motion.cardPaging, probe: animationProbe) { [self] in
            pane = next
            history.reset()
            read.insert(next.key)
        }
        inbox?.markSeen(next)
        killArmed = false
        killFailure = nil
        highlighted = nil
        notice = nil
        stopping = false
        Task { await load() }
    }
}

extension CardModel: ChatPaging {
    func olderPage(before: Int64) async throws -> ChatPayload { try await actions.chat(pane, before: before) }
}
