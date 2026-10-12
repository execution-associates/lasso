import AppKit
import Foundation
import Testing
import WebKit

@testable import LassoDesktopKit

// The Browser window (PLAN 24): what its page may reach, the picker over the
// connected lasso's browsers, an agent's page waiting as a pulse and a
// banner, and the window end to end against a stand-in page and CDP socket.

private func browsers() -> BrowserStatusPayload {
    BrowserStatusPayload(profiles: [
        BrowserInfo(
            id: "default", name: "Default", isDefault: true, running: true,
            pages: [BrowserPageInfo(id: "A1", url: "https://example.com/", title: "Example")]),
        BrowserInfo(
            id: "minime", name: "minime Chrome", running: true,
            pages: [
                BrowserPageInfo(id: "B1", url: "https://github.com/", title: "GitHub"),
                BrowserPageInfo(id: "B2", url: "https://apple.com/", title: "Apple"),
            ]),
        BrowserInfo(id: "work", name: "Work"),
    ])
}

private func scratchDefaults(_ name: String) -> UserDefaults {
    let d = UserDefaults(suiteName: name)!
    d.removePersistentDomain(forName: name)
    return d
}

private let offer = BrowserOpen(
    profile: "minime", tab_id: "B7F3A0C2D1E4", url: "https://github.com/execution-associates/lasso?tab=readme", from: "olivia")

/// The page reaches its document, its build's files and the browser status,
/// and sockets to the browsers' CDP; nothing else on lasso.
@Test func theBrowserPageReachesOnlyTheBrowsers() {
    func path(_ s: String, _ method: String = "GET") -> String? { BrowserPage.lassoPath(URL(string: s)!, method: method) }
    #expect(path("lasso-browser://lasso/desktop-browser.html?profile=minime") == "/desktop-browser.html?profile=minime")
    #expect(path("lasso-browser://lasso/assets/desktop-browser-B7MImw1p.js") == "/assets/desktop-browser-B7MImw1p.js")
    #expect(path("lasso-browser://lasso/api/browser") == "/api/browser")
    #expect(path("lasso-browser://lasso/api/browser", "POST") == "/api/browser")
    for refused in [
        ("lasso-browser://lasso/api/file?path=/etc/passwd", "GET"),
        ("lasso-browser://lasso/api/browser/profiles", "GET"),
        ("lasso-browser://lasso/api/browser/profiles/work", "DELETE"),
        ("lasso-browser://lasso/desktop-browser.html", "POST"),
        ("lasso-browser://lasso/assets/../api/file", "GET"),
        ("lasso-browser://lasso/assets/%2e%2e/api", "GET"),
        ("lasso-browser://lasso/assets/a/b.js", "GET"),
        ("lasso-browser://lasso/assets/.env", "GET"),
        ("lasso-browser://lasso/", "GET"),
        ("lasso-browser://lasso/terminal/local/", "GET"),
        ("lasso-browser://elsewhere/desktop-browser.html", "GET"),
        ("lasso-terminal://lasso/desktop-browser.html", "GET"),
    ] {
        #expect(path(refused.0, refused.1) == nil, "\(refused)")
    }

    let https = URL(string: "https://lasso.example.com")!
    #expect(BrowserPage.socketURL(base: https, path: "/cdp")?.absoluteString == "wss://lasso.example.com/cdp")
    #expect(
        BrowserPage.socketURL(base: URL(string: "http://127.0.0.1:18090")!, path: "/cdp/p/minime")?.absoluteString
            == "ws://127.0.0.1:18090/cdp/p/minime")
    for refused in [
        "/cdp/p/", "/cdp/p/Minime", "/cdp/p/a/b", "/cdp/p/-x", "/cdp/devtools/browser/1", "/cdp/json/list", "/cdp/browsers",
        "/terminal/local/ws", "/api/events", "/cdp/p/\(String(repeating: "a", count: 33))",
    ] {
        #expect(BrowserPage.socketURL(base: https, path: refused) == nil, "\(refused)")
    }
}

/// The picker lists the connected lasso's browsers, remembers the pick, and
/// gives way to the default when the remembered one is gone.
@MainActor @Test func thePickerListsLassosBrowsers() async {
    let d = scratchDefaults("lasso-test-browser-picker")
    d.set("gone", forKey: BrowserWindowModel.storedKey)
    let m = BrowserWindowModel(source: FixtureBrowserSource(browsers()), defaults: d)
    var picked: [String] = []
    m.onSelect = { picked.append($0) }
    #expect(m.selected == "gone")
    await m.refresh()
    #expect(m.browsers.map(\.id) == ["default", "minime", "work"])
    #expect(m.selected == "default" && d.string(forKey: BrowserWindowModel.storedKey) == "default")
    #expect(m.unavailable == nil)
    #expect(m.browsers.map(m.line) == ["Default · 1 page", "minime Chrome · 2 pages", "Work"])

    m.select("minime")
    #expect(m.selected == "minime" && m.currentName == "minime Chrome" && picked == ["default", "minime"])
    #expect(BrowserWindowModel(source: FixtureBrowserSource(browsers()), defaults: d).selected == "minime")

    // An agent's page in another browser: shown there, the page switching itself.
    m.take(BrowserOpen(profile: "work", tab_id: "W1", url: "https://example.org/", from: "stub"))
    #expect(m.selected == "work" && picked == ["default", "minime"])
    m.take(BrowserOpen(profile: nil, tab_id: nil, url: nil, from: nil))
    #expect(m.selected == "default")

    // A lasso with no Chromium says why; one that predates browsers has its one.
    let none = BrowserWindowModel(
        source: FixtureBrowserSource(BrowserStatusPayload(available: false, reason: "no Chromium was found", profiles: [])),
        defaults: scratchDefaults("lasso-test-browser-none"))
    await none.refresh()
    #expect(none.unavailable == "no Chromium was found" && none.browsers.map(\.id) == ["default"])
    let gone = BrowserWindowModel(source: FixtureBrowserSource(nil), defaults: scratchDefaults("lasso-test-browser-gone"))
    await gone.refresh()
    #expect(gone.browsers.isEmpty && gone.selected == "default")
}

/// The browser-open event, as lasso sends it, and how the banner words it.
@Test func anAgentsPageReadsAsTheBannerSaysIt() throws {
    let dir = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        .appendingPathComponent("Spec/contract")
    let o = try JSONDecoder().decode(BrowserOpen.self, from: Data(contentsOf: dir.appendingPathComponent("browser_open.json")))
    #expect(o.profile == "x" && o.tab_id == "x" && o.url == "x" && o.from == "x" && o.mode == "x")
    let st = try JSONDecoder().decode(BrowserStatusPayload.self, from: Data(contentsOf: dir.appendingPathComponent("browser_status.json")))
    let b = try #require(st.browsers.first)
    #expect(st.available == true && b.id == "x" && b.isDefault && b.running && b.pages?.first?.title == "x" && b.ws_path == "x")

    #expect(offer.who == "olivia" && offer.browser == "minime")
    #expect(offer.shownURL == "github.com/execution-associates/lasso")
    let bare = BrowserOpen(profile: "", tab_id: nil, url: "about:blank", from: "")
    #expect(bare.who == "An agent" && bare.browser == "default" && bare.shownURL == "a new page")
    #expect(BrowserOpen(profile: nil, tab_id: nil, url: "https://example.com/", from: nil).shownURL == "example.com")
}

/// An offer pulses until it is opened, dismissed, or seen: a banner that
/// went on its own leaves the pulse until the rail has shown it.
@MainActor @Test func anOfferPulsesUntilAnswered() {
    let a = BrowserAttention()
    #expect(!a.pulsing)
    a.arrived(offer)
    #expect(a.pulsing && a.bannerShown)
    // The rail opening while the banner is up does not count as seen.
    a.rail(expanded: true)
    a.rail(expanded: false)
    #expect(a.pulsing)
    a.bannerLingered()
    #expect(a.pulsing && !a.bannerShown)
    a.rail(expanded: false)
    #expect(a.pulsing)
    a.rail(expanded: true)
    #expect(a.pulsing)
    a.rail(expanded: false)
    #expect(!a.pulsing)

    a.arrived(offer)
    let newer = BrowserOpen(profile: "default", tab_id: "C1", url: "https://example.com/", from: "clem")
    a.arrived(newer)
    #expect(a.request == newer)
    a.clear()
    #expect(!a.pulsing && !a.bannerShown)
}

/// An agent's page: the rail's Browser button pulses and the banner comes up
/// beside the rail, never taking the keyboard; nothing opens. Dismiss puts
/// both away; Open (or the button) takes the offer.
@MainActor @Test func anAgentsPageBringsTheBannerNotTheWindow() {
    let app = AppCoordinator(fleet: FleetModel(), sync: nil, fixture: nil)
    let before = NSApp.windows.filter(\.isVisible).count
    app.browserOpened(offer)
    #expect(app.rail.model.browserPulse && app.browserAttention.bannerShown)
    let banner = app.banner.panel
    #expect(banner.isVisible && !banner.canBecomeKey && !banner.isKeyWindow)
    // The banner is the only window that came up.
    #expect(NSApp.windows.filter(\.isVisible).count == before + 1)

    app.dismissBrowserOffer()
    #expect(!app.rail.model.browserPulse && !app.banner.panel.isVisible)

    app.browserOpened(offer)
    #expect(app.rail.model.browserPulse)
    app.rail.model.setExpanded(true, animated: false)
    app.rail.model.activate(.browser)
    #expect(!app.rail.model.browserPulse && !app.browserAttention.bannerShown && !app.banner.panel.isVisible)
    #expect(app.rail.model.items.suffix(4) == [.terminal, .browser, .newAgent, .settings])
}

// MARK: the window end to end

private let browserPage = #"""
<html><body><script>
window.__log = [];
window.__lassoBrowser = {
  select: (id) => __log.push("select:" + id),
  show: (r) => __log.push("show:" + r.profile + ":" + r.tab_id + ":" + r.from),
};
const ws = new WebSocket("ws://lasso" + "/cdp/p/" + new URLSearchParams(location.search).get("profile"));
ws.onopen = () => { ws.send(JSON.stringify({ id: 1, method: "Target.setDiscoverTargets" })); window.__open = true; };
fetch("/api/file?path=/etc/passwd").then(() => __log.push("file:reached"), () => __log.push("file:refused"));
fetch("/api/browser").then((r) => r.json()).then((j) => __log.push("status:" + j.profiles.length), () => __log.push("status:failed"));
webkit.messageHandlers.lassoPage.postMessage({ op: "ready" });
</script></body></html>
"""#

/// lasso's HTTP for the Browser window: the stand-in page and the status.
final class BrowserStub: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var seen: [String] = []
    override class func canInit(with request: URLRequest) -> Bool { request.url?.scheme == "http" }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let url = request.url!
        Self.seen.append(url.path)
        let (status, type, body): (Int, String, String) =
            switch url.path {
            case "/desktop-browser.html": (200, "text/html", browserPage)
            case "/api/browser": (200, "application/json", #"{"available":true,"profiles":[{"id":"default","name":"Default","default":true,"running":true,"pages":[],"ws_path":"/cdp"},{"id":"minime","name":"minime Chrome","default":false,"running":true,"pages":[],"ws_path":"/cdp/p/minime"}]}"#)
            default: (404, "text/plain", "not here")
            }
        let resp = HTTPURLResponse(url: url, statusCode: status, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": type])!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

/// The window loads lasso's page on the remembered browser through the app's
/// own scheme, its socket reaches that browser's CDP natively, a route the
/// page has no business with is refused, and an agent's page that arrived
/// before the page was up is handed over once it is.
@MainActor @Test func theBrowserWindowRunsLassosPage() async throws {
    let cdp = try StandInSocketServer()
    defer { cdp.stop() }
    await until(2) { cdp.port != 0 }
    UserDefaults.standard.set("minime", forKey: BrowserWindowModel.storedKey)
    defer { UserDefaults.standard.removeObject(forKey: BrowserWindowModel.storedKey) }
    BrowserStub.seen = []
    let client = LassoClient(baseURL: URL(string: "http://127.0.0.1:\(cdp.port)")!, protocolClasses: [BrowserStub.self])
    let c = BrowserWindowController(source: FixtureBrowserSource(browsers()), client: client)
    c.show(offer)
    defer { c.close() }
    let web = try #require(c.pageView)
    func log() async -> String { (try? await web.evaluateJavaScript("__log.join(' ')")) as? String ?? "" }
    await until(10) {
        guard cdp.frames.count >= 1 else { return false }
        let l = await log()
        return l.contains("status:") && l.contains("file:") && l.contains("show:")
    }
    #expect(c.model.phase == .live && c.model.selected == "minime")
    #expect(cdp.frames == [#"{"id":1,"method":"Target.setDiscoverTargets"}"#])
    let l = await log()
    #expect(l.contains("select:minime") && l.contains("show:minime:B7F3A0C2D1E4:olivia"))
    #expect(l.contains("file:refused") && l.contains("status:2"))
    #expect(!BrowserStub.seen.contains("/api/file"))

    // The picker: the page switches browser.
    c.model.select("default")
    await until(3) { (await log()).contains("select:default") }
    #expect((await log()).contains("select:default"))
}
