import Foundation

/// One server-sent event.
public struct SSEEvent: Equatable, Sendable {
    public var event: String
    public var data: String
}

/// Server-sent events framing (the WHATWG rules lasso's /api/events follows):
/// `event:` and `data:` lines, a blank line dispatches, `:` starts a comment.
public struct SSEParser: Sendable {
    private var event = ""
    private var data: [String] = []

    public init() {}

    public mutating func feed(line raw: String) -> SSEEvent? {
        let line = raw.hasSuffix("\r") ? String(raw.dropLast()) : raw
        if line.isEmpty {
            defer {
                event = ""
                data = []
            }
            guard !data.isEmpty || !event.isEmpty else { return nil }
            return SSEEvent(event: event.isEmpty ? "message" : event, data: data.joined(separator: "\n"))
        }
        if line.hasPrefix(":") { return nil }
        let (field, value): (Substring, Substring)
        if let i = line.firstIndex(of: ":") {
            field = line[..<i]
            var v = line[line.index(after: i)...]
            if v.hasPrefix(" ") { v = v.dropFirst() }
            value = v
        } else {
            field = Substring(line)
            value = ""
        }
        switch field {
        case "event": event = String(value)
        case "data": data.append(String(value))
        default: break
        }
        return nil
    }
}

/// /api/events with reconnects. The stream is a hint ("something changed,
/// refetch"); the poll floor in FleetSync keeps the app correct when it is
/// down. Silence past the watchdog (the server pings every 25 s) counts as
/// dead.
public final class EventStream: @unchecked Sendable {
    public enum Status: Equatable, Sendable {
        case connecting
        case live
        case down(String)
    }

    private let client: LassoClient
    private let clientID: String
    /// The host whose state the stream carries ("local" or an ssh alias).
    private let host: String
    private let onEvent: @Sendable (SSEEvent) -> Void
    private let onStatus: @Sendable (Status) -> Void
    private var task: Task<Void, Never>?

    public init(
        client: LassoClient, clientID: String, host: String = "local", onEvent: @escaping @Sendable (SSEEvent) -> Void,
        onStatus: @escaping @Sendable (Status) -> Void
    ) {
        self.client = client
        self.clientID = clientID
        self.host = host
        self.onEvent = onEvent
        self.onStatus = onStatus
    }

    public func start() {
        task = Task { [client, clientID, host, onEvent, onStatus] in
            let watchdog = Theme.Live.sseWatchdog.seconds
            let backoff = Theme.Live.backoff
            var delay = Double(backoff.number("min"))
            while !Task.isCancelled {
                onStatus(.connecting)
                let started = Date()
                do {
                    try await EventStream.consume(
                        client: client, clientID: clientID, host: host, watchdog: watchdog, onEvent: onEvent,
                        onLive: { onStatus(.live) })
                    onStatus(.down("stream ended"))
                } catch is CancellationError {
                    return
                } catch {
                    onStatus(.down(String(describing: error)))
                }
                // A connection that stayed up a while resets the backoff.
                if Date().timeIntervalSince(started) > 30 { delay = Double(backoff.number("min")) }
                let jitter = Double(backoff.number("jitter"))
                let wait = delay * (1 + Double.random(in: -jitter...jitter))
                try? await Task.sleep(nanoseconds: UInt64(max(0.2, wait) * 1e9))
                delay = min(Double(backoff.number("max")), delay * 2)
            }
        }
    }

    public func stop() { task?.cancel() }

    struct Watchdog: Error, CustomStringConvertible {
        var description: String { "no event or ping within the watchdog" }
    }

    private static func consume(
        client: LassoClient, clientID: String, host: String, watchdog: Double, onEvent: @escaping @Sendable (SSEEvent) -> Void,
        onLive: @escaping @Sendable () -> Void
    ) async throws {
        // The stream carries the token like any request; a refusal refreshes
        // it for the reconnect.
        await client.auth?.freshen()
        // `app=mac` is what makes an agent's open_file with app "mac" reach
        // this stream, and one with app "web" skip it.
        let req = client.streamRequest("/api/events", query: ["host": host, "client": clientID, "app": "mac"])
        let last = LastSeen()
        try await withThrowingTaskGroup(of: Void.self) { group in
            group.addTask {
                let (bytes, resp) = try await URLSession.shared.bytes(for: req)
                let code = (resp as? HTTPURLResponse)?.statusCode ?? 0
                guard code == 200 else {
                    if code == 401 { _ = await client.auth?.refreshAfterRefusal() }
                    throw LassoError.http(code, "event stream")
                }
                onLive()
                var parser = SSEParser()
                var line: [UInt8] = []
                // Split on \n ourselves: AsyncLineSequence drops the blank
                // lines that delimit events.
                for try await b in bytes {
                    last.touch()
                    if b == UInt8(ascii: "\n") {
                        if let e = parser.feed(line: String(decoding: line, as: UTF8.self)) { onEvent(e) }
                        line.removeAll(keepingCapacity: true)
                    } else {
                        line.append(b)
                    }
                }
            }
            group.addTask {
                while true {
                    try await Task.sleep(nanoseconds: 5_000_000_000)
                    if last.age > watchdog { throw Watchdog() }
                }
            }
            try await group.next()
            group.cancelAll()
        }
    }
}

private final class LastSeen: @unchecked Sendable {
    private let lock = NSLock()
    private var at = Date()

    func touch() {
        lock.lock()
        at = Date()
        lock.unlock()
    }

    var age: Double {
        lock.lock()
        defer { lock.unlock() }
        return Date().timeIntervalSince(at)
    }
}
