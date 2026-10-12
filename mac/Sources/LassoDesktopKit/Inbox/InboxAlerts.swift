import AppKit
import Foundation

/// How the inbox interrupts this Mac when something arrives (PLAN 17): the
/// arrival sound, once per burst, and a macOS notification per arrival. Both
/// go only for a kind this Mac wants (lasso's notification kinds, the bot
/// rule in `InboxModel.arrivalList`), and only for what is new: the first
/// reading once lasso has answered is the baseline, and an arrival is
/// announced once, however often it leaves the inbox and comes back.
@MainActor
public final class InboxAlerts {
    let fleet: FleetModel
    let inbox: InboxModel
    let actions: CardActions
    /// nil: no notifications (the parity harness, a bare binary).
    let notifier: Notifier?

    /// The arrival sound (the live app's; tests and fixtures make none).
    var playSound: () -> Void = {}
    /// lasso has answered every read the inbox is made of (FleetModel.ready).
    var ready: () -> Bool = { true }
    /// The arrival is on screen already (the card shows it, or the panel
    /// the notices): no notification.
    var showing: (InboxArrival) -> Bool = { _ in false }
    /// Asking macOS for permission waits until onboarding is done.
    var mayAsk: () -> Bool = { true }
    /// A bot's picture, as the Bots column has it.
    var picture: (String) -> NSImage? = { _ in nil }
    /// Acknowledge an entry through the shared path (the card's, when it
    /// shows that entry); by default lasso's, as the card does it.
    var acknowledgeEntry: (@MainActor (InboxEntry) async throws -> Void)?
    /// A click: open the inbox on the entry, on the notices, or as it is.
    var openEntry: (HostPane) -> Void = { _ in }
    var openNotices: () -> Void = {}
    var openInbox: () -> Void = {}

    /// The arrival keys of the last reading; nil until the baseline.
    private var before: Set<String>?
    /// Every arrival announced (or passed over), and when: never again.
    private var announced: [String: Date] = [:]
    /// The notifications in Notification Center, by id: what each is about.
    private(set) var delivered: [String: Delivered] = [:]
    private var asking: Task<PermissionState, Never>?

    struct Delivered: Equatable {
        var key: String
        /// The entry's pane (nil for a notice).
        var pane: String?
    }

    public init(fleet: FleetModel, inbox: InboxModel, actions: CardActions, notifier: Notifier?) {
        self.fleet = fleet
        self.inbox = inbox
        self.actions = actions
        self.notifier = notifier
        notifier?.onAction = { [weak self] t, a in self?.respond(t, a) }
    }

    // MARK: arrivals

    /// The inbox changed: sound and notify for what arrived.
    public func note(now: Date = Date()) {
        guard ready() else {
            before = nil
            return
        }
        let list = inbox.arrivalList
        let keys = Set(list.map(\.key))
        guard let before else {
            // The baseline: what is there now was there before this Mac looked.
            self.before = keys
            for k in keys { announced[k] = now }
            Task { await adoptDelivered(keys) }
            return
        }
        self.before = keys
        let fresh = list.filter { !before.contains($0.key) && announced[$0.key] == nil }
        for a in fresh { announced[a.key] = now }
        announced = announced.filter { keys.contains($0.key) || now.timeIntervalSince($0.value) < 86400 }
        let notify = fleet.notify
        let wanted = fresh.filter { a in a.kinds.contains { kind in notify?.wants(kind) ?? true } }
        if !wanted.isEmpty { playSound() }
        for a in wanted where !showing(a) { announce(a) }
        withdraw(keeping: keys)
    }

    /// Another lasso: nothing from the old one stands.
    public func reset() {
        notifier?.remove(Array(delivered.keys))
        delivered = [:]
        announced = [:]
        before = nil
    }

    /// The card showed this pane's entry: its notification has been read.
    public func seen(_ p: HostPane) {
        forget(Array(delivered.filter { $0.value.pane == p.key }.keys))
    }

    /// The panel showed the notices.
    public func noticesSeen() {
        forget(Array(delivered.filter { $0.value.pane == nil }.keys))
    }

    private func forget(_ ids: [String]) {
        guard !ids.isEmpty else { return }
        notifier?.remove(ids)
        for id in ids { delivered[id] = nil }
    }

    /// Acknowledged, answered or killed anywhere: its notification goes.
    private func withdraw(keeping keys: Set<String>) {
        forget(Array(delivered.filter { !keys.contains($0.value.key) }.keys))
    }

    /// At the baseline: the notifications a previous run left keep their
    /// place while their entry is still in the inbox; the rest go.
    private func adoptDelivered(_ keys: Set<String>) async {
        guard let notifier else { return }
        var stale: [String] = []
        for (id, t) in await notifier.delivered() {
            let key = Self.key(t)
            if keys.contains(key) {
                if case .entry(let host, let pane, _) = t { delivered[id] = Delivered(key: key, pane: host + "\u{0}" + pane) }
                else { delivered[id] = Delivered(key: key, pane: nil) }
            } else {
                stale.append(id)
            }
        }
        notifier.remove(stale)
    }

    static func key(_ t: NoteTarget) -> String {
        switch t {
        case .entry(let host, let pane, let mark): "\(host)\u{0}\(pane)@\(mark)"
        case .notice(let id): "notice:\(id)"
        }
    }

    private func announce(_ a: InboxArrival) {
        guard notifier != nil else { return }
        Task {
            guard await authorized() else { return }
            let note = await content(a)
            // Still in the inbox as it was, and still not on screen.
            guard let notifier, inbox.arrivalList.contains(where: { $0.key == a.key }), !showing(a) else { return }
            notifier.post(note)
            if case .entry(let e) = a.item {
                delivered[note.id] = Delivered(key: a.key, pane: e.pane.key)
            } else {
                delivered[note.id] = Delivered(key: a.key, pane: nil)
            }
        }
    }

    /// macOS lets this app post. Not asked yet: ask once, after onboarding.
    func authorized() async -> Bool {
        guard let notifier else { return false }
        switch await notifier.authorization() {
        case .granted: return true
        case .denied: return false
        case .notDetermined:
            guard mayAsk() else { return false }
            if asking == nil { asking = Task { await self.notifier?.requestAuthorization() ?? .denied } }
            return await asking?.value == .granted
        }
    }

    // MARK: what a notification says

    private func bot(_ e: InboxEntry) -> BotView? {
        e.bot.flatMap { name in fleet.bots.first { $0.name == name && $0.host == e.pane.host } }
    }

    /// The notification for an arrival, reading the agent's chat when its
    /// words are not in the fleet already (a pane's question or last reply).
    func content(_ a: InboxArrival) async -> UserNote {
        guard case .entry(let e) = a.item else { return Self.note(a, bot: nil, chat: nil, image: nil) }
        let b = bot(e)
        let fromFleet = (e.kind == .waiting && b?.runtime == "omp" && b?.pending_ui?.first != nil) || (e.kind == .finished && b != nil)
        var chat: ChatPayload?
        if !fromFleet { chat = try? await actions.chat(e.pane) }
        let image = e.bot.flatMap(picture).flatMap(NotePicture.file)
        return Self.note(a, bot: b, chat: chat, image: image)
    }

    /// One notification's id: an agent's (a bot's) or a notice's, so a newer
    /// one from the same agent replaces the last.
    static func noteID(_ e: InboxEntry) -> String {
        e.bot.map { "bot:\(e.pane.host)/\($0)" } ?? "agent:\(e.pane.host)/\(e.pane.pane_id)"
    }

    static func note(_ a: InboxArrival, bot b: BotView?, chat: ChatPayload?, image: URL?) -> UserNote {
        switch a.item {
        case .notice(let n):
            let message = n.kind == NotifyKinds.agentMessage
            return UserNote(
                id: "notice:\(n.id)", thread: message ? "lasso.messages" : "lasso.notices", title: n.title,
                subtitle: message ? "Message" : "lasso notice", body: trimmed(n.detail ?? ""), category: .notice,
                target: .notice(n.id))
        case .entry(let e):
            let id = noteID(e)
            let target = NoteTarget.entry(host: e.pane.host, pane: e.pane.pane_id, mark: e.mark)
            let title = e.bot ?? e.pane.displayName
            let subtitle: String
            let body: String
            switch e.kind {
            case .waiting:
                if b?.runtime == "omp", let p = b?.pending_ui?.first {
                    subtitle = "Asking"
                    body = trimmed(p.title ?? p.message ?? "")
                } else if let q = chat.flatMap(openAsk) {
                    subtitle = "Asking"
                    body = trimmed(q.question ?? q.header ?? "")
                } else {
                    subtitle = "Blocked"
                    body = "Waiting on you in the terminal"
                }
            case .finished:
                subtitle = b != nil ? "Message" : "Finished"
                body = lastLine(b?.last_text ?? chat.flatMap(lastReply) ?? "")
            }
            return UserNote(
                id: id, thread: id, title: title, subtitle: subtitle, body: body,
                category: e.kind == .waiting ? .waiting : .finished, target: target, image: image)
        }
    }

    /// The question the agent has open, as the card captures it.
    static func openAsk(_ chat: ChatPayload) -> ChatTool.Question? {
        guard let item = chat.items.last(where: { $0.tool?.ask != nil }), item.tool?.state != "completed",
              item.tool?.state != "error"
        else { return nil }
        return item.tool?.ask?.questions.first
    }

    /// The agent's last words.
    static func lastReply(_ chat: ChatPayload) -> String? {
        chat.items.last { $0.kind == "agent" && $0.thinking != true && !($0.text ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }?
            .text
    }

    /// A reply's last line, without Markdown's leading marks.
    static func lastLine(_ text: String) -> String {
        let line = text.split(whereSeparator: \.isNewline).map { $0.trimmingCharacters(in: .whitespaces) }.last { !$0.isEmpty } ?? ""
        return trimmed(line.replacingOccurrences(of: #"^(?:[#>]+|[-*+]|\d+[.)])\s+"#, with: "", options: .regularExpression))
    }

    /// One paragraph, at most 240 characters.
    static func trimmed(_ text: String) -> String {
        let s = text.split(whereSeparator: \.isWhitespace).joined(separator: " ")
        return s.count > 240 ? String(s.prefix(239)) + "…" : s
    }

    // MARK: the user's answer

    /// A click or an action on a notification.
    public func respond(_ t: NoteTarget, _ action: NoteAction) {
        switch t {
        case .notice(let id):
            switch action {
            case .open: openNotices()
            case .acknowledge:
                if let n = fleet.inboxNotices.first(where: { $0.id == id }) { inbox.acknowledge(n) }
            case .reply: break
            }
        case .entry(let host, let pane, let mark):
            let entry = inbox.entries.first { $0.pane.host == host && $0.pane.pane_id == pane }
            switch action {
            case .open:
                if let entry { openEntry(entry.pane) } else { openInbox() }
            case .acknowledge:
                // Only what the notification said: a newer question is unread.
                guard let entry, entry.mark == mark else { return }
                Task {
                    do {
                        if let acknowledgeEntry {
                            try await acknowledgeEntry(entry)
                        } else {
                            try await actions.acknowledge(entry.pane)
                            fleet.markAcknowledged(entry)
                        }
                        forget([Self.noteID(entry)])
                    } catch {
                        failed(entry, "Not acknowledged", "\(error)")
                    }
                }
            case .reply(let text):
                reply(text, host: host, pane: pane, entry: entry)
            }
        }
    }

    /// Reply from the notification: the composer's send, to a finished
    /// turn only. A pane waiting on you is answered in the card, never typed
    /// into (PLAN 13.9).
    private func reply(_ text: String, host: String, pane: String, entry: InboxEntry?) {
        let text = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        guard let p = fleet.agents.first(where: { $0.host == host && $0.pane_id == pane }) else {
            if let entry { failed(entry, "Reply not sent", "The agent is gone") }
            return
        }
        let shown = entry ?? InboxEntry(pane: p, kind: .finished, bot: fleet.bot(for: p)?.name, ackKey: p.key, ackAt: 0, mark: "", at: 0)
        if entry?.kind == .waiting || p.state == .blocked {
            failed(shown, "Reply not sent", "\(shown.bot ?? p.displayName) is waiting on you: answer it in the inbox")
            return
        }
        Task {
            do {
                let o = try await actions.send(p, text: text)
                if o.ok { forget([Self.noteID(shown)]) } else { failed(shown, "Reply not sent", [o.outcome, o.detail].compactMap { $0 }.joined(separator: ": ")) }
            } catch {
                failed(shown, "Reply not sent", "\(error)")
            }
        }
    }

    /// Say what went wrong in the agent's own notification.
    private func failed(_ e: InboxEntry, _ subtitle: String, _ why: String) {
        guard let notifier else { return }
        let id = Self.noteID(e)
        notifier.post(UserNote(
            id: id, thread: id, title: e.bot ?? e.pane.displayName, subtitle: subtitle, body: Self.trimmed(why),
            category: .notice, target: .entry(host: e.pane.host, pane: e.pane.pane_id, mark: e.mark)))
    }
}
