import Foundation
import Testing

@testable import LassoDesktopKit

/// What a connector and the first-run panel did, recorded.
@MainActor
private final class Log {
    var probes: [(URL, String?)] = []
    var used: [URL] = []
    var signedIn: [URL] = []
    var passwords: [(URL, String)] = []
    var kept: [String] = []
    var grants: [URL] = []
}

private let grant = OAuthGrant(
    clientID: "c1", tokenEndpoint: URL(string: "https://auth.example.com/token")!, resource: "https://lasso.example.com",
    accessToken: "t", refreshToken: "r", expiresAt: nil)

/// A connector over a canned network: `answer` says what each probe finds.
@MainActor
private func connector(
    _ log: Log, signIn: @escaping LassoConnector.SignIn = { _, _ in grant },
    answer: @escaping (URL, String?) -> LassoAccess
) -> LassoConnector {
    let c = LassoConnector(
        probe: { url, basic in
            log.probes.append((url, basic))
            return answer(url, basic)
        }, signIn: signIn)
    c.onUse = { log.used.append($0) }
    c.onSignedIn = { url, _ in log.signedIn.append(url) }
    c.onPassword = { url, pair in log.passwords.append((url, pair)) }
    c.keepPassword = { log.kept.append($0) }
    c.keepGrant = { _, url in log.grants.append(url) }
    return c
}

private func freshDefaults() -> UserDefaults { UserDefaults(suiteName: "first-run-test-\(UUID().uuidString)")! }

/// Wait for a model's Task to land.
@MainActor
private func settle(_ m: FirstRunModel) async {
    for _ in 0..<200 {
        await Task.yield()
        if !m.busy { return }
        try? await Task.sleep(nanoseconds: 5_000_000)
    }
}

@MainActor @Suite struct ConnectorTests {
    @Test func addressesBecomeURLs() {
        #expect(LassoConnector.address("lasso.example.com")?.url.absoluteString == "https://lasso.example.com")
        #expect(LassoConnector.address("lasso.example.com")?.guessed == true)
        #expect(LassoConnector.address(" 127.0.0.1:8090/ ")?.url.absoluteString == "http://127.0.0.1:8090")
        #expect(LassoConnector.address("localhost:8090")?.url.absoluteString == "http://localhost:8090")
        #expect(LassoConnector.address("100.86.22.100:8090")?.url.absoluteString == "http://100.86.22.100:8090")
        #expect(LassoConnector.address("http://titan:8090")?.guessed == false)
        #expect(LassoConnector.address("") == nil)
        #expect(LassoConnector.address("not an address") == nil)
        #expect(LassoConnector.address("ftp://x") == nil)
    }

    @Test func anOpenLassoIsUsed() async {
        let log = Log()
        let c = connector(log) { _, _ in .open }
        let r = await c.connect("127.0.0.1:8090")
        #expect(r == .connected(URL(string: "http://127.0.0.1:8090")!, .open))
        #expect(log.used == [URL(string: "http://127.0.0.1:8090")!])
        #expect(log.probes.first?.1 == nil)
    }

    @Test func aBareNameFallsBackToPlainHTTP() async {
        let log = Log()
        let c = connector(log) { url, _ in url.scheme == "http" ? .open : .unreachable("Could not connect to the server.") }
        let r = await c.connect("titan.tail.ts.net:8090")
        #expect(r == .connected(URL(string: "http://titan.tail.ts.net:8090")!, .open))
        #expect(log.probes.map(\.0.scheme) == ["https", "http"])
    }

    @Test func unreachableSaysSoWithTheHost() async {
        let log = Log()
        let c = connector(log) { _, _ in .unreachable("Could not connect to the server.") }
        let r = await c.connect("lasso.example.com")
        guard case .failed(let why) = r else { Issue.record("\(r)"); return }
        #expect(why.contains("lasso.example.com"))
        #expect(log.used.isEmpty)
        #expect(await c.connect("two words") == .failed("That is not an address. Try a hostname, like lasso.example.com."))
    }

    @Test func aPasswordIsCheckedThenKeptAndUsed() async {
        let log = Log()
        let c = connector(log) { _, basic in basic == "me:pw" ? .open : .password }
        let url = URL(string: "https://old.example.com")!
        #expect(await c.connect("old.example.com") == .needsPassword(url))
        #expect(await c.password(url, user: "me", password: "nope") == .failed("That user and password did not work."))
        #expect(log.kept.isEmpty && log.passwords.isEmpty)
        #expect(await c.password(url, user: " me ", password: "pw") == .connected(url, .password))
        #expect(log.kept == ["me:pw"])
        #expect(log.passwords.map(\.1) == ["me:pw"])
        #expect(log.probes.last?.1 == "me:pw")
    }

    @Test func aSignInKeepsTheTokensAndCanBeCancelled() async {
        let meta = URL(string: "https://lasso.example.com/.well-known/oauth-protected-resource")!
        let log = Log()
        let c = connector(log) { _, _ in .signIn(resourceMetadata: meta) }
        var told: URL?
        let r = await c.connect("lasso.example.com") { told = $0 }
        #expect(r == .connected(URL(string: "https://lasso.example.com")!, .signedIn))
        #expect(told == URL(string: "https://lasso.example.com")!)
        #expect(log.grants.count == 1 && log.signedIn.count == 1)

        let log2 = Log()
        let c2 = connector(log2, signIn: { _, _ in throw OAuthError.cancelled }) { _, _ in .signIn(resourceMetadata: meta) }
        #expect(await c2.connect("lasso.example.com") == .cancelled)
        #expect(log2.grants.isEmpty)
    }
}

@MainActor @Suite struct FirstRunTests {
    private func model(_ log: Log, answers: Bool, answer: @escaping (URL, String?) -> LassoAccess) -> FirstRunModel {
        let m = FirstRunModel(connector: connector(log, answer: answer), defaults: freshDefaults())
        m.configured = { URL(string: "https://lasso.example.com")! }
        m.answers = { answers }
        m.describe = { FirstRunModel.Connected(name: "titan", version: "lasso 6.4.0", address: LassoConnector.shown($0)) }
        return m
    }

    @Test func anAnsweringLassoSkipsToConnected() async {
        let log = Log()
        let m = model(log, answers: true) { _, _ in .unreachable("x") }
        await m.begin()
        #expect(m.step == .connected)
        #expect(m.connected?.name == "titan")
        #expect(m.connected?.address == "lasso.example.com")
        #expect(m.everConnected)
        #expect(log.probes.isEmpty)
    }

    @Test func lassoOnThisMacIsTriedNext() async {
        let log = Log()
        let m = model(log, answers: false) { url, _ in url == FirstRunModel.localLasso ? .open : .unreachable("x") }
        await m.begin()
        #expect(m.step == .connected)
        #expect(log.used == [FirstRunModel.localLasso])
        #expect(m.connected?.address == "127.0.0.1:8090")
    }

    /// A lasso older than 7.0.0 on this Mac is not taken: the field shows.
    @Test func anOldLassoOnThisMacIsSkipped() async {
        let log = Log()
        let m = model(log, answers: false) { url, _ in url == FirstRunModel.localLasso ? .tooOld("lasso 2.13.0") : .unreachable("x") }
        await m.begin()
        #expect(m.step == .connect)
        #expect(log.used.isEmpty)
    }

    /// Connecting to one by hand says why, and what to do.
    @Test func anOldLassoIsRefusedWithAReason() async {
        let log = Log()
        let c = connector(log) { _, _ in .tooOld("lasso 2.13.0") }
        let r = await c.connect("127.0.0.1:8090")
        guard case .failed(let why) = r else { Issue.record("connected to a lasso too old"); return }
        #expect(why.contains("lasso 2.13.0") && why.contains("7.0.0 or later") && why.contains("lasso update"))
        #expect(log.used.isEmpty)
    }

    @Test func releasesAreComparedByNumber() {
        func v(_ s: String) -> VersionInfo { VersionInfo(lasso_protocol: 22, lasso_version: s, compatible: true) }
        #expect(LassoCompat.supports(v("7.0.0 (6ab86064a6df)")))
        #expect(LassoCompat.supports(v("7.0.1")) && LassoCompat.supports(v("10.0.0")) && LassoCompat.supports(v("7.1.0 (dev)")))
        #expect(!LassoCompat.supports(v("6.4.0 (dev)")) && !LassoCompat.supports(v("2.13.0")))
        #expect(LassoCompat.supports(v("dev")))
        #expect(LassoChoice(url: "http://127.0.0.1:8090", version: v("2.13.0 (dev)")).detail
            == "lasso 2.13.0, too old: needs 7.0.0 or later · 127.0.0.1:8090")
    }

    @Test func nothingAnsweringShowsTheField() async {
        let log = Log()
        let m = model(log, answers: false) { _, _ in .unreachable("Could not connect to the server.") }
        await m.begin()
        #expect(m.step == .connect)
        #expect(m.focus == .address)
        #expect(!m.everConnected)

        m.address = "nowhere.example.com"
        m.connect()
        await settle(m)
        #expect(m.step == .connect)
        #expect(m.note?.contains("nowhere.example.com") == true)
        #expect(m.address == "nowhere.example.com")
    }

    @Test func openConnects() async {
        let log = Log()
        let m = model(log, answers: false) { url, _ in url.host == "titan.tail.ts.net" ? .open : .unreachable("x") }
        await m.begin()
        m.use(FirstRunModel.examples[0])
        #expect(m.address == "titan.your-tailnet.ts.net")
        m.address = "titan.tail.ts.net"
        m.primary()
        await settle(m)
        #expect(m.step == .connected)
        #expect(log.used.map(\.host) == ["titan.tail.ts.net"])
    }

    @Test func aPasswordStepStoresAndRetries() async {
        let log = Log()
        let m = model(log, answers: false) { url, basic in
            url.host == "old.example.com" ? (basic == "me:pw" ? .open : .password) : .unreachable("x")
        }
        await m.begin()
        m.address = "old.example.com"
        m.connect()
        await settle(m)
        #expect(m.step == .password)
        #expect(m.focus == .user)
        m.user = "me"
        m.primary()  // ⏎ in the user field moves on
        #expect(m.focus == .password)
        m.password = "pw"
        m.primary()
        await settle(m)
        #expect(m.step == .connected)
        #expect(log.kept == ["me:pw"])
        #expect(log.passwords.map(\.0.host) == ["old.example.com"])
    }

    @Test func doneSetsOnboardedAndEscBeforeConnectingDoesNot() async {
        let log = Log()
        let m = model(log, answers: false) { _, _ in .unreachable("x") }
        var closed = 0
        m.onClose = { closed += 1 }
        await m.begin()
        m.dismiss()
        #expect(closed == 1)
        #expect(!m.finished)

        let m2 = model(log, answers: true) { _, _ in .open }
        m2.onClose = { closed += 1 }
        await m2.begin()
        m2.dismiss()  // esc once connected is Done
        #expect(m2.finished)
        #expect(closed == 2)
    }

    @Test func tipsShowTheShortcutsAsSet() {
        let m = model(Log(), answers: false) { _, _ in .open }
        m.shortcut = { slot in slot == .inbox ? .doubleTap(.command) : slot == .newScratch ? .off : slot.fallback }
        let tips = m.tips
        #expect(tips.map(\.id) == ["inbox", "new", "settings"])
        #expect(tips[0].keys == ["⌘⌘"])
        #expect(tips[1].keys == ["⌥⇧O"])
        #expect(!tips[1].detail.contains("scratch"))
        m.shortcut = { slot in slot == .inbox ? .off : slot.fallback }
        #expect(m.tips[0].keys.isEmpty)
        #expect(m.tips[1].detail.contains("⌃⇧O"))
    }

    @Test func aBasicCredentialSetLaterReachesEveryRequest() {
        let c = LassoClient(baseURL: URL(string: "https://old.example.com")!)
        #expect(c.request("/api/version").value(forHTTPHeaderField: "Authorization") == nil)
        c.setBasicAuth("me:pw")
        #expect(c.request("/api/version").value(forHTTPHeaderField: "Authorization") == "Basic " + Data("me:pw".utf8).base64EncodedString())
        #expect(c.probe(URL(string: "https://x.example.com")!).basicAuth == "me:pw")
        c.setBasicAuth(nil)
        #expect(c.request("/api/version").value(forHTTPHeaderField: "Authorization") == nil)
    }
}
