import AppKit
import Combine
import SwiftUI
import WebKit

// A file an agent opened for the human, in a window of its own (PLAN 26). It
// is a page window (PageWindow): an agent window's panel, over every app and
// on every Space, collapsing to its bar, resized by its corner, never full
// screen, and it never takes the keyboard by itself. Its bar says what the
// file is and where, and holds its controls; a banner strip under the bar
// (never over the file) says when the file changed on disk, went away or
// would not save; below is the file: lasso's page for text, markdown and SVG,
// a separate web view for rendered HTML (DocPage), or the Mac's own viewer
// (FileViewers).

/// One file window's state: what it shows and how that is going.
@MainActor
public final class FileWindowModel: ObservableObject {
    public enum Phase: Equatable, Sendable {
        case loading
        /// A native kind's copy coming down: the fraction, when lasso said the size.
        case downloading(Double?)
        case live
        case failed(String)
        /// A format neither AVFoundation nor QuickLook plays (webm, mkv, ogg).
        case unplayable
    }

    /// The strip under the bar, at most one at a time, the most pressing first.
    public enum Banner: Equatable, Sendable {
        /// ⌘W with unsaved edits: once more closes.
        case confirmClose
        /// A save was refused: the file changed on disk since it was read.
        case conflict
        /// The file changed on disk under unsaved edits.
        case changed
        case deleted
        /// ⋯'s Download did not finish.
        case downloadFailed
    }

    public let path: String
    public let host: String
    /// The machine as the bar names it (lasso's own by its hostname).
    public let hostLabel: String
    public let kind: FileKind
    @Published public internal(set) var from: String
    @Published public internal(set) var phase: Phase = .loading
    @Published public internal(set) var mode: FileMode
    /// What the page last said of its buffer.
    @Published var page = FilePageState()
    @Published public internal(set) var deleted = false
    @Published public internal(set) var confirmingClose = false
    /// ⋯'s Download is fetching the file.
    @Published public internal(set) var downloading = false
    /// Why the last Download failed, until it is dismissed or retried.
    @Published public internal(set) var downloadError: String?

    var onSave: () -> Void = {}
    var onMode: (FileMode) -> Void = { _ in }
    var onReload: () -> Void = {}
    var onOpenCopy: () -> Void = {}
    var onDownload: () -> Void = {}
    var onTakeDisk: () -> Void = {}
    var onKeepMine: () -> Void = {}
    var onDiscard: () -> Void = {}

    public init(open: FileOpen, kind: FileKind, hostLabel: String? = nil) {
        path = open.path
        host = open.hostKey
        self.hostLabel = hostLabel ?? open.hostKey
        self.kind = kind
        from = open.who
        mode = kind.hasRich && open.line == nil ? .rich : .raw
    }

    public var name: String { (path as NSString).lastPathComponent }
    public var subtitle: String { "\(hostLabel) · opened by \(from)" }
    public var dirty: Bool { page.dirty }
    /// Saving is the editor's: the web kinds, once loaded, with edits.
    public var canSave: Bool { kind.isWeb && page.loaded && page.dirty && !page.saving && !page.readOnly }

    public var banner: Banner? {
        if confirmingClose { return .confirmClose }
        if page.conflict { return page.saveError != nil ? .conflict : .changed }
        if deleted { return .deleted }
        if downloadError != nil { return .downloadFailed }
        return nil
    }

    /// What the banner says.
    public var bannerText: String {
        switch banner {
        case .confirmClose?: "You have unsaved changes. Close again to discard them."
        case .conflict?: "Not saved: \(name) changed on \(hostLabel) since you opened it."
        case .changed?: "\(name) changed on \(hostLabel) while you were editing."
        case .deleted?: "\(name) is no longer on \(hostLabel)."
        case .downloadFailed?: "Could not download \(name): \(downloadError ?? "")"
        case nil: ""
        }
    }

    func setMode(_ m: FileMode) {
        guard kind.hasRich, m != mode else { return }
        mode = m
        onMode(m)
    }

    /// The page's word on its buffer. Saved, a close waits no more.
    func take(_ s: FilePageState) {
        page = s
        if !s.dirty { confirmingClose = false }
        if s.missing { deleted = true }
        if s.loaded, kind != .html || mode == .raw { phase = .live }
    }

    /// ⌘W or the close button: false while the first one with unsaved edits
    /// asks to be sure.
    func shouldClose() -> Bool {
        guard dirty, !confirmingClose else { return true }
        confirmingClose = true
        return false
    }

    func keepEditing() { confirmingClose = false }
}

/// The window's bar, its header: the file's kind, name and where it is,
/// then rendered or source, Save and ⋯, then minimize and close.
struct FileBar: View {
    @ObservedObject var model: FileWindowModel
    @ObservedObject var chrome: AgentWindowChrome

    var body: some View {
        PageWindowHeader(chrome: chrome, anchor: "file", height: Theme.File.bar.number("height")) {
            HStack(spacing: Theme.space("m")) {
                FileTitle(model: model)
                Spacer(minLength: Theme.space("m"))
                if !chrome.collapsed { FileControls(model: model) }
            }
        }
        .parityAnchor("file.bar")
    }
}

/// The bar's leading end: the kind's icon, the name (a dot while it has
/// unsaved edits) over "host · opened by agent".
struct FileTitle: View {
    @ObservedObject var model: FileWindowModel

    var body: some View {
        HStack(spacing: Theme.space("m")) {
            LucideIcon(model.kind.icon, size: Theme.Rail.icon.number("size") - 3)
                .foregroundStyle(Theme.Text.secondary.color)
            VStack(alignment: .leading, spacing: 0) {
                HStack(spacing: Theme.space("xs")) {
                    Text(model.name).font(Theme.Fonts.label.font).foregroundStyle(Theme.Text.primary.color)
                        .lineLimit(1).truncationMode(.middle)
                    if model.dirty {
                        Circle().fill(Theme.accent.color)
                            .frame(width: Theme.space("s"), height: Theme.space("s"))
                            .parityAnchor("file.unsaved")
                            .accessibilityLabel("Unsaved changes")
                    }
                }
                Text(model.subtitle).font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color)
                    .lineLimit(1).truncationMode(.tail)
            }
            .parityAnchor("file.title")
        }
        .help(model.path)
    }
}

/// Rendered or source (markdown, HTML, SVG), Save (the web kinds), and ⋯.
struct FileControls: View {
    @ObservedObject var model: FileWindowModel

    var body: some View {
        HStack(spacing: Theme.space("s")) {
            if model.kind.hasRich {
                HStack(spacing: Theme.space("2xs")) {
                    PillButton(title: "Rendered", primary: model.mode == .rich, label: "Show \(model.name) rendered") { model.setMode(.rich) }
                    PillButton(title: "Source", primary: model.mode == .raw, label: "Show \(model.name)'s source") { model.setMode(.raw) }
                }
                .fixedSize()
                .parityAnchor("file.mode")
            }
            if model.kind.isWeb, !model.page.readOnly {
                PillButton(
                    title: model.page.saving ? "Saving" : "Save", primary: model.canSave, enabled: model.canSave,
                    hint: model.dirty ? "Saving" : "No changes to save", busy: model.page.saving, label: "Save \(model.name)"
                ) { model.onSave() }
                .fixedSize()
                .parityAnchor("file.save")
            }
            Menu {
                Button { model.onReload() } label: { Label("Reload", lucide: .refreshCw, size: Theme.Fonts.small.number("size")) }
                Button { model.onOpenCopy() } label: {
                    Label("Open a copy in default app", lucide: .externalLink, size: Theme.Fonts.small.number("size"))
                }
                Button { model.onDownload() } label: {
                    Label(model.downloading ? "Downloading…" : "Download", lucide: .download, size: Theme.Fonts.small.number("size"))
                }
                .disabled(model.downloading)
            } label: {
                Image(nsImage: Self.tinted(.ellipsis, size: Theme.Agent.window.number("icon"), color: Theme.Text.primary.nsColor))
            }
            .menuStyle(.borderlessButton)
            .menuIndicator(.hidden)
            .fixedSize()
            .frame(width: Theme.Agent.window.number("button"), height: Theme.Agent.window.number("button"))
            .parityAnchor("file.more")
            .accessibilityLabel("More")
            .help("Reload, open a copy, download")
        }
    }
}

extension FileControls {
    /// A borderless menu draws its label's template image black, whatever
    /// the foreground style: the icon is drawn in its colour instead.
    @MainActor static func tinted(_ icon: Lucide, size: CGFloat, color: NSColor) -> NSImage {
        let base = LucideAssets.image(icon, size: size)
        let out = NSImage(size: base.size, flipped: false) { r in
            base.draw(in: r)
            color.set()
            r.fill(using: .sourceAtop)
            return true
        }
        out.isTemplate = false
        out.accessibilityDescription = icon.spoken
        return out
    }
}

/// The strip under the bar: what happened, and what can be done about it.
struct FileBannerView: View {
    @ObservedObject var model: FileWindowModel

    var body: some View {
        if let b = model.banner {
            HStack(spacing: Theme.space("m")) {
                LucideIcon(b == .confirmClose ? .info : .triangleAlert, size: Theme.Fonts.small.number("size"))
                    .foregroundStyle(b == .confirmClose ? Theme.Text.secondary.color : Theme.dangerText.color)
                Text(model.bannerText)
                    .font(Theme.Fonts.small.font)
                    .foregroundStyle(Theme.Text.primary.color)
                    .lineLimit(1).truncationMode(.middle)
                Spacer(minLength: Theme.space("m"))
                actions(b).fixedSize()
            }
            .padding(.horizontal, Theme.Panel.inset.pt)
            .frame(height: Theme.File.banner.number("height"))
            .frame(maxWidth: .infinity)
            .background(Theme.Panel.inner.color("fill"))
            .overlay(alignment: .bottom) { Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair")) }
            .parityAnchor("file.banner")
            .accessibilityElement(children: .contain)
        }
    }

    @ViewBuilder private func actions(_ b: FileWindowModel.Banner) -> some View {
        HStack(spacing: Theme.space("s")) {
            switch b {
            case .confirmClose:
                PillButton(title: "Keep editing", label: "Keep editing \(model.name)") { model.keepEditing() }
                PillButton(title: "Discard", textColor: Theme.dangerText.color, label: "Discard your changes and close") { model.onDiscard() }
                PillButton(title: "Save", primary: true, label: "Save \(model.name)") { model.onSave() }
            case .changed, .conflict:
                PillButton(title: "Reload", label: "Drop your edits and show the file as it is now") { model.onTakeDisk() }
                    .parityAnchor("file.banner.reload")
                PillButton(title: "Keep mine", primary: true, label: "Keep your edits; the next save replaces the file") { model.onKeepMine() }
            case .deleted:
                EmptyView()
            case .downloadFailed:
                PillButton(title: "Dismiss", label: "Dismiss") { model.downloadError = nil }
                PillButton(title: "Try again", primary: true, label: "Try downloading \(model.name) again") { model.onDownload() }
            }
        }
    }
}

/// Over the file while it is not up: opening, coming down, why not.
struct FileStatusView: View {
    @ObservedObject var model: FileWindowModel

    var body: some View {
        ZStack {
            Theme.Panel.surface.color
            VStack(spacing: Theme.space("l")) {
                switch model.phase {
                case .failed(let why):
                    Text("Could not open \(model.name)").font(Theme.Fonts.title.font).foregroundStyle(Theme.Text.primary.color)
                    Text(why).font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color)
                        .multilineTextAlignment(.center).textSelection(.enabled)
                    PillButton(title: "Try again", label: "Try opening \(model.name) again") { model.onReload() }
                case .unplayable:
                    Text("Can't play here").font(Theme.Fonts.title.font).foregroundStyle(Theme.Text.primary.color)
                    Text("macOS has no player for .\((model.path as NSString).pathExtension.lowercased()) files.")
                        .font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color)
                    PillButton(title: "Open a copy in default app", primary: true, label: "Open a copy of \(model.name) in its default app") {
                        model.onOpenCopy()
                    }
                case .downloading(let f?):
                    ProgressView(value: f).frame(maxWidth: Theme.List.size.size.width / 2)
                    Text("Fetching \(model.name)").font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color)
                default:
                    WaitingRow(text: "Opening \(model.name)")
                }
            }
            .padding(Theme.Panel.inset.pt)
        }
        .parityAnchor("file.status")
    }
}

/// A native viewer inside SwiftUI (the parity harness's image state).
struct FileViewerRep: NSViewRepresentable {
    let viewer: FileViewer

    func makeNSView(context: Context) -> NSView { viewer.view }
    func updateNSView(_ v: NSView, context: Context) {}
}

/// The window's native parts, as the parity harness draws them: the bar, the
/// banner, and the file (a native viewer), or the status while the page,
/// which comes from a live lasso, opens.
struct FileWindowView: View {
    @ObservedObject var model: FileWindowModel
    @ObservedObject var chrome = AgentWindowChrome()
    var viewer: FileViewer?

    var body: some View {
        VStack(spacing: 0) {
            FileBar(model: model, chrome: chrome)
            FileBannerView(model: model)
            if let viewer, model.phase == .live {
                FileViewerRep(viewer: viewer)
            } else if model.phase == .live {
                Theme.Panel.surface.color.parityAnchor("file.page")
            } else {
                FileStatusView(model: model)
            }
        }
        .background(Theme.Panel.surface.color)
        .parityAnchor("file.window")
    }
}

/// The banner strip above the file: the file's view takes what is left.
final class FileContentView: NSView {
    let banner: NSView
    let body: NSView
    var showsBanner = false {
        didSet {
            guard showsBanner != oldValue else { return }
            banner.isHidden = !showsBanner
            needsLayout = true
        }
    }

    init(frame: NSRect, banner: NSView) {
        self.banner = banner
        body = NSView(frame: frame)
        super.init(frame: frame)
        banner.isHidden = true
        addSubview(body)
        addSubview(banner)
    }

    required init?(coder: NSCoder) { fatalError("unused") }

    override func layout() {
        super.layout()
        let h = showsBanner ? Theme.File.banner.number("height") : 0
        banner.frame = NSRect(x: 0, y: bounds.height - h, width: bounds.width, height: h)
        body.frame = NSRect(x: 0, y: 0, width: bounds.width, height: bounds.height - h)
        for v in body.subviews { v.frame = body.bounds }
    }
}

/// One file's window: its page or viewer, the watcher's word on its file,
/// and its close.
@MainActor
final class FileWindowController: NSObject {
    let model: FileWindowModel
    let open: FileOpen
    private let client: LassoClient
    private let source: FileSource
    private let cache: FileCache
    private(set) var window: PageWindow!
    private let content: FileContentView
    private let status: OverlayHostingView<FileStatusView>
    private var page: FileWebView?
    private var bridge: FileBridge?
    private var doc: DocView?
    private var viewer: FileViewer?
    /// The version of the file shown (a native kind's) or last seen.
    private var current: FileStatEntry?
    private var fetching: Task<Void, Never>?
    private var subs: Set<AnyCancellable> = []
    weak var watcher: FileWatcher?
    /// It closed (its button, ⌘W, a lasso switch).
    var onClosed: (FileWindowController) -> Void = { _ in }
    /// When it was opened or last shown (the manager reuses the oldest).
    private(set) var shownAt = Date()

    static let frameName = "LassoFile"

    init(
        open: FileOpen, kind: FileKind, stat: FileStatEntry?, hostLabel: String?, client: LassoClient, source: FileSource,
        frame: NSRect? = nil, cache: FileCache = .standard
    ) {
        self.open = open
        model = FileWindowModel(open: open, kind: kind, hostLabel: hostLabel)
        self.client = client
        self.source = source
        self.cache = cache
        current = stat
        let t = Theme.File.window
        let size = t.size2("size")
        let barHeight = Theme.File.bar.number("height")
        let bounds = NSRect(x: 0, y: 0, width: size.width, height: size.height - barHeight)
        status = OverlayHostingView(rootView: FileStatusView(model: model))
        content = FileContentView(frame: bounds, banner: NSHostingView(rootView: FileBannerView(model: model)))
        super.init()
        content.body.addSubview(status)
        window = PageWindow(name: Self.frameName, size: size, min: t.size2("min"), headerHeight: barHeight, page: content, frame: frame) {
            [model] chrome in FileBar(model: model, chrome: chrome)
        }
        window.panel.title = model.name
        window.shouldClose = { [weak self] in self?.model.shouldClose() ?? true }
        window.onClosed = { [weak self] in self?.teardown() }
        wire()
        start()
    }

    var panel: AgentWindowPanel { window.panel }
    var isOpen: Bool { window.isOpen }
    var dirty: Bool { model.dirty }
    var pageView: FileWebView? { page }
    var docView: DocView? { doc }
    var pageBridge: FileBridge? { bridge }

    /// On screen where the human can see it: open, expanded, not covered.
    var isOnScreen: Bool {
        guard window.isOpen, !window.collapsed else { return false }
        return window.panel.occlusionState.contains(.visible)
    }

    private func wire() {
        model.onSave = { [weak self] in self?.bridge?.save() }
        model.onMode = { [weak self] m in self?.switchMode(m) }
        model.onReload = { [weak self] in self?.reload() }
        model.onOpenCopy = { [weak self] in self?.openCopy() }
        model.onDownload = { [weak self] in self?.download() }
        model.onTakeDisk = { [weak self] in
            self?.model.deleted = false
            self?.bridge?.takeDisk()
        }
        model.onKeepMine = { [weak self] in self?.bridge?.keepMine() }
        model.onDiscard = { [weak self] in self?.window.close() }
        model.objectWillChange
            .receive(on: RunLoop.main)
            .sink { [weak self] _ in self?.refresh() }
            .store(in: &subs)
        window.chrome.$collapsed
            .removeDuplicates()
            .dropFirst()
            .sink { [weak self] collapsed in
                guard let self, !collapsed else { return }
                self.watcher?.poke(self)
            }
            .store(in: &subs)
    }

    /// The banner's strip and the status over the file, as the model says.
    private func refresh() {
        content.showsBanner = model.banner != nil
        status.isHidden = model.phase == .live
        window.focus = model.kind == .html && model.mode == .rich ? doc?.web : page
    }

    // MARK: showing

    /// Brought forward on the Space the user is on, expanded, without the
    /// keyboard unless asked (an agent's file never takes the keyboard: the
    /// person's click does).
    func show(takeKey: Bool = false) {
        if window.collapsed { window.toggle() }
        window.show(takeKey: takeKey)
        shownAt = Date()
        watcher?.poke(self)
    }

    /// The same file opened again: forward, and at `line` when one is named.
    func reopen(_ o: FileOpen) {
        if let who = o.from, !who.isEmpty { model.from = who }
        if let line = o.line, model.kind.isWeb {
            if model.kind.hasRich, model.mode != .raw { model.setMode(.raw) }
            bridge?.load(kind: model.kind, mode: .raw, line: line)
        }
        show()
    }

    /// Close it, unsaved edits or not (a lasso switch).
    func close() { window.close() }

    private func start() {
        if model.kind.isWeb {
            makePage()
            if model.kind == .html {
                makeDoc()
                if model.mode == .rich { doc?.load() }
            }
        } else if model.kind == .av, !FileKind.playable(model.path) {
            model.phase = .unplayable
        } else {
            let v = FileViewers.make(model.kind)
            viewer = v
            content.body.addSubview(v.view, positioned: .below, relativeTo: status)
            fetch()
        }
        layoutBody()
        refresh()
    }

    private func layoutBody() {
        content.needsLayout = true
        content.layoutSubtreeIfNeeded()
        let rich = model.kind == .html && model.mode == .rich
        page?.isHidden = rich
        doc?.web.isHidden = !rich
    }

    private func makePage() {
        let bridge = FileBridge(client: client, host: model.host, path: model.path)
        bridge.onLink = { NSWorkspace.shared.open($0) }
        bridge.onState = { [weak self] s in self?.model.take(s) }
        bridge.onSaved = { [weak self] mtime in self?.saved(mtime) }
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .nonPersistent()
        config.setURLSchemeHandler(bridge, forURLScheme: FilePage.scheme)
        config.userContentController.add(WeakMessageHandler(bridge), name: "lassoPage")
        config.userContentController.addUserScript(
            WKUserScript(source: FileBridge.script, injectionTime: .atDocumentStart, forMainFrameOnly: true))
        let web = FileWebView(frame: content.body.bounds, configuration: config)
        web.navigationDelegate = self
        web.uiDelegate = self
        web.underPageBackgroundColor = Theme.Panel.surface.nsColor
        web.setAccessibilityLabel(model.name)
        bridge.webView = web
        content.body.addSubview(web, positioned: .below, relativeTo: status)
        page = web
        self.bridge = bridge
        web.load(URLRequest(url: FilePage.pageURL(path: model.path, host: model.host, kind: model.kind, mode: model.mode, line: open.line)))
    }

    private func makeDoc() {
        let d = DocView(client: client, host: model.host, path: model.path)
        d.draft = { [weak self] in
            guard let self, self.model.dirty else { return nil }
            return await self.bridge?.draft()
        }
        d.onLoaded = { [weak self] in
            guard let self, self.model.mode == .rich else { return }
            self.model.phase = .live
        }
        d.onFailed = { [weak self] why in self?.model.phase = .failed(why) }
        content.body.addSubview(d.web, positioned: .below, relativeTo: status)
        doc = d
    }

    private func switchMode(_ m: FileMode) {
        bridge?.setMode(m)
        if model.kind == .html {
            if m == .rich { doc?.load() } else if model.page.loaded { model.phase = .live }
        }
        layoutBody()
        refresh()
    }

    private func saved(_ mtime: Int64) {
        if model.kind == .html, model.mode == .rich { doc?.load() }
    }

    /// The ⋯ menu's Reload, and a failed open's Try again.
    private func reload() {
        if model.kind.isWeb {
            model.deleted = false
            if model.phase != .live, let page {
                model.phase = .loading
                page.load(
                    URLRequest(url: FilePage.pageURL(path: model.path, host: model.host, kind: model.kind, mode: model.mode, line: nil)))
            } else {
                bridge?.takeDisk()
            }
            if model.kind == .html, model.mode == .rich { doc?.load() }
        } else if viewer != nil {
            fetch()
        }
    }

    // MARK: native kinds

    /// Fetch the file's current version into the cache and show it; the
    /// version before it goes once the new one is in.
    private func fetch() {
        fetching?.cancel()
        fetching = Task { [weak self] in
            guard let self else { return }
            var stat = self.current
            if stat == nil || stat?.exists == false {
                stat = try? await self.source.stat(host: self.model.host, paths: [self.model.path]).first
            }
            guard let stat, stat.exists, !Task.isCancelled else {
                if stat?.exists == false { self.model.deleted = true }
                if self.model.phase != .live { self.model.phase = .failed("\(self.model.name) is not on \(self.model.hostLabel)") }
                return
            }
            let dest = self.cache.version(host: self.model.host, path: self.model.path, mtime: stat.mtime, size: stat.size)
            if self.model.phase != .live { self.model.phase = .downloading(nil) }
            do {
                if !FileManager.default.fileExists(atPath: dest.path) {
                    let model = self.model
                    try await self.source.download(host: self.model.host, path: self.model.path, to: dest) { got, total in
                        guard total > 0 else { return }
                        Task { @MainActor in
                            if case .downloading = model.phase { model.phase = .downloading(Double(got) / Double(total)) }
                        }
                    }
                }
                guard !Task.isCancelled, self.isOpen else { return }
                self.current = stat
                self.viewer?.show(dest)
                self.cache.keepOnly(dest)
                self.model.phase = .live
            } catch {
                guard !Task.isCancelled else { return }
                if self.model.phase != .live { self.model.phase = .failed(String(describing: error)) }
            }
        }
    }

    /// ⋯'s Open a copy in default app: a copy, so the default app's edits
    /// never land in the cache or pass for the file on lasso's machine.
    private func openCopy() {
        let name = model.name
        let host = model.host
        let path = model.path
        Task {
            let dir = FileManager.default.temporaryDirectory.appendingPathComponent("Lasso Copies/" + UUID().uuidString, isDirectory: true)
            let dest = dir.appendingPathComponent(name)
            do {
                try await source.download(host: host, path: path, to: dest) { _, _ in }
                NSWorkspace.shared.open(dest)
            } catch {
                Trace.event("file copy failed: \(error)")
            }
        }
    }

    /// ⋯'s Download: the file into ~/Downloads under the first free name
    /// ("plan.md", "plan 2.md", …), as a browser saves one (`FileDownloads.save`).
    private func download() {
        guard !model.downloading else { return }
        model.downloading = true
        model.downloadError = nil
        let model = self.model
        let source = self.source
        Task {
            defer { model.downloading = false }
            do {
                _ = try await FileDownloads.save(host: model.host, path: model.path, from: source)
            } catch {
                model.downloadError = FileDownloads.reason(error)
            }
        }
    }

    private func teardown() {
        fetching?.cancel()
        bridge?.shutdown()
        page?.stopLoading()
        page?.loadHTMLString("", baseURL: nil)
        doc?.teardown()
        viewer?.close()
        cache.remove(host: model.host, path: model.path)
        watcher?.remove(self)
        subs.removeAll()
        Trace.event("file window closed")
        onClosed(self)
    }
}

extension FileWindowController: FileWatched {
    var watchHost: String { model.host }
    var watchDoc: String { model.path }
    var watchAssets: [String] { (bridge?.served ?? []) + (doc?.served ?? []) }
    var watchVisible: Bool { isOnScreen }

    func docChanged(_ e: FileStatEntry) {
        model.deleted = false
        if model.kind.isWeb {
            // The page compares content: a clean buffer takes the disk's, an
            // edited one says it changed (the banner) and keeps the edits.
            bridge?.changed()
            if model.kind == .html, model.mode == .rich, !model.dirty { doc?.load() }
        } else if viewer != nil {
            current = e
            fetch()
        }
    }

    func docDeleted() { model.deleted = true }

    func assetsChanged() {
        bridge?.assetsChanged()
        if model.kind == .html, model.mode == .rich { doc?.load() }
    }
}

extension FileWindowController: WKNavigationDelegate {
    func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction) async -> WKNavigationActionPolicy {
        guard let url = action.request.url else { return .cancel }
        if url.scheme == FilePage.scheme || url.absoluteString == "about:blank" { return .allow }
        if ["http", "https", "mailto"].contains(url.scheme?.lowercased() ?? "") { NSWorkspace.shared.open(url) }
        return .cancel
    }

    func webView(_ webView: WKWebView, didStartProvisionalNavigation navigation: WKNavigation!) { bridge?.closeAll() }

    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: any Error) {
        guard (error as NSError).code != NSURLErrorCancelled else { return }
        model.phase = .failed(error.localizedDescription)
    }

    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
        guard isOpen else { return }
        reload()
    }
}

extension FileWindowController: WKUIDelegate {
    func webView(
        _ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration, for action: WKNavigationAction,
        windowFeatures: WKWindowFeatures
    ) -> WKWebView? {
        if let url = action.request.url, ["http", "https", "mailto"].contains(url.scheme?.lowercased() ?? "") {
            NSWorkspace.shared.open(url)
        }
        return nil
    }
}

/// Where ⋯'s Download (and the Files column's) puts a file.
enum FileDownloads {
    /// The file into ~/Downloads, as a browser saves one. It comes down to a
    /// temporary folder first, so Downloads never holds half a file, then
    /// takes the first free name, carries the quarantine a browser's
    /// download does, and bounces the Dock's Downloads stack. Returns where
    /// it went.
    @MainActor
    static func save(host: String, path: String, from source: FileSource) async throws -> URL {
        let name = (path as NSString).lastPathComponent
        let tmp = FileManager.default.temporaryDirectory.appendingPathComponent("Lasso Downloads/" + UUID().uuidString, isDirectory: true)
        defer { try? FileManager.default.removeItem(at: tmp) }
        do {
            let got = tmp.appendingPathComponent(name)
            try await source.download(host: host, path: path, to: got) { _, _ in }
            let dir = try FileManager.default.url(for: .downloadsDirectory, in: .userDomainMask, appropriateFor: nil, create: true)
            let dest = destination(for: name, in: dir)
            try FileManager.default.moveItem(at: got, to: dest)
            quarantine(dest)
            DistributedNotificationCenter.default().post(name: .init("com.apple.DownloadFileFinished"), object: dest.resolvingSymlinksInPath().path)
            Trace.event("file downloaded")
            return dest
        } catch {
            Trace.event("file download failed: \(error)")
            throw error
        }
    }

    /// An error in words a banner or a row can show.
    static func reason(_ error: any Error) -> String {
        (error as? LocalizedError)?.errorDescription ?? String(describing: error)
    }

    /// The first name in `dir` nothing holds yet: `name`, then Safari's
    /// "name 2.ext", "name 3.ext", ….
    static func destination(for name: String, in dir: URL, exists: (String) -> Bool = { FileManager.default.fileExists(atPath: $0) }) -> URL {
        let first = dir.appendingPathComponent(name)
        guard exists(first.path) else { return first }
        let ext = (name as NSString).pathExtension
        let stem = ext.isEmpty ? name : (name as NSString).deletingPathExtension
        var n = 2
        while true {
            let next = dir.appendingPathComponent(ext.isEmpty ? "\(stem) \(n)" : "\(stem) \(n).\(ext)")
            if !exists(next.path) { return next }
            n += 1
        }
    }

    /// The quarantine a browser's download carries, so Gatekeeper treats a
    /// downloaded app or script as it would one from the web.
    static func quarantine(_ url: URL) {
        var u = url
        var v = URLResourceValues()
        v.quarantineProperties = [
            kLSQuarantineAgentNameKey as String: "Lasso",
            kLSQuarantineTypeKey as String: kLSQuarantineTypeWebDownload as String,
        ]
        try? u.setResourceValues(v)
    }
}
