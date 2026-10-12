import AppKit
import Combine
import SwiftUI
import WebKit

// lasso's browsers in a window of their own (PLAN 24): the rail's Browser
// button opens it. Its bar picks which of the connected lasso's browsers it
// shows; below is the web Browser tab's Agent view (BrowserPage), whose own
// toolbar, page tabs and live view drive that browser.

/// What the Browser window asks of lasso.
@MainActor
public protocol BrowserSource: AnyObject {
    /// GET /api/browser: whether lasso has a browser, and every browser.
    func status() async throws -> BrowserStatusPayload
}

/// The source on a live lasso.
@MainActor
public final class LiveBrowserSource: BrowserSource {
    let client: LassoClient

    public init(client: LassoClient) { self.client = client }

    public func status() async throws -> BrowserStatusPayload { try await client.get("/api/browser") }
}

/// A lasso's browsers from a fixture (the parity harness, tests).
@MainActor
public final class FixtureBrowserSource: BrowserSource {
    var payload: BrowserStatusPayload?

    public init(_ payload: BrowserStatusPayload?) { self.payload = payload }

    public func status() async throws -> BrowserStatusPayload {
        guard let payload else { throw LassoError.http(404, "no browser") }
        return payload
    }
}

/// The window's state: the browsers to pick from, the one shown, and how
/// its page is doing.
@MainActor
public final class BrowserWindowModel: ObservableObject {
    public enum Phase: Equatable, Sendable {
        case closed
        case connecting
        case live
        case failed(String)
    }

    /// The connected lasso's browsers, the default first.
    @Published public internal(set) var browsers: [BrowserInfo] = []
    /// The browser shown (its id), remembered for the app.
    @Published public internal(set) var selected: String
    @Published public internal(set) var phase: Phase = .closed
    /// lasso has no browser to show: why, in its words.
    @Published public internal(set) var unavailable: String?

    let source: BrowserSource
    private let defaults: UserDefaults
    static let storedKey = "browserProfile"
    /// The human picked another browser.
    var onSelect: (String) -> Void = { _ in }
    var onRetry: () -> Void = {}

    public init(source: BrowserSource, defaults: UserDefaults = .standard) {
        self.source = source
        self.defaults = defaults
        selected = defaults.string(forKey: Self.storedKey) ?? BrowserPage.defaultBrowser
    }

    /// The browser shown, once lasso has listed it.
    public var current: BrowserInfo? { browsers.first { $0.id == selected } }

    /// The picker's words for the browser shown.
    public var currentName: String { current?.name ?? (selected == BrowserPage.defaultBrowser ? "Default" : selected) }

    /// A browser's line in the picker: its name, and how many pages it has open.
    public func line(_ b: BrowserInfo) -> String {
        let n = b.pages?.count ?? 0
        guard b.running, n > 0 else { return b.name }
        return "\(b.name) · \(n) \(n == 1 ? "page" : "pages")"
    }

    /// Read lasso's browsers. A remembered browser lasso no longer has gives
    /// way to the default.
    public func refresh() async {
        do {
            let st = try await source.status()
            browsers = st.browsers
            unavailable = st.available == false ? (st.reason?.isEmpty == false ? st.reason : "no Chromium was found") : nil
            if !browsers.contains(where: { $0.id == selected }), let first = browsers.first {
                select(first.id)
            }
        } catch {
            // The page says why it cannot reach the browser; the picker keeps
            // what it had.
        }
    }

    /// The human picked a browser.
    public func select(_ id: String) {
        guard id != selected else { return }
        selected = id
        defaults.set(id, forKey: Self.storedKey)
        onSelect(id)
    }

    /// An agent's page: its browser becomes the one shown (the page switches
    /// itself as it takes the page).
    public func take(_ o: BrowserOpen) {
        guard o.browser != selected else { return }
        selected = o.browser
        defaults.set(o.browser, forKey: Self.storedKey)
    }
}

/// The window's bar: what it is and which browser it shows.
struct BrowserBar: View {
    @ObservedObject var model: BrowserWindowModel

    var body: some View {
        let bar = Theme.Browser.bar
        HStack(spacing: Theme.space("m")) {
            Image(systemName: "globe")
                .font(.system(size: Theme.Rail.icon.number("size") - 3, weight: .regular))
                .foregroundStyle(Theme.Text.secondary.color)
            Text("Browser").font(Theme.Fonts.label.font).foregroundStyle(Theme.Text.primary.color)
            Menu {
                ForEach(model.browsers) { b in
                    Button { model.select(b.id) } label: {
                        if b.id == model.selected { Label(model.line(b), systemImage: "checkmark") } else { Text(model.line(b)) }
                    }
                }
            } label: {
                Text(model.currentName).font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.primary.color).lineLimit(1)
            }
            .menuStyle(.borderlessButton)
            .menuIndicator(.visible)
            .fixedSize()
            .disabled(model.browsers.count < 2)
            .padding(.horizontal, Theme.Control.pill.number("pad_h"))
            .frame(height: Theme.Control.pill.number("height"))
            .background(Capsule().fill(Theme.Control.pill.color("fill")))
            .parityAnchor("browser.picker")
            .accessibilityLabel("Browser")
            .accessibilityValue(model.currentName)
            .help("Which of lasso's browsers to show")
            Spacer(minLength: 0)
        }
        .padding(.horizontal, Theme.Panel.inset.pt)
        .frame(height: bar.number("height"))
        .frame(maxWidth: .infinity)
        .background(Theme.Panel.surface.color)
        .overlay(alignment: .bottom) { Rectangle().fill(Theme.Panel.border.color).frame(height: 1) }
        .parityAnchor("browser.bar")
    }
}

/// Over the page while it is not up: opening, or why it could not.
struct BrowserStatus: View {
    @ObservedObject var model: BrowserWindowModel

    var body: some View {
        ZStack {
            Theme.Panel.surface.color
            VStack(spacing: Theme.space("l")) {
                if case .failed(let why) = model.phase {
                    Text("Could not open the browser")
                        .font(Theme.Fonts.title.font)
                        .foregroundStyle(Theme.Text.primary.color)
                    Text(why)
                        .font(Theme.Fonts.small.font)
                        .foregroundStyle(Theme.Text.secondary.color)
                        .multilineTextAlignment(.center)
                        .textSelection(.enabled)
                    PillButton(title: "Try again", label: "Try opening the browser again") { model.onRetry() }
                } else {
                    WaitingRow(text: "Opening \(model.currentName)")
                }
            }
            .padding(Theme.Panel.inset.pt)
        }
        .parityAnchor("browser.status")
    }
}

/// The window's native parts, as the parity harness draws them: the bar
/// over the status that covers the page until it is up.
struct BrowserWindowView: View {
    @ObservedObject var model: BrowserWindowModel

    var body: some View {
        VStack(spacing: 0) {
            BrowserBar(model: model)
            BrowserStatus(model: model)
        }
        .background(Theme.Panel.surface.color)
        .parityAnchor("browser.window")
    }
}

/// The one Browser window: open, show an agent's page, close.
@MainActor
public final class BrowserWindowController: NSObject {
    public let model: BrowserWindowModel
    private let client: LassoClient
    private var window: NSWindow?
    private var web: LassoPageWebView?
    private var bridge: BrowserBridge?
    private var status: NSHostingView<BrowserStatus>?
    /// The page has mounted and takes requests.
    private var ready = false
    /// An agent's page that arrived before the page could take it.
    private var pending: BrowserOpen?
    private var subs: Set<AnyCancellable> = []

    static let frameName = "LassoBrowser"

    public init(source: BrowserSource, client: LassoClient) {
        model = BrowserWindowModel(source: source)
        self.client = client
        super.init()
        model.onRetry = { [weak self] in self?.load() }
        model.onSelect = { [weak self] id in self?.bridge?.select(id) }
    }

    public var isOpen: Bool { window?.isVisible == true }

    /// The window and its page, once made (tests).
    var hostWindow: NSWindow? { window }
    var pageView: LassoPageWebView? { web }

    /// On screen where the human can see it: open, not minimized, not
    /// covered entirely.
    public var isOnScreen: Bool {
        guard let w = window, w.isVisible, !w.isMiniaturized else { return false }
        return w.occlusionState.contains(.visible)
    }

    /// The rail's button: the window as it is, else the remembered browser.
    public func open() {
        present()
        switch model.phase {
        case .closed, .failed: load()
        case .connecting, .live: break
        }
        Task { await model.refresh() }
        Trace.event("browser window opened")
    }

    /// An agent's page: the window on that browser, that page selected.
    /// `raise` false leaves the window where it is and the keyboard where it
    /// is (the window is already in view).
    public func show(_ o: BrowserOpen, raise: Bool = true) {
        model.take(o)
        if raise || window == nil { present() }
        switch model.phase {
        case .closed, .failed:
            pending = o
            load()
        case .connecting, .live:
            if ready { bridge?.show(o) } else { pending = o }
        }
        Task { await model.refresh() }
        Trace.event("browser window showing an agent's page")
    }

    public func close() { window?.performClose(nil) }

    private func present() {
        let w = window ?? makeWindow()
        NSApp.activate()
        w.makeKeyAndOrderFront(nil)
        if let web { w.makeFirstResponder(web) }
    }

    private func makeWindow() -> NSWindow {
        let t = Theme.Browser.window
        let size = t.size2("size")
        let w = NSWindow(
            contentRect: NSRect(origin: .zero, size: size), styleMask: [.titled, .closable, .miniaturizable, .resizable],
            backing: .buffered, defer: false)
        w.isReleasedWhenClosed = false
        w.minSize = t.size2("min")
        w.tabbingMode = .disallowed
        w.appearance = NSAppearance(named: .darkAqua)
        w.backgroundColor = Theme.Panel.surface.nsColor
        w.collectionBehavior = [.fullScreenPrimary]
        w.title = "Browser"
        w.center()
        w.setFrameAutosaveName(Self.frameName)
        w.delegate = self

        let bridge = BrowserBridge(client: client)
        bridge.onLink = { NSWorkspace.shared.open($0) }
        bridge.onReady = { [weak self] in self?.pageReady() }
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .nonPersistent()
        config.setURLSchemeHandler(bridge, forURLScheme: BrowserPage.scheme)
        config.userContentController.add(WeakMessageHandler(bridge), name: "lassoPage")
        config.userContentController.addUserScript(
            WKUserScript(source: BrowserBridge.script, injectionTime: .atDocumentStart, forMainFrameOnly: true))

        let barHeight = Theme.Browser.bar.number("height")
        let content = NSView(frame: NSRect(origin: .zero, size: size))
        let pageFrame = NSRect(x: 0, y: 0, width: size.width, height: size.height - barHeight)
        let web = LassoPageWebView(frame: pageFrame, configuration: config)
        web.navigationDelegate = self
        web.uiDelegate = self
        web.underPageBackgroundColor = Theme.Panel.surface.nsColor
        web.autoresizingMask = [.width, .height]
        web.setAccessibilityLabel("Browser page")
        bridge.webView = web

        let status = NSHostingView(rootView: BrowserStatus(model: model))
        status.frame = pageFrame
        status.autoresizingMask = [.width, .height]
        let bar = NSHostingView(rootView: BrowserBar(model: model))
        bar.frame = NSRect(x: 0, y: size.height - barHeight, width: size.width, height: barHeight)
        bar.autoresizingMask = [.width, .minYMargin]
        content.addSubview(web)
        content.addSubview(status)
        content.addSubview(bar)
        w.contentView = content

        model.$phase
            .receive(on: RunLoop.main)
            .sink { [weak self] phase in self?.status?.isHidden = phase == .live }
            .store(in: &subs)

        window = w
        self.web = web
        self.bridge = bridge
        self.status = status
        return w
    }

    /// Load the page on the browser shown.
    private func load() {
        guard let web, let bridge else { return }
        ready = false
        model.phase = .connecting
        bridge.closeAll()
        web.load(URLRequest(url: BrowserPage.pageURL(browser: model.selected)))
    }

    private func pageReady() {
        ready = true
        // The page started on the browser in its address; one picked since
        // (or an agent's page's) wins.
        bridge?.select(model.selected)
        if let p = pending {
            pending = nil
            bridge?.show(p)
        }
    }

    private func teardown() {
        bridge?.shutdown()
        web?.stopLoading()
        web?.loadHTMLString("", baseURL: nil)
        ready = false
        pending = nil
        model.phase = .closed
        Trace.event("browser window closed")
    }
}

extension BrowserWindowController: NSWindowDelegate {
    public func windowWillClose(_ notification: Notification) { teardown() }
}

extension BrowserWindowController: WKNavigationDelegate {
    public func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction) async -> WKNavigationActionPolicy {
        guard let url = action.request.url else { return .cancel }
        if url.scheme == BrowserPage.scheme || url.absoluteString == "about:blank" { return .allow }
        if ["http", "https", "mailto"].contains(url.scheme?.lowercased() ?? "") { NSWorkspace.shared.open(url) }
        return .cancel
    }

    public func webView(_ webView: WKWebView, didStartProvisionalNavigation navigation: WKNavigation!) {
        // A new page: the old one's sockets go.
        bridge?.closeAll()
    }

    public func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        guard model.phase == .connecting else { return }
        model.phase = .live
    }

    public func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: any Error) { failed(error) }

    public func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: any Error) {
        failed(error)
    }

    private func failed(_ error: any Error) {
        guard model.phase == .connecting, (error as NSError).code != NSURLErrorCancelled else { return }
        model.phase = .failed(error.localizedDescription)
    }

    public func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
        guard model.phase != .closed else { return }
        load()
    }
}

extension BrowserWindowController: WKUIDelegate {
    /// A link asking for a new window opens in the Mac's browser instead.
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
