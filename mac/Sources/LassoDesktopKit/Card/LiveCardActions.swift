import AppKit
import CoreGraphics
import UniformTypeIdentifiers

/// A card's side effects through lasso's API (PLAN 3). Each is a real change
/// on the user's machine, made only from a key or click the user gave.
@MainActor
public final class LiveCardActions: CardActions {
    let client: LassoClient
    let fleet: FleetModel
    var refreshUIState: () async -> Void = {}

    public init(client: LassoClient, fleet: FleetModel) {
        self.client = client
        self.fleet = fleet
    }

    public var canAcknowledge: Bool { fleet.uiState.dismissed_cards != nil }

    struct Rename: Encodable {
        var pane_id: String
        var name: String
    }
    struct Renamed: Decodable { var ok: Bool? }

    /// POST /api/agent/rename, then the new name everywhere at once.
    public func rename(_ pane: HostPane, to name: String) async throws {
        let _: Renamed = try await client.post("/api/agent/rename", query: ["host": pane.host], json: Rename(pane_id: pane.pane_id, name: name))
        fleet.relabel(pane.key, name.isEmpty ? nil : name)
        Analytics.shared.track(.inboxAction(.rename))
    }

    /// The omp bot running in this pane: its chat, messages and questions go
    /// through the bot's own routes, which see its pending requests.
    private func ompBot(_ pane: HostPane) -> BotView? {
        fleet.bot(for: pane).flatMap { $0.runtime == "omp" ? $0 : nil }
    }

    private func botPath(_ b: BotView, _ tail: String) -> String {
        "/api/bots/\(b.name.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? b.name)/\(tail)"
    }

    public func chat(_ pane: HostPane, before: Int64?) async throws -> ChatPayload {
        try await chat(pane, incoming: false, before: before)
    }

    /// `incoming` adds the messages Claude Code channels delivered (mail,
    /// texts, a bot's scheduled loops) as `incoming` rows: a bot's chat
    /// reads them, as the web's Bots view does (`?incoming=1`).
    public func chat(_ pane: HostPane, incoming: Bool, before: Int64? = nil) async throws -> ChatPayload {
        var q = before.map { ["before": String($0)] } ?? [:]
        if let b = ompBot(pane) { return try await client.get(botPath(b, "chat"), query: q) }
        q["pane"] = pane.pane_id
        q["host"] = pane.host
        if incoming { q["incoming"] = "1" }
        return try await client.get("/api/chat", query: q)
    }

    struct SendBody: Encodable {
        var pane_id: String
        var text: String
    }

    struct BotSend: Encodable { var text: String }

    public func send(_ pane: HostPane, text: String) async throws -> Outcome {
        let o: Outcome =
            if let b = ompBot(pane) {
                try await client.post(botPath(b, "chat/send"), json: BotSend(text: text))
            } else {
                try await client.post("/api/chat/send", query: ["host": pane.host], json: SendBody(pane_id: pane.pane_id, text: text))
            }
        return Self.counted(o, .reply)
    }

    /// An answer that went through counts as that inbox action (PLAN 25).
    static func counted(_ o: Outcome, _ a: AnalyticsInboxAction) -> Outcome {
        if o.ok { Analytics.shared.track(.inboxAction(a)) }
        return o
    }

    struct BotAnswer: Encodable {
        var id: String
        var value: String?
        var confirmed: Bool?
    }

    public func answer(_ pane: HostPane, pending: ChatPayload.PendingUI, value: String) async throws -> Outcome {
        guard let b = ompBot(pane) else { throw LassoError.http(409, "not an omp bot's pane") }
        let body =
            pending.method == "confirm"
            ? BotAnswer(id: pending.id, value: nil, confirmed: value == "Yes") : BotAnswer(id: pending.id, value: value, confirmed: nil)
        return Self.counted(try await client.post(botPath(b, "chat/answer"), json: body), .answer)
    }

    struct AnswerBody: Encodable {
        struct Pick: Encodable {
            var selected: [Int]
            var multi: Bool
            var options: Int
            /// Free text for the dialog's Other row (S4).
            var custom: String?
            /// The question's own name for that row, sent with `custom`: a
            /// plan's feedback field is typed into only while it still shows
            /// that placeholder.
            var other: String?
        }

        var pane_id: String
        var expect: String
        var labels: [String]
        var answers: [Pick]
    }

    public func answer(_ pane: HostPane, question: ChatTool.Question, option: Int?, custom: String?) async throws -> Outcome {
        let labels = (question.options ?? []).map(\.label)
        let body = AnswerBody(
            pane_id: pane.pane_id, expect: question.question ?? "", labels: labels,
            answers: [.init(selected: option.map { [$0] } ?? [], multi: question.multi ?? false, options: labels.count, custom: custom,
                other: custom == nil ? nil : question.other)])
        return Self.counted(try await client.post("/api/chat/answer", query: ["host": pane.host], json: body), .answer)
    }

    struct AgentClose: Encodable {
        var pane_id: String
        var agent_id: String
        var host: String
        var remove_worktree: Bool
    }

    struct AgentClosed: Decodable {
        var agent_killed: Bool?
        var pane_closed: Bool?
    }

    struct PaneClose: Encodable { var pane_ids: [String] }

    struct PaneClosed: Decodable {
        var closed: [String]?
        var errors: [String: String]?
    }

    /// A lasso agent (it has a record) is closed through its record, which
    /// keeps the record honest; any other agent pane is closed as a pane.
    public func kill(_ pane: HostPane) async throws -> String {
        // lasso keeps a bot running; ending one is the Bots view's Stop.
        if let b = fleet.bot(for: pane) { return "\(b.name) is a bot: stop it from lasso's Bots view" }
        if let id = pane.agent_id, !id.isEmpty {
            let r: AgentClosed = try await client.post(
                "/api/agent/close",
                json: AgentClose(pane_id: pane.pane_id, agent_id: id, host: pane.host, remove_worktree: false))
            Analytics.shared.track(.inboxAction(.kill))
            return r.agent_killed == true || r.pane_closed == true ? "Killed \(pane.displayName)" : "Nothing to kill"
        }
        let r: PaneClosed = try await client.post("/api/close", query: ["host": pane.host], json: PaneClose(pane_ids: [pane.pane_id]))
        if let err = r.errors?[pane.pane_id] { throw LassoError.http(502, err) }
        Analytics.shared.track(.inboxAction(.kill))
        return "Closed \(pane.displayName)"
    }

    struct AckWrite: Encodable {
        var client_id = "lasso-desktop"
        var card_dismiss: [String: Int64]
    }

    /// Acknowledge by the inbox entry's own key (PLAN 13.6): a pane by its
    /// transcript, an omp bot's question by its id, a bot's reply by its time.
    public func acknowledge(_ pane: HostPane) async throws {
        let e = fleet.inbox.first { $0.id == pane.key }
        let _: UIStateSlice = try await client.post(
            "/api/ui-state", json: AckWrite(card_dismiss: [e?.ackKey ?? pane.key: e?.ackAt ?? pane.transcript_at ?? 0]))
        Analytics.shared.track(.inboxAction(.acknowledge))
        await refreshUIState()
    }

    struct PaneStop: Encodable { var pane_id: String }
    struct Empty: Encodable {}

    /// POST /api/chat/stop on the pane's host, or an omp bot's own stop route.
    public func stop(_ pane: HostPane) async throws -> Outcome {
        let o: Outcome =
            if let b = ompBot(pane) {
                try await client.post(botPath(b, "chat/stop"), json: Empty())
            } else {
                try await client.post("/api/chat/stop", query: ["host": pane.host], json: PaneStop(pane_id: pane.pane_id))
            }
        return Self.counted(o, .stop)
    }

    struct Focus: Encodable { var pane_id: String }
    struct Focused: Decodable { var ok: Bool? }

    public func focus(_ pane: HostPane) async throws {
        let _: Focused = try await client.post("/api/focus", query: ["host": pane.host], json: Focus(pane_id: pane.pane_id))
    }

    /// Upload a file to the agent's machine (lasso's paste-file route, as the
    /// web client's drop does) and hand back the path to put in the message.
    public func attach(_ file: URL, to pane: HostPane) async throws -> String {
        try await upload(file, host: pane.host)
    }

    func upload(_ file: URL, host: String) async throws -> String {
        try await client.pasteFile(file, host: host)
    }
}

extension LassoClient {
    struct Pasted: Decodable {
        var path: String
        /// Set while parts are still to come: the token the next part names.
        var upload: String?
        var received: Int64?
    }

    /// The part size a big file is sent in (src/pasteupload.go
    /// pasteChunkBytes): Cloudflare refuses a request body over 100 MB in
    /// front of lasso, so a screen recording sent whole never arrives.
    static let pasteChunkBytes = 50 << 20

    /// Upload a file to `host`'s machine (lasso's paste-file route) and
    /// answer the path it was written to there. A file over one part goes
    /// in parts, each read from disk as it is sent.
    func pasteFile(_ file: URL, host: String, chunk: Int = pasteChunkBytes) async throws -> String {
        let type = UTType(filenameExtension: file.pathExtension)?.preferredMIMEType ?? "application/octet-stream"
        func post(_ query: [String: String]) -> URLRequest {
            var req = request("/api/paste-file", query: query.merging(["host": host]) { a, _ in a }, method: "POST")
            req.setValue(type, forHTTPHeaderField: "Content-Type")
            return req
        }
        let size = Int64((try? file.resourceValues(forKeys: [.fileSizeKey]).fileSize) ?? 0)
        let name = file.lastPathComponent
        guard size > chunk else {
            let r: Pasted = try await upload(post(["name": name]), fromFile: file)
            Analytics.shared.track(.featureUsed(.paste_upload))
            return r.path
        }
        let h = try FileHandle(forReadingFrom: file)
        defer { try? h.close() }
        var r: Pasted = try await upload(
            post(["name": name, "size": String(size)]), from: h.read(upToCount: chunk) ?? Data())
        while let tok = r.upload, let off = r.received {
            try h.seek(toOffset: UInt64(off))
            r = try await upload(
                post(["upload": tok, "offset": String(off)]), from: h.read(upToCount: chunk) ?? Data())
        }
        Analytics.shared.track(.featureUsed(.paste_upload))
        return r.path
    }
}

/// The parity fixtures' actions: the chat comes from the fixture, and every
/// side effect is a recorded no-op.
@MainActor
public final class FixtureCardActions: CardActions {
    public var chats: [String: ChatPayload]
    public private(set) var calls: [String] = []
    public let canAcknowledge: Bool

    public init(chats: [String: ChatPayload], canAcknowledge: Bool) {
        self.chats = chats
        self.canAcknowledge = canAcknowledge
    }

    /// The pages above the fixture's chat, by the offset each ends at; a
    /// read of any other fails, as a dropped connection would.
    public var olderPages: [Int64: ChatPayload] = [:]
    /// The `before` of each read of a page above, in order.
    public private(set) var befores: [Int64] = []

    public func chat(_ pane: HostPane, before: Int64?) async throws -> ChatPayload {
        guard let before else {
            return chats[pane.key] ?? ChatPayload(pane_id: pane.pane_id, agent: pane.agent, host: pane.host, items: [])
        }
        befores.append(before)
        guard let page = olderPages[before] else { throw FixtureError(message: "no page before \(before)") }
        return page
    }

    public func rename(_ pane: HostPane, to name: String) async throws { calls.append("rename:\(name)") }

    /// What a send answers (a test sets "refused").
    public var sendOutcome = "confirmed"

    /// The text of each send, in order.
    public private(set) var sent: [String] = []

    public func send(_ pane: HostPane, text: String) async throws -> Outcome {
        calls.append("send")
        sent.append(text)
        return Outcome(outcome: sendOutcome)
    }

    public func answer(_ pane: HostPane, question: ChatTool.Question, option: Int?, custom: String?) async throws -> Outcome {
        calls.append(custom == nil ? "answer" : "answer-custom")
        return Outcome(outcome: "sent")
    }

    public func stop(_ pane: HostPane) async throws -> Outcome {
        calls.append("stop")
        return stopOutcome
    }

    /// What the fixture's stop answers.
    public var stopOutcome = Outcome(outcome: "sent")

    public func kill(_ pane: HostPane) async throws -> String {
        calls.append("kill")
        if killWait > 0 { try await Task.sleep(nanoseconds: killWait) }
        if let killError { throw FixtureError(message: killError) }
        return "fixture"
    }

    /// How long the fixture's kill takes, and why it fails (nil: it succeeds).
    public var killWait: UInt64 = 0
    public var killError: String?

    public struct FixtureError: Error, CustomStringConvertible {
        public var message: String
        public var description: String { message }
    }

    public func acknowledge(_ pane: HostPane) async throws { calls.append("acknowledge") }

    public func focus(_ pane: HostPane) async throws { calls.append("focus") }


    public func answer(_ pane: HostPane, pending: ChatPayload.PendingUI, value: String) async throws -> Outcome {
        calls.append("answer")
        return Outcome(outcome: "sent")
    }

    public func attach(_ file: URL, to pane: HostPane) async throws -> String {
        calls.append("attach")
        return "/fixture/" + file.lastPathComponent
    }
}
