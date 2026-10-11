import Foundation

/// `LassoDesktop --smoke [seconds]`: read-only checks against the configured
/// lasso. Every request is a GET; nothing here changes the user's machine. It
/// reports shapes and counts, never content (no pane titles, no chat text).
public enum Smoke {
    public static func run(seconds: Double) async -> Int32 {
        let client = LassoClient.configured()
        var failures = 0
        func out(_ s: String) { FileHandle.standardOutput.write(Data((s + "\n").utf8)) }
        func check<T: Decodable>(_ path: String, _ type: T.Type, _ summary: (T) -> String) async {
            do {
                out("ok   \(path)  \(summary(try await client.get(path, as: type)))")
            } catch {
                failures += 1
                out("FAIL \(path)  \(error)")
            }
        }
        out("lasso at \(client.baseURL.absoluteString)")
        await check("/api/version", VersionInfo.self) { "lasso \($0.lasso_version), protocol \($0.lasso_protocol)" }
        await check("/api/hosts", HostsPayload.self) {
            "\($0.hosts.count + 1) hosts, \($0.hosts.filter { $0.reachable && $0.running && $0.compatible }.count + 1) usable"
        }
        await check("/api/all-panes", AllPanes.self) { p in
            let agents = p.panes.filter(\.has_agent)
            let by = Dictionary(grouping: agents, by: \.state).map { "\($0.key.rawValue) \($0.value.count)" }.sorted()
            return "\(p.panes.count) panes, \(agents.count) agents (\(by.joined(separator: ", ")))"
        }
        // The card's read path on real conversations: a few agents' chats,
        // reported as counts only.
        if let panes = try? await client.get("/api/all-panes", as: AllPanes.self) {
            for p in panes.panes.filter({ $0.has_agent && $0.transcript_at != nil }).prefix(4) {
                do {
                    let c = try await client.get("/api/chat", query: ["pane": p.pane_id, "host": p.host], as: ChatPayload.self)
                    let kinds = Dictionary(grouping: c.items, by: \.kind).map { "\($0.key) \($0.value.count)" }.sorted()
                    out("ok   /api/chat (\(p.agent) on \(p.host))  \(c.items.count) items (\(kinds.joined(separator: ", ")))\(c.unavailable.map { "; unavailable: \($0.reason)" } ?? "")")
                } catch {
                    failures += 1
                    out("FAIL /api/chat (\(p.agent) on \(p.host))  \(error)")
                }
            }
        }
        await check("/api/ui-state", UIStateSlice.self) {
            "\($0.pinned_agents?.count ?? 0) pins; assistant_bot \($0.assistant_bot == nil ? "absent (server lacks S1)" : "present"); dismissed_cards \($0.dismissed_cards == nil ? "absent" : "present")"
        }
        do {
            let t = try await client.get("/api/transcribe", as: TranscribeStatus.self)
            out("ok   /api/transcribe  available \(t.available), provider \(t.provider ?? "-")\(t.reason.map { " (\($0))" } ?? "")")
        } catch LassoError.http(404, _) {
            out("ok   /api/transcribe  absent (server lacks S2)")
        } catch {
            failures += 1
            out("FAIL /api/transcribe  \(error)")
        }
        await check("/api/bots", BotsPayload.self) { b in
            "\(b.bots.count) bots (\(b.bots.map { "\($0.runtime)/\($0.state)" }.sorted().joined(separator: ", ")))"
        }

        let counts = Counts()
        let stream = EventStream(
            client: client, clientID: "lasso-desktop-smoke",
            onEvent: { counts.add($0.event) }, onStatus: { counts.status($0) })
        stream.start()
        try? await Task.sleep(nanoseconds: UInt64(seconds * 1e9))
        stream.stop()
        let (events, live) = counts.snapshot()
        if live {
            out("ok   /api/events  \(events.isEmpty ? "connected, no events in \(Int(seconds))s" : events.map { "\($0.key) \($0.value)" }.sorted().joined(separator: ", "))")
        } else {
            failures += 1
            out("FAIL /api/events  never connected")
        }
        out(failures == 0 ? "smoke: all GETs ok" : "smoke: \(failures) failed")
        return failures == 0 ? 0 : 1
    }
}

private final class Counts: @unchecked Sendable {
    private let lock = NSLock()
    private var events: [String: Int] = [:]
    private var live = false

    func add(_ e: String) {
        lock.lock()
        events[e, default: 0] += 1
        lock.unlock()
    }

    func status(_ s: EventStream.Status) {
        lock.lock()
        if s == .live { live = true }
        lock.unlock()
    }

    func snapshot() -> ([String: Int], Bool) {
        lock.lock()
        defer { lock.unlock() }
        return (events, live)
    }
}
