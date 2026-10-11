import Combine
import Foundation
import os

private let syncLog = Logger(subsystem: "com.orangecountyai.LassoDesktop", category: "sync")

/// Keeps a FleetModel in step with a live lasso: the event stream says when
/// something changed, and a poll floor refetches anyway, so a dead stream
/// costs freshness, never correctness. Agent status changes are not in the
/// stream at all (only the stream host's layout is), which is why the web
/// client polls /api/all-panes too (src/web/src/lib/agents.ts:337).
@MainActor
public final class FleetSync: ObservableObject {
    @Published public private(set) var stream: EventStream.Status = .connecting
    @Published public private(set) var lastError: String?
    @Published public private(set) var version: VersionInfo?

    public let client: LassoClient
    public let fleet: FleetModel
    /// Told when lasso says a bot's chat changed (one-shot `bot-chat`).
    public var onBotChat: (String) -> Void = { _ in }

    private var events: EventStream?
    private var loops: [Task<Void, Never>] = []
    private var revs = ActiveFrame()
    private let clientID = "lasso-desktop-" + UUID().uuidString.prefix(8).lowercased()

    public init(client: LassoClient, fleet: FleetModel) {
        self.client = client
        self.fleet = fleet
    }

    public func start() {
        let es = EventStream(
            client: client, clientID: clientID,
            onEvent: { [weak self] e in Task { @MainActor in self?.handle(e) } },
            onStatus: { [weak self] s in Task { @MainActor in self?.stream = s } })
        events = es
        es.start()
        loops = [
            every(Theme.Live.pollAllPanes.seconds) { await $0.refreshPanes() },
            every(Theme.Live.pollBots.seconds) { await $0.refreshBots() },
            every(30) { await $0.refreshUIState() },
        ]
        Task { await refreshVersion() }
    }

    public func stop() {
        events?.stop()
        loops.forEach { $0.cancel() }
    }

    /// Talk to another lasso from now on: the fleet empties and refills from
    /// it, and every live action follows, since they share this client.
    public func retarget(_ url: URL) {
        stop()
        client.retarget(url)
        fleet.clear()
        revs = ActiveFrame()
        version = nil
        lastError = nil
        stream = .connecting
        start()
    }

    private func every(_ seconds: Double, _ body: @escaping (FleetSync) async -> Void) -> Task<Void, Never> {
        Task { [weak self] in
            while !Task.isCancelled {
                guard let self else { return }
                await body(self)
                try? await Task.sleep(nanoseconds: UInt64(seconds * 1e9))
            }
        }
    }

    private func handle(_ e: SSEEvent) {
        switch e.event {
        case "active":
            guard let f = try? JSONDecoder().decode(ActiveFrame.self, from: Data(e.data.utf8)) else { return }
            if f.panes_rev != revs.panes_rev { Task { await refreshPanes() } }
            if f.ui_state_rev != revs.ui_state_rev { Task { await refreshUIState() } }
            revs = f
        case "notice":
            if let n = try? JSONDecoder().decode(InboxNotice.self, from: Data(e.data.utf8)) { fleet.add(notice: n) }
        case "bot-chat":
            struct B: Decodable { var bot: String }
            Task { await refreshBots() }
            if let b = try? JSONDecoder().decode(B.self, from: Data(e.data.utf8)) { onBotChat(b.bot) }
        default:
            break
        }
    }

    private func note(_ error: Error, _ what: String) {
        lastError = "\(what): \(error)"
        syncLog.error("\(what, privacy: .public) failed: \(String(describing: error), privacy: .public)")
    }

    public func refreshPanes() async {
        do {
            fleet.apply(panes: try await client.get("/api/all-panes", as: AllPanes.self))
            lastError = nil
        } catch { note(error, "all-panes") }
    }

    public func refreshBots() async {
        do { fleet.apply(bots: try await client.get("/api/bots", as: BotsPayload.self)) } catch { note(error, "bots") }
    }

    public func refreshUIState() async {
        do { fleet.apply(uiState: try await client.get("/api/ui-state", as: UIStateSlice.self)) } catch {
            note(error, "ui-state")
        }
    }

    public func refreshVersion() async {
        do { version = try await client.get("/api/version", as: VersionInfo.self) } catch { note(error, "version") }
    }
}
