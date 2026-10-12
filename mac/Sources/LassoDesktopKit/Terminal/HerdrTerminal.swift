import AppKit
import Combine
import SwiftUI
import WebKit

// lasso's herdr in a window of its own (PLAN 22): the rail's terminal button
// opens it, and an agent's header button (or T) focuses that agent's pane in
// it. It is lasso's web terminal (TerminalBridge), one window for the app,
// showing one host's herdr at a time, in an agent window's panel (PageWindow):
// over every app and on every Space, and never full screen.

/// What the herdr window asks of lasso.
@MainActor
public protocol HerdrTerminalSource: AnyObject {
    /// Make `host`'s terminal resident (POST /api/host, as a web tab moving
    /// onto a host does) and say its URL path segment (/api/active's
    /// host_slug: lasso disambiguates aliases, so it is never derived here).
    func attach(_ host: String) async throws -> String
    /// The query lasso serves the terminal's document with (its palette),
    /// read off lasso's redirect so the page's own URL carries it: ttyd reads
    /// its options from there.
    func documentQuery(_ slug: String) async throws -> String
    /// Focus the pane in its host's herdr: herdr's focus is one for every
    /// client of that herdr. True when this window's herdr client must start
    /// again to show it (it shows a saved machine; lasso's `reveal`).
    func focus(_ pane: HostPane) async throws -> Bool
    /// A human acted in the window: take the right to resize `host`'s shared
    /// terminal (/api/term-claim). Who holds it afterwards.
    func claim(_ host: String, client: String) async throws -> String
}

/// The source on a live lasso.
@MainActor
public final class LiveHerdrTerminalSource: HerdrTerminalSource {
    let client: LassoClient

    public init(client: LassoClient) { self.client = client }

    struct Attach: Encodable { var host: String }
    struct Attached: Decodable { var active: String? }
    struct Active: Decodable { var host_slug: String? }

    public func attach(_ host: String) async throws -> String {
        let _: Attached = try await client.post("/api/host", json: Attach(host: host))
        let a: Active = try await client.get("/api/active", query: ["host": host])
        guard let slug = a.host_slug, !slug.isEmpty else { throw LassoError.http(502, "lasso named no terminal for \(host)") }
        return slug
    }

    public func documentQuery(_ slug: String) async throws -> String {
        var r = client.request(raw: HerdrTerminal.documentPath(slug))
        r.setValue("text/html", forHTTPHeaderField: "Accept")
        let (_, http) = try await client.response(r, redirects: false)
        let code = http?.statusCode ?? 0
        if (300..<400).contains(code), let to = http?.value(forHTTPHeaderField: "Location") {
            return URLComponents(string: to)?.percentEncodedQuery ?? ""
        }
        guard (200..<300).contains(code) else { throw LassoError.http(code, "no terminal at \(HerdrTerminal.documentPath(slug))") }
        return ""
    }

    struct Focus: Encodable {
        var pane_id: String
        var reveal: Bool
    }
    struct Focused: Decodable {
        var ok: Bool?
        var reattach: Bool?
    }

    public func focus(_ pane: HostPane) async throws -> Bool {
        let f: Focused = try await client.post(
            "/api/focus", query: ["host": pane.host], json: Focus(pane_id: pane.pane_id, reveal: true))
        return f.reattach == true
    }

    struct Claim: Encodable { var client_id: String }
    struct Claimed: Decodable { var term_owner: String? }

    public func claim(_ host: String, client id: String) async throws -> String {
        let c: Claimed = try await client.post("/api/term-claim", query: ["host": host], json: Claim(client_id: id))
        return c.term_owner ?? ""
    }
}

/// The window's state.
@MainActor
public final class HerdrTerminalModel: ObservableObject {
    public enum Phase: Equatable, Sendable {
        case closed
        case connecting
        case live
        case failed(String)
    }

    /// The host whose herdr the window shows ("local" or an ssh alias).
    @Published public internal(set) var host = "local"
    /// That host's name as the rest of the app shows it.
    @Published public internal(set) var hostLabel = ""
    @Published public internal(set) var phase: Phase = .closed
    /// One line under the title: what the last focus did.
    @Published public internal(set) var notice: String?
    var onRetry: () -> Void = {}

    public var title: String { "herdr on \(hostLabel.isEmpty ? host : hostLabel)" }
}

/// The window's header: what it shows, and one line on what the last focus did.
struct HerdrTerminalTitle: View {
    @ObservedObject var model: HerdrTerminalModel

    var body: some View {
        HStack(spacing: Theme.space("m")) {
            LucideIcon(.squareTerminal, size: Theme.Rail.icon.number("size") - 3)
                .foregroundStyle(Theme.Text.secondary.color)
            VStack(alignment: .leading, spacing: 0) {
                Text(model.title)
                    .font(Theme.Fonts.label.font)
                    .foregroundStyle(Theme.Text.primary.color)
                    .lineLimit(1)
                    .truncationMode(.middle)
                if let n = model.notice, !n.isEmpty {
                    Text(n)
                        .font(Theme.Fonts.small.font)
                        .foregroundStyle(Theme.Text.secondary.color)
                        .lineLimit(1)
                        .truncationMode(.tail)
                }
            }
        }
        .accessibilityElement(children: .combine)
        .parityAnchor("terminal.title")
    }
}

/// Over the page while it is not up: opening, or why it could not.
struct HerdrTerminalStatus: View {
    @ObservedObject var model: HerdrTerminalModel

    var body: some View {
        ZStack {
            Theme.Panel.surface.color
            VStack(spacing: Theme.space("l")) {
                if case .failed(let why) = model.phase {
                    Text("Could not open \(model.title)")
                        .font(Theme.Fonts.title.font)
                        .foregroundStyle(Theme.Text.primary.color)
                    Text(why)
                        .font(Theme.Fonts.small.font)
                        .foregroundStyle(Theme.Text.secondary.color)
                        .multilineTextAlignment(.center)
                        .textSelection(.enabled)
                    PillButton(title: "Try again", label: "Try opening \(model.title) again") { model.onRetry() }
                } else {
                    WaitingRow(text: "Opening \(model.title)")
                }
            }
            .padding(Theme.Panel.inset.pt)
        }
    }
}

/// The terminal's web view: a lasso page's (no WebKit menu), and ⌘= ⌘-
/// ⌘0 size its text. The window takes these before the page sees them, so
/// neither xterm nor the shell gets them; they work with no menu bar, and the
/// View menu's items reach them through the responder chain while this
/// window is key.
final class TerminalWebView: LassoPageWebView {
    var onZoom: (TerminalZoom.Step) -> Void = { _ in }

    override func performKeyEquivalent(with e: NSEvent) -> Bool {
        if let step = TerminalZoom.step(e) {
            onZoom(step)
            return true
        }
        return super.performKeyEquivalent(with: e)
    }

    @objc func terminalZoomIn(_ sender: Any?) { onZoom(.bigger) }
    @objc func terminalZoomOut(_ sender: Any?) { onZoom(.smaller) }
    @objc func terminalActualSize(_ sender: Any?) { onZoom(.actual) }
}

/// The part of an `active` frame the window reads.
private struct OwnerFrame: Decodable { var term_owner: String? }

/// The one herdr window: open, show an agent, close.
@MainActor
public final class HerdrTerminalController: NSObject {
    public let model = HerdrTerminalModel()
    private let source: HerdrTerminalSource
    private let client: LassoClient
    private let fleet: FleetModel
    /// This window's identity to lasso's term claim, carried by its own
    /// event stream (a claim stands only for a client with a stream).
    private let clientID = "lasso-desktop-term-" + UUID().uuidString.prefix(8).lowercased()
    private var claim: TermClaim
    private var claiming = false
    /// The text size (TerminalZoom), one for the app.
    private var fontSize = TerminalZoom.stored()
    /// The page loaded last: a reload starts it again at today's size.
    private var page: (slug: String, query: String)?
    private var window: PageWindow?
    private var web: TerminalWebView?
    private var bridge: TerminalBridge?
    private var status: NSHostingView<HerdrTerminalStatus>?
    private var stream: EventStream?
    private var loading: Task<Void, Never>?
    private var subs: Set<AnyCancellable> = []

    static let frameName = "LassoHerdrTerminal"

    public init(source: HerdrTerminalSource, client: LassoClient, fleet: FleetModel) {
        self.source = source
        self.client = client
        self.fleet = fleet
        claim = TermClaim(me: clientID)
        super.init()
        model.onRetry = { [weak self] in
            guard let self else { return }
            self.load(self.model.host)
        }
    }

    public var isOpen: Bool { window?.isOpen == true }

    /// The window's panel, made if it is not yet, and its page (tests).
    var hostWindow: NSWindow { (window ?? makeWindow()).panel }
    var pageView: TerminalWebView? { web }
    var pageWindow: PageWindow? { window }

    /// The rail's button: the window as it is, else lasso's own herdr.
    public func open() {
        present()
        switch model.phase {
        case .closed: load("local")
        case .failed: load(model.host)
        case .connecting, .live: break
        }
        Trace.event("herdr window opened (\(model.host))")
    }

    /// An agent's header button, or T: focus its pane in its host's herdr
    /// (every client's focus moves: herdr has one), then show that herdr.
    public func show(_ pane: HostPane, note: String? = nil) {
        present()
        let name = pane.displayName
        model.notice = "Focusing \(name)"
        Trace.event("herdr window focusing a pane on \(pane.host)")
        Task {
            var reattach = false
            do {
                reattach = try await source.focus(pane)
                model.notice = note ?? "Showing \(name)"
            } catch {
                model.notice = "Could not focus \(name): \(error)"
            }
            if pane.host != model.host || model.phase == .closed || isFailed {
                load(pane.host)
            } else if reattach {
                reload()
            }
        }
    }

    /// Close the window: its herdr client goes with its socket.
    public func close() {
        window?.close()
    }

    private var isFailed: Bool {
        if case .failed = model.phase { return true }
        return false
    }

    // MARK: the window

    /// On the Space the user is on, over whatever is in front, with the
    /// keyboard in the terminal: without activating the app, as an agent's
    /// window opens. A collapsed window opens out again.
    private func present() {
        let w = window ?? makeWindow()
        if w.collapsed { w.toggle() }
        w.show(takeKey: true)
    }

    private func makeWindow() -> PageWindow {
        let t = Theme.Terminal.window

        let bridge = TerminalBridge(client: client)
        bridge.onAct = { [weak self] in self?.humanActed() }
        bridge.onLink = { NSWorkspace.shared.open($0) }
        bridge.onCopy = { text in
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setString(text, forType: .string)
        }
        bridge.onDocumentFailed = { [weak self] why in self?.model.phase = .failed(why) }
        bridge.setFontSize(fontSize)
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .nonPersistent()
        config.setURLSchemeHandler(bridge, forURLScheme: HerdrTerminal.scheme)
        config.userContentController.add(WeakMessageHandler(bridge), name: "lassoTerm")
        config.userContentController.addUserScript(
            WKUserScript(source: TerminalBridge.script, injectionTime: .atDocumentStart, forMainFrameOnly: true))
        let web = TerminalWebView(frame: NSRect(origin: .zero, size: t.size2("size")), configuration: config)
        web.navigationDelegate = self
        web.uiDelegate = self
        web.underPageBackgroundColor = Theme.Panel.surface.nsColor
        web.autoresizingMask = [.width, .height]
        web.setAccessibilityLabel("herdr terminal")
        web.onZoom = { [weak self] in self?.zoom($0) }
        bridge.webView = web

        let status = NSHostingView(rootView: HerdrTerminalStatus(model: model))
        status.frame = web.frame
        status.autoresizingMask = [.width, .height]
        let container = NSView(frame: web.frame)
        container.addSubview(web)
        container.addSubview(status)
        let header = t.number("header")
        let w = PageWindow(name: Self.frameName, size: t.size2("size"), min: t.size2("min"), headerHeight: header, page: container) {
            [model] chrome in
            PageWindowHeader(chrome: chrome, anchor: "terminal", height: header) { HerdrTerminalTitle(model: model) }
                .parityAnchor("terminal.header")
        }
        w.focus = web
        w.onClosed = { [weak self] in self?.teardown() }
        // The window taking the keyboard is the human acting.
        w.onBecameKey = { [weak self] in self?.humanActed() }

        model.$phase.combineLatest(model.$hostLabel)
            .receive(on: RunLoop.main)
            .sink { [weak self] phase, _ in
                guard let self else { return }
                // What VoiceOver and the window list call it.
                self.window?.panel.title = self.model.title
                self.status?.isHidden = phase == .live
            }
            .store(in: &subs)

        window = w
        self.web = web
        self.bridge = bridge
        self.status = status
        return w
    }

    // MARK: loading a host's herdr

    /// Show `host`'s herdr: make its terminal resident, then load its page.
    func load(_ host: String) {
        loading?.cancel()
        model.host = host
        model.hostLabel = label(host)
        model.phase = .connecting
        watch(host)
        loading = Task { [weak self] in
            guard let self else { return }
            do {
                let slug = try await source.attach(host)
                let query = try await source.documentQuery(slug)
                guard !Task.isCancelled, model.host == host, let bridge, let web else { return }
                bridge.closeAll()
                bridge.slug = slug
                page = (slug, query)
                web.load(URLRequest(url: pageURL(slug, query)))
            } catch is CancellationError {
            } catch {
                guard model.host == host else { return }
                model.phase = .failed(String(describing: error))
            }
        }
    }

    /// Start this herdr client again (lasso's reveal asked for it).
    private func reload() {
        bridge?.closeAll()
        model.phase = .connecting
        if let page, page.slug == bridge?.slug {
            web?.load(URLRequest(url: pageURL(page.slug, page.query)))
        } else {
            web?.reload()
        }
    }

    /// The page at the window's text size: ttyd applies it from the query
    /// as it connects, so a load refits once, not twice.
    private func pageURL(_ slug: String, _ query: String) -> URL {
        HerdrTerminal.pageURL(slug: slug, query: TerminalZoom.query(query, fontSize: fontSize))
    }

    /// ⌘= ⌘- ⌘0: a new size refits the terminal, and herdr reflows every
    /// client of its session on the new columns and rows. That is a human
    /// resizing, so it claims the size first, as a key does: a resize while
    /// another client holds it waits for the claim.
    func zoom(_ step: TerminalZoom.Step) {
        let scale = TerminalZoom.Scale()
        let next = scale.apply(step, to: fontSize)
        guard next != fontSize else { return }
        fontSize = next
        TerminalZoom.store(next, scale: scale)
        Analytics.shared.track(.featureUsed(.terminal_zoom))
        humanActed()
        bridge?.setFontSize(next)
        Trace.event("herdr window text \(next)")
    }

    /// The host as the app names it elsewhere: its label (lasso's own
    /// machine by its hostname, never "local").
    private func label(_ host: String) -> String {
        fleet.panes.first { $0.host == host }?.host_label ?? host
    }

    /// The window's own event stream on `host`: it keeps that host's
    /// terminal resident (lasso retires one nobody watches), makes this
    /// window a client a claim can stand for, and says who holds the size.
    private func watch(_ host: String) {
        stream?.stop()
        claim.owner = ""
        bridge?.setResizeAllowed(true)
        let s = EventStream(
            client: client, clientID: clientID, host: host,
            onEvent: { [weak self] e in
                guard e.event == "active", let owner = Self.owner(e.data) else { return }
                Task { @MainActor in self?.setOwner(owner, host: host) }
            },
            onStatus: { _ in })
        s.start()
        stream = s
    }

    /// The size's holder in an `active` frame, when the frame says.
    nonisolated static func owner(_ data: String) -> String? {
        (try? JSONDecoder().decode(OwnerFrame.self, from: Data(data.utf8)))?.term_owner
    }

    private func setOwner(_ owner: String, host: String) {
        guard host == model.host else { return }
        claim.owner = owner
        bridge?.setResizeAllowed(claim.may)
    }

    /// A key, a pointer or the window taking focus: this is where the human
    /// works, so the terminal's size should follow this window.
    private func humanActed() {
        guard model.phase != .closed, !claiming, claim.due(now: Date(), renew: Theme.Live.termClaim.number("renew")) else { return }
        claiming = true
        let host = model.host
        Task {
            defer { claiming = false }
            // An older lasso has no claim: free, as the web app reads it.
            let owner = (try? await source.claim(host, client: clientID)) ?? ""
            setOwner(owner, host: host)
        }
    }

    private func teardown() {
        loading?.cancel()
        stream?.stop()
        stream = nil
        bridge?.shutdown()
        bridge?.slug = nil
        web?.stopLoading()
        web?.loadHTMLString("", baseURL: nil)
        model.phase = .closed
        model.notice = nil
        claim.owner = ""
        Trace.event("herdr window closed")
    }
}

extension HerdrTerminalController: WKNavigationDelegate {
    public func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction) async -> WKNavigationActionPolicy {
        guard let url = action.request.url else { return .cancel }
        if url.scheme == HerdrTerminal.scheme { return .allow }
        // The blank page a closed window is left on.
        if url.absoluteString == "about:blank" { return .allow }
        if ["http", "https", "mailto"].contains(url.scheme?.lowercased() ?? ""), action.navigationType == .linkActivated {
            NSWorkspace.shared.open(url)
        }
        return .cancel
    }

    public func webView(_ webView: WKWebView, didStartProvisionalNavigation navigation: WKNavigation!) {
        // A new page: the old one's sockets (and herdr clients) go.
        bridge?.closeAll()
    }

    public func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        guard bridge?.slug != nil else { return }
        model.phase = .live
        bridge?.pushGate()
        bridge?.pushZoom()
    }

    public func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: any Error) {
        failed(error)
    }

    public func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: any Error) {
        failed(error)
    }

    /// A load that failed, unless another load replaced it or lasso's own
    /// answer already said why (onDocumentFailed).
    private func failed(_ error: any Error) {
        guard bridge?.slug != nil, (error as NSError).code != NSURLErrorCancelled, !isFailed else { return }
        model.phase = .failed(error.localizedDescription)
    }

    public func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
        guard bridge?.slug != nil else { return }
        reload()
    }
}

extension HerdrTerminalController: WKUIDelegate {
    /// A link asking for a new window opens in the browser instead.
    public func webView(
        _ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration, for action: WKNavigationAction,
        windowFeatures: WKWindowFeatures
    ) -> WKWebView? {
        if let url = action.request.url, ["http", "https", "mailto"].contains(url.scheme?.lowercased() ?? "") {
            NSWorkspace.shared.open(url)
        }
        return nil
    }
}
