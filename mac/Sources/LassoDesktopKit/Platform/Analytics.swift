import Foundation

// Anonymous usage analytics (PLAN 25): a small, closed set of events posted
// straight to PostHog's capture API, since this app may talk to a lasso it
// does not control. Off means nothing is sent at all. Sending never blocks,
// never retries, never shows an error and never logs above debug: PostHog
// being slow, down or unreachable changes nothing a user can see.

/// Where events go. One place for the host and the project's public
/// ingestion key (`phc_…`, write-only, safe to ship). An empty key sends nothing.
public enum AnalyticsConfig {
    public static let host = "https://posthog.execution.associates"
    public static let key = "phc_opKzDx78nmTsMeZAJmB7PLzFja5eW6AdKiN5pqUSKyUR"
    /// What is collected and why.
    public static let privacyURL = URL(string: "https://www.executionassociates.com/privacy/")!

    /// UserDefaults: the user's choice (absent = on), the install's random
    /// id, and the UTC day `install_active` last went out.
    public static let enabledKey = "analyticsEnabled"
    public static let installIdKey = "analyticsInstallId"
    public static let activeDayKey = "analyticsActiveDay"

    /// At most this many events wait; past it the oldest go.
    public static let queueCap = 200
    /// A batch goes out once this many wait, or `flushDelay` after the first.
    public static let batchSize = 20
    public static let flushDelay: TimeInterval = 30
    /// Request and resource timeout, seconds.
    public static let timeout: TimeInterval = 5
    /// One `view_opened` per view per this long.
    public static let viewThrottle: TimeInterval = 600
}

// MARK: the vocabulary

/// A surface of the app (or the web app: the names are shared).
public enum AnalyticsView: String, CaseIterable, Sendable {
    case terminal, chat, grid, bots, browser, files, scratch, settings
    case simple_inbox, simple_agents, inbox_card, agents_column, bots_column
    case agent_window, bot_window, terminal_window, browser_window, new_form
}

public enum AnalyticsHarness: String, CaseIterable, Sendable {
    case claude, codex, opencode, omp, pi, other

    /// lasso's harness id, anything unknown as `other`.
    public init(id: String) { self = Self(rawValue: id.lowercased()) ?? .other }
}

public enum AnalyticsAgentKind: String, CaseIterable, Sendable { case git, scratch }

public enum AnalyticsInboxAction: String, CaseIterable, Sendable {
    case acknowledge, kill, reply, answer, stop, rename
}

public enum AnalyticsFeature: String, CaseIterable, Sendable {
    case shortcut_used, paste_upload, terminal_zoom, browser_new_tab, plugin_enabled
    case theme_changed, voice_input, file_saved, bot_created, agent_pinned
}

/// Counts as coarse buckets, never the number itself.
public enum AnalyticsBucket {
    public static let hosts = ["1", "2-3", "4+"]
    public static let counts = ["0", "1-5", "6-20", "21+"]

    public static func hosts(_ n: Int) -> String { n <= 1 ? "1" : (n <= 3 ? "2-3" : "4+") }
    public static func count(_ n: Int) -> String { n <= 0 ? "0" : (n <= 5 ? "1-5" : (n <= 20 ? "6-20" : "21+")) }
}

/// Everything the app can send. Nothing else exists.
public enum AnalyticsEvent: Equatable, Sendable {
    case installActive(hosts: Int, agents: Int, bots: Int)
    case viewOpened(AnalyticsView)
    case agentCreated(AnalyticsHarness, AnalyticsAgentKind)
    case inboxAction(AnalyticsInboxAction)
    case featureUsed(AnalyticsFeature)
    case onboardingCompleted

    public var name: String {
        switch self {
        case .installActive: "install_active"
        case .viewOpened: "view_opened"
        case .agentCreated: "agent_created"
        case .inboxAction: "inbox_action"
        case .featureUsed: "feature_used"
        case .onboardingCompleted: "onboarding_completed"
        }
    }

    /// The event's own properties (the common ones are added when sent).
    public var properties: [String: String] {
        switch self {
        case .installActive(let h, let a, let b):
            ["hosts_count": AnalyticsBucket.hosts(h), "agents_running": AnalyticsBucket.count(a), "bots_count": AnalyticsBucket.count(b)]
        case .viewOpened(let v): ["view": v.rawValue]
        case .agentCreated(let h, let k): ["harness": h.rawValue, "kind": k.rawValue, "from": "mac"]
        case .inboxAction(let a): ["action": a.rawValue, "from": "mac"]
        case .featureUsed(let f): ["feature": f.rawValue]
        case .onboardingCompleted: [:]
        }
    }

    /// Every event with every value it can carry (the tests walk these).
    public static var all: [AnalyticsEvent] {
        var out: [AnalyticsEvent] = [.onboardingCompleted]
        for n in [0, 1, 2, 3, 4, 5, 6, 20, 21, 500] { out.append(.installActive(hosts: n, agents: n, bots: n)) }
        out += AnalyticsView.allCases.map { .viewOpened($0) }
        for h in AnalyticsHarness.allCases { for k in AnalyticsAgentKind.allCases { out.append(.agentCreated(h, k)) } }
        out += AnalyticsInboxAction.allCases.map { .inboxAction($0) }
        out += AnalyticsFeature.allCases.map { .featureUsed($0) }
        return out
    }
}

// MARK: the transport

/// Posts one batch; the status code, or a throw. Tests inject a fake.
public protocol AnalyticsTransport: Sendable {
    func post(_ body: Data, to url: URL) async throws -> Int
}

/// URLSession with an ephemeral configuration: no cookies, no cache, no
/// credential store, and short timeouts.
public struct URLSessionAnalyticsTransport: AnalyticsTransport {
    private let session: URLSession

    public init() {
        let c = URLSessionConfiguration.ephemeral
        c.timeoutIntervalForRequest = AnalyticsConfig.timeout
        c.timeoutIntervalForResource = AnalyticsConfig.timeout
        c.httpCookieAcceptPolicy = .never
        c.httpShouldSetCookies = false
        c.urlCache = nil
        c.requestCachePolicy = .reloadIgnoringLocalCacheData
        c.waitsForConnectivity = false
        session = URLSession(configuration: c)
    }

    public func post(_ body: Data, to url: URL) async throws -> Int {
        var req = URLRequest(url: url, timeoutInterval: AnalyticsConfig.timeout)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = body
        let (_, resp) = try await session.data(for: req)
        return (resp as? HTTPURLResponse)?.statusCode ?? 0
    }
}

// MARK: the client

/// The app's analytics: a capped queue, flushed in batches by at most one
/// request at a time, off the main thread. Thread-safe; callable from anywhere.
public final class Analytics: @unchecked Sendable {
    /// The app's. Inert in the test runner and the parity harness (see `inert`).
    public static let shared = Analytics(inert: Bundle.main.bundleIdentifier != "com.orangecountyai.Lasso")

    private let defaults: UserDefaults
    private let env: [String: String]
    private let transport: AnalyticsTransport
    private let now: @Sendable () -> Date
    private let autoFlush: Bool
    private let lock = NSLock()
    private var queue: [[String: Any]] = []
    private var sending = 0
    private var flushScheduled = false
    private var lastView: [AnalyticsView: Date] = [:]
    /// Never sends: a fixture or parity run, a test process.
    private var _inert: Bool

    public init(
        defaults: UserDefaults = .standard, env: [String: String] = ProcessInfo.processInfo.environment,
        transport: AnalyticsTransport = URLSessionAnalyticsTransport(), now: @escaping @Sendable () -> Date = { Date() },
        autoFlush: Bool = true, inert: Bool = false
    ) {
        self.defaults = defaults
        self.env = env
        self.transport = transport
        self.now = now
        self.autoFlush = autoFlush
        _inert = inert
    }

    public var inert: Bool {
        get { lock.withLock { _inert } }
        set {
            lock.withLock {
                _inert = newValue
                if newValue { queue.removeAll() }
            }
        }
    }

    // MARK: on or off

    /// The user's choice (Settings › Info, the first-run panel). Default on.
    public var enabled: Bool {
        get { defaults.object(forKey: AnalyticsConfig.enabledKey) as? Bool ?? true }
        set {
            defaults.set(newValue, forKey: AnalyticsConfig.enabledKey)
            if !newValue { lock.withLock { queue.removeAll() } }
        }
    }

    /// Why analytics is off whatever the choice, in a sentence, or nil.
    public var forcedOff: String? {
        if Self.truthy(env["DO_NOT_TRACK"]) { return "Off: DO_NOT_TRACK is set in this app's environment." }
        if let v = env["LASSO_TELEMETRY"]?.lowercased(), ["0", "false", "off", "no"].contains(v) {
            return "Off: LASSO_TELEMETRY is off in this app's environment."
        }
        if key.isEmpty { return "Off in this build." }
        return nil
    }

    /// Whether anything would be sent now.
    public var active: Bool { !inert && enabled && forcedOff == nil }

    /// The project key: LASSO_TELEMETRY_KEY when set (empty sends nothing), else the build's.
    var key: String { env["LASSO_TELEMETRY_KEY"] ?? AnalyticsConfig.key }

    var endpoint: URL? {
        let h = (env["LASSO_TELEMETRY_HOST"].flatMap { $0.isEmpty ? nil : $0 }) ?? AnalyticsConfig.host
        guard var c = URLComponents(string: h), c.scheme == "https" || c.scheme == "http", c.host != nil else { return nil }
        c.path = (c.path.hasSuffix("/") ? String(c.path.dropLast()) : c.path) + "/batch/"
        return c.url
    }

    private static func truthy(_ v: String?) -> Bool {
        guard let v = v?.lowercased() else { return false }
        return ["1", "true", "yes"].contains(v)
    }

    /// The install's random id, made the first time one is needed.
    var installID: String {
        if let id = defaults.string(forKey: AnalyticsConfig.installIdKey), UUID(uuidString: id) != nil { return id }
        let id = UUID().uuidString.lowercased()
        defaults.set(id, forKey: AnalyticsConfig.installIdKey)
        return id
    }

    // MARK: what is sent

    /// Sent with every event.
    public static var common: [String: Any] {
        let v = (Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String) ?? "dev"
        #if arch(arm64)
            let arch = "arm64"
        #else
            let arch = "x86_64"
        #endif
        return [
            "app": "mac", "app_version": v, "os": "darwin", "arch": arch, "$lib": "lasso",
            "$geoip_disable": true, "$process_person_profile": false,
        ]
    }

    /// An event's full properties as sent.
    public static func payload(_ e: AnalyticsEvent) -> [String: Any] {
        var p = common
        for (k, v) in e.properties { p[k] = v }
        return p
    }

    // MARK: tracking

    public func track(_ e: AnalyticsEvent) {
        guard active else { return }
        let stamp = ISO8601DateFormatter().string(from: now())
        let item: [String: Any] = ["event": e.name, "distinct_id": installID, "timestamp": stamp, "properties": Self.payload(e)]
        let (flushNow, schedule) = lock.withLock { () -> (Bool, Bool) in
            queue.append(item)
            if queue.count > AnalyticsConfig.queueCap { queue.removeFirst(queue.count - AnalyticsConfig.queueCap) }
            if queue.count >= AnalyticsConfig.batchSize { return (true, false) }
            if flushScheduled { return (false, false) }
            flushScheduled = true
            return (false, true)
        }
        guard autoFlush else { return }
        if flushNow {
            flush()
        } else if schedule {
            later()
        }
    }

    /// A flush `flushDelay` from now (the caller set `flushScheduled`).
    private func later() {
        DispatchQueue.global(qos: .utility).asyncAfter(deadline: .now() + AnalyticsConfig.flushDelay) { [weak self] in
            guard let self else { return }
            self.lock.withLock { self.flushScheduled = false }
            self.flush()
        }
    }

    /// A surface opened: at most once per view per ten minutes.
    public func view(_ v: AnalyticsView) {
        guard active else { return }
        let t = now()
        let fresh = lock.withLock { () -> Bool in
            if let last = lastView[v], t.timeIntervalSince(last) < AnalyticsConfig.viewThrottle { return false }
            lastView[v] = t
            return true
        }
        if fresh { track(.viewOpened(v)) }
    }

    /// `install_active`, once per UTC day per install.
    public func dailyActive(hosts: Int, agents: Int, bots: Int) {
        guard active else { return }
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = TimeZone(identifier: "UTC")!
        let d = cal.dateComponents([.year, .month, .day], from: now())
        let day = String(format: "%04d-%02d-%02d", d.year ?? 0, d.month ?? 0, d.day ?? 0)
        guard defaults.string(forKey: AnalyticsConfig.activeDayKey) != day else { return }
        defaults.set(day, forKey: AnalyticsConfig.activeDayKey)
        track(.installActive(hosts: hosts, agents: agents, bots: bots))
    }

    /// Send what waits, unless a request is already out: one at a time, so a
    /// slow or hanging PostHog never piles up tasks. A failure drops the batch.
    public func flush() {
        guard active, let url = endpoint else { return }
        let key = key
        let batch: [[String: Any]]? = lock.withLock {
            guard sending == 0, !queue.isEmpty else { return nil }
            let n = min(queue.count, AnalyticsConfig.batchSize * 5)
            let b = Array(queue.prefix(n))
            queue.removeFirst(n)
            sending += 1
            return b
        }
        guard let batch, let body = try? JSONSerialization.data(withJSONObject: ["api_key": key, "batch": batch]) else { return }
        let transport = transport
        let count = batch.count
        Task.detached(priority: .utility) { [weak self] in
            let note: String
            do {
                note = "status \(try await transport.post(body, to: url))"
            } catch {
                note = "failed"
            }
            self?.finished(note, count: count)
        }
    }

    private func finished(_ note: String, count: Int) {
        let (more, schedule) = lock.withLock { () -> (Bool, Bool) in
            sending -= 1
            if queue.count >= AnalyticsConfig.batchSize { return (true, false) }
            guard !queue.isEmpty, !flushScheduled else { return (false, false) }
            flushScheduled = true
            return (false, true)
        }
        log.debug("analytics: \(count, privacy: .public) events, \(note, privacy: .public)")
        guard autoFlush else { return }
        if more { flush() } else if schedule { later() }
    }

    // MARK: tests

    var pendingCount: Int { lock.withLock { queue.count } }
    var inFlight: Int { lock.withLock { sending } }
    var pendingNames: [String] { lock.withLock { queue.compactMap { $0["event"] as? String } } }
}
