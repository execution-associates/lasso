import Foundation
import Testing

@testable import LassoDesktopKit

/// A transport that records what it was given and answers as told.
private final class FakeTransport: AnalyticsTransport, @unchecked Sendable {
    enum Mode { case ok, status(Int), fail, hang }

    private let lock = NSLock()
    private var _bodies: [Data] = []
    private var _urls: [URL] = []
    private var _active = 0
    private var _maxActive = 0
    let mode: Mode

    init(_ mode: Mode = .ok) { self.mode = mode }

    var calls: Int { lock.withLock { _bodies.count } }
    var bodies: [Data] { lock.withLock { _bodies } }
    var urls: [URL] { lock.withLock { _urls } }
    var maxActive: Int { lock.withLock { _maxActive } }

    func post(_ body: Data, to url: URL) async throws -> Int {
        lock.withLock {
            _bodies.append(body)
            _urls.append(url)
            _active += 1
            _maxActive = max(_maxActive, _active)
        }
        defer { lock.withLock { _active -= 1 } }
        switch mode {
        case .ok: return 200
        case .status(let s): return s
        case .fail: throw URLError(.cannotFindHost)
        case .hang:
            try await Task.sleep(for: .seconds(3600))
            return 200
        }
    }
}

/// A clock the test moves.
private final class Clock: @unchecked Sendable {
    private let lock = NSLock()
    private var t = Date(timeIntervalSince1970: 1_800_000_000)
    var now: Date { lock.withLock { t } }
    func advance(_ s: TimeInterval) { lock.withLock { t = t.addingTimeInterval(s) } }
}

private let onEnv = ["LASSO_TELEMETRY_KEY": "phc_test", "LASSO_TELEMETRY_HOST": "https://posthog.example.com"]

private func defaults() -> UserDefaults {
    let name = "analytics-test-\(UUID().uuidString)"
    let d = UserDefaults(suiteName: name)!
    d.removePersistentDomain(forName: name)
    return d
}

private func client(
    _ t: FakeTransport, env: [String: String] = onEnv, defaults d: UserDefaults = defaults(), clock: Clock = Clock(), autoFlush: Bool = false
) -> Analytics {
    Analytics(defaults: d, env: env, transport: t, now: { clock.now }, autoFlush: autoFlush)
}

/// Every event, every view, a daily ping and a flush: what a busy app does.
private func exercise(_ a: Analytics) {
    for e in AnalyticsEvent.all { a.track(e) }
    for v in AnalyticsView.allCases { a.view(v) }
    a.dailyActive(hosts: 2, agents: 3, bots: 1)
    a.flush()
}

private func waitFor(_ until: () -> Bool = { false }) async throws {
    for _ in 0..<50 {
        if until() { return }
        try await Task.sleep(for: .milliseconds(20))
    }
}

@Suite struct AnalyticsTests {
    // MARK: off means nothing is sent

    @Test func offByTheSwitchSendsNothing() async throws {
        let t = FakeTransport()
        let d = defaults()
        d.set(false, forKey: AnalyticsConfig.enabledKey)
        let a = client(t, defaults: d, autoFlush: true)
        #expect(!a.active)
        exercise(a)
        try await waitFor()
        #expect(t.calls == 0)
        #expect(a.pendingCount == 0)
        #expect(d.string(forKey: AnalyticsConfig.installIdKey) == nil)
    }

    @Test(arguments: [
        ["DO_NOT_TRACK": "1"], ["DO_NOT_TRACK": "true"], ["LASSO_TELEMETRY": "0"], ["LASSO_TELEMETRY": "off"],
        ["LASSO_TELEMETRY_KEY": ""],
    ])
    func offByTheEnvironmentSendsNothing(_ extra: [String: String]) async throws {
        let t = FakeTransport()
        let a = client(t, env: onEnv.merging(extra) { _, new in new }, autoFlush: true)
        #expect(!a.active)
        #expect(a.forcedOff != nil)
        exercise(a)
        try await waitFor()
        #expect(t.calls == 0)
        #expect(a.pendingCount == 0)
    }

    @Test func inertSendsNothing() async throws {
        let t = FakeTransport()
        let a = Analytics(defaults: defaults(), env: onEnv, transport: t, autoFlush: true, inert: true)
        exercise(a)
        try await waitFor()
        #expect(t.calls == 0)
    }

    @Test func turningItOffDropsWhatWaits() {
        let t = FakeTransport()
        let a = client(t)
        a.track(.onboardingCompleted)
        #expect(a.pendingCount == 1)
        a.enabled = false
        #expect(a.pendingCount == 0)
        a.flush()
        #expect(t.calls == 0)
    }

    @Test func theAppsOwnClientIsInertUnderTest() {
        #expect(Analytics.shared.inert)
        #expect(!Analytics.shared.active)
    }

    // MARK: only what the spec lists

    static let common: Set<String> = ["app", "app_version", "os", "arch", "$lib", "$geoip_disable", "$process_person_profile"]
    static let allowed: [String: [String: Set<String>]] = [
        "install_active": [
            "hosts_count": Set(AnalyticsBucket.hosts), "agents_running": Set(AnalyticsBucket.counts),
            "bots_count": Set(AnalyticsBucket.counts),
        ],
        "view_opened": [
            "view": [
                "terminal", "chat", "grid", "bots", "browser", "files", "scratch", "settings", "simple_inbox", "simple_agents",
                "inbox_card", "agents_column", "bots_column", "agent_window", "bot_window", "terminal_window", "browser_window", "file_window",
                "new_form",
            ]
        ],
        "agent_created": [
            "harness": ["claude", "codex", "opencode", "omp", "pi", "other"], "kind": ["git", "scratch"], "from": ["mac"],
        ],
        "inbox_action": ["action": ["acknowledge", "kill", "reply", "answer", "stop", "rename"], "from": ["mac"]],
        "feature_used": [
            "feature": [
                "shortcut_used", "paste_upload", "terminal_zoom", "browser_new_tab", "plugin_enabled", "theme_changed",
                "voice_input", "file_saved", "bot_created", "agent_pinned",
            ]
        ],
        "onboarding_completed": [:],
    ]

    @Test func everyEventCarriesOnlyAllowedPropertiesAndValues() throws {
        let all = AnalyticsEvent.all
        // Every kind of event is walked.
        #expect(Set(all.map(\.name)) == Set(Self.allowed.keys))
        for e in all {
            let rules = try #require(Self.allowed[e.name], "unknown event \(e.name)")
            let p = Analytics.payload(e)
            for (k, v) in p {
                if Self.common.contains(k) {
                    switch k {
                    case "app": #expect(v as? String == "mac")
                    case "os": #expect(v as? String == "darwin")
                    case "arch": #expect(["arm64", "x86_64"].contains(v as? String ?? ""))
                    case "$lib": #expect(v as? String == "lasso")
                    case "$geoip_disable": #expect(v as? Bool == true)
                    case "$process_person_profile": #expect(v as? Bool == false)
                    case "app_version":
                        let s = v as? String ?? ""
                        #expect(s == "dev" || s.range(of: #"^\d+(\.\d+){0,3}$"#, options: .regularExpression) != nil)
                    default: break
                    }
                    continue
                }
                let ok = try #require(rules[k], "\(e.name) carries \(k)")
                #expect(ok.contains(v as? String ?? "\u{0}"), "\(e.name).\(k) = \(v)")
            }
            // Each of the event's own properties is there.
            #expect(Set(rules.keys).isSubset(of: Set(p.keys)), "\(e.name) misses a property")
        }
    }

    @Test func harnessIdsOutsideTheListAreOther() {
        #expect(AnalyticsHarness(id: "claude") == .claude)
        #expect(AnalyticsHarness(id: "Codex") == .codex)
        #expect(AnalyticsHarness(id: "my-secret-harness") == .other)
    }

    @Test func bucketsNeverCarryTheNumber() {
        #expect(AnalyticsBucket.count(0) == "0")
        #expect(AnalyticsBucket.count(5) == "1-5")
        #expect(AnalyticsBucket.count(6) == "6-20")
        #expect(AnalyticsBucket.count(4000) == "21+")
        #expect(AnalyticsBucket.hosts(0) == "1")
        #expect(AnalyticsBucket.hosts(3) == "2-3")
        #expect(AnalyticsBucket.hosts(9) == "4+")
    }

    // MARK: the wire

    @Test func aBatchIsPostHogsCaptureShape() async throws {
        let t = FakeTransport()
        let a = client(t)
        a.track(.featureUsed(.terminal_zoom))
        a.flush()
        try await waitFor { t.calls == 1 && a.inFlight == 0 }
        #expect(t.urls.first?.absoluteString == "https://posthog.example.com/batch/")
        let body = try #require(t.bodies.first)
        let json = try #require(try JSONSerialization.jsonObject(with: body) as? [String: Any])
        #expect(json["api_key"] as? String == "phc_test")
        let batch = try #require(json["batch"] as? [[String: Any]])
        #expect(batch.count == 1)
        #expect(batch[0]["event"] as? String == "feature_used")
        #expect(UUID(uuidString: batch[0]["distinct_id"] as? String ?? "") != nil)
        #expect((batch[0]["properties"] as? [String: Any])?["feature"] as? String == "terminal_zoom")
        #expect(a.pendingCount == 0)
    }

    @Test func theInstallIdIsStable() {
        let d = defaults()
        let a = client(FakeTransport(), defaults: d)
        a.track(.onboardingCompleted)
        let id = d.string(forKey: AnalyticsConfig.installIdKey)
        #expect(id != nil)
        a.track(.onboardingCompleted)
        #expect(d.string(forKey: AnalyticsConfig.installIdKey) == id)
    }

    // MARK: PostHog down never shows

    @Test(arguments: [500, 503, 404])
    func anErrorStatusDropsTheBatchQuietly(_ status: Int) async throws {
        let t = FakeTransport(.status(status))
        let a = client(t)
        for _ in 0..<5 { a.track(.featureUsed(.shortcut_used)) }
        a.flush()
        try await waitFor { t.calls == 1 && a.inFlight == 0 }
        #expect(t.calls == 1)
        #expect(a.inFlight == 0)
        #expect(a.pendingCount == 0)
        // No retry follows.
        try await waitFor()
        #expect(t.calls == 1)
    }

    @Test func anUnreachableHostDropsTheBatchQuietly() async throws {
        let t = FakeTransport(.fail)
        let a = client(t)
        a.track(.onboardingCompleted)
        a.flush()
        try await waitFor { t.calls == 1 && a.inFlight == 0 }
        #expect(a.inFlight == 0)
        #expect(a.pendingCount == 0)
        a.track(.onboardingCompleted)
        a.flush()
        try await waitFor { t.calls == 2 && a.inFlight == 0 }
        #expect(t.calls == 2)
    }

    @Test func aHangingHostHoldsOneRequestAndABoundedQueue() async throws {
        let t = FakeTransport(.hang)
        let a = client(t)
        a.track(.onboardingCompleted)
        a.flush()
        try await waitFor { t.calls == 1 }
        // Flushes while it hangs start nothing more.
        for _ in 0..<1000 {
            a.track(.featureUsed(.shortcut_used))
            a.flush()
        }
        try await waitFor()
        #expect(t.calls == 1)
        #expect(t.maxActive == 1)
        #expect(a.inFlight == 1)
        #expect(a.pendingCount == AnalyticsConfig.queueCap)
    }

    @Test func theQueueDropsTheOldestPastItsCap() {
        let a = client(FakeTransport())
        a.track(.onboardingCompleted)
        for _ in 0..<(AnalyticsConfig.queueCap + 50) { a.track(.featureUsed(.paste_upload)) }
        #expect(a.pendingCount == AnalyticsConfig.queueCap)
        #expect(!a.pendingNames.contains("onboarding_completed"))
    }

    @Test func aFullBatchFlushesByItselfOneRequestAtATime() async throws {
        let t = FakeTransport()
        let a = client(t, autoFlush: true)
        for _ in 0..<(AnalyticsConfig.batchSize * 3) { a.track(.featureUsed(.shortcut_used)) }
        try await waitFor { a.pendingCount == 0 && a.inFlight == 0 }
        #expect(t.calls >= 1)
        #expect(t.maxActive == 1)
        #expect(a.pendingCount == 0)
    }

    // MARK: throttles

    @Test func aViewIsCountedOncePerTenMinutes() {
        let clock = Clock()
        let a = client(FakeTransport(), clock: clock)
        a.view(.settings)
        a.view(.settings)
        a.view(.agent_window)
        #expect(a.pendingCount == 2)
        clock.advance(AnalyticsConfig.viewThrottle - 1)
        a.view(.settings)
        #expect(a.pendingCount == 2)
        clock.advance(2)
        a.view(.settings)
        #expect(a.pendingCount == 3)
    }

    @Test func installActiveGoesOncePerDay() {
        let clock = Clock()
        let a = client(FakeTransport(), clock: clock)
        a.dailyActive(hosts: 1, agents: 0, bots: 0)
        a.dailyActive(hosts: 1, agents: 2, bots: 0)
        #expect(a.pendingNames == ["install_active"])
        clock.advance(86_400)
        a.dailyActive(hosts: 1, agents: 2, bots: 0)
        #expect(a.pendingNames == ["install_active", "install_active"])
    }

    // MARK: the first-run panel's switch

    @MainActor @Test func theFirstRunSwitchIsTheChoiceAndItsEndIsCountedOnlyWhenOn() {
        let d = defaults()
        let t = FakeTransport()
        let a = client(t, defaults: d)
        let m = FirstRunModel(connector: LassoConnector(probe: { _, _ in .unreachable("x") }, signIn: { _, _ in throw URLError(.cancelled) }), defaults: d)
        m.analytics = a
        #expect(m.shareAnalytics)
        m.setShareAnalytics(false)
        #expect(!a.enabled)
        m.done()
        #expect(a.pendingCount == 0)

        let d2 = defaults()
        let a2 = client(FakeTransport(), defaults: d2)
        let m2 = FirstRunModel(connector: LassoConnector(probe: { _, _ in .unreachable("x") }, signIn: { _, _ in throw URLError(.cancelled) }), defaults: d2)
        m2.analytics = a2
        m2.done()
        #expect(a2.pendingNames == ["onboarding_completed"])
    }

    @MainActor @Test func theSettingsSwitchIsTheChoice() {
        let a = client(FakeTransport())
        let s = SettingsModel(permissions: FixedPermissions([:]), baseURL: { "http://127.0.0.1:8090" })
        s.analytics = a
        #expect(s.analyticsOn)
        #expect(s.analyticsNote == nil)
        s.setAnalytics(false)
        #expect(!a.enabled)
        #expect(!s.analyticsOn)
        let off = Analytics(defaults: defaults(), env: ["DO_NOT_TRACK": "1"], transport: FakeTransport())
        s.analytics = off
        #expect(s.analyticsNote?.contains("DO_NOT_TRACK") == true)
    }
}
