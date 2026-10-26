import AppKit
import QuickLookThumbnailing
import SwiftUI

// The Files column's hover preview (PLAN 27): resting on a row shows the file
// left of the column, as the agents and Bots columns preview theirs. Text and
// code show their first lines, markdown rendered, an image itself, and PDFs,
// video and documents Quick Look's thumbnail. It reads the file through
// lasso's /api/file like the window does, into a cache of its own, and never
// past a size cap: a preview is a glance, not a download. A click opens the
// file's window.

/// What a preview shows of its file.
public enum FilePreviewContent: Equatable {
    case loading
    /// The first lines of a text kind; `cut` when there was more.
    case text(String, cut: Bool)
    case markdown(String, cut: Bool)
    /// An image, or Quick Look's picture of the file.
    case image(NSImage)
    /// Something to say instead: a title and a line under it.
    case note(String, String)
}

/// Reads files for previews: stat, then the bytes into its own cache
/// (`~/Library/Caches/Lasso/Previews`), never past a kind's cap.
@MainActor
final class FilePreviewLoader {
    /// lasso's file routes (a fake in tests).
    var source: () -> FileSource?
    let cache: FileCache
    /// A preview already worked out, by file and version: resting on the
    /// same row again asks lasso only whether the file changed.
    private var memo: [String: (sig: String, content: FilePreviewContent)] = [:]
    private var memoOrder: [String] = []
    private let memoMax = 24
    /// The picture Quick Look draws, in points: at most the preview's size.
    var thumbnailSize = Theme.Agent.preview.size2("size")

    /// The text kinds are read up to `textCap` and shown up to `lineCap`
    /// lines; everything else is fetched whole up to `fileCap`.
    static let textCap: Int64 = 1 << 20
    static let fileCap: Int64 = 32 << 20
    static let lineCap = 400
    static let markdownCap = 48 << 10

    init(source: @escaping () -> FileSource?, cache: FileCache = FilePreviewLoader.standardCache) {
        self.source = source
        self.cache = cache
        reset()
    }

    static var standardCache: FileCache {
        let caches = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask).first ?? FileManager.default.temporaryDirectory
        return FileCache(root: caches.appendingPathComponent("Lasso/Previews", isDirectory: true))
    }

    /// Everything kept goes (a lasso switch, and at launch).
    func reset() {
        memo = [:]
        memoOrder = []
        try? FileManager.default.removeItem(at: cache.root)
    }

    /// A file off the list: its copy goes.
    func forget(_ f: RecentFile) {
        memo[f.id] = nil
        memoOrder.removeAll { $0 == f.id }
        cache.remove(host: f.host, path: f.path)
    }

    func load(_ f: RecentFile, hostLabel: String) async -> FilePreviewContent {
        guard let source = source() else { return .note("No preview", "Not connected to a lasso.") }
        let stat: FileStatEntry
        do {
            guard let s = try await source.stat(host: f.host, paths: [f.path]).first else {
                return .note("No preview", "This lasso is too old to read files for the Mac app.")
            }
            stat = s
        } catch {
            return .note("Couldn't preview \(f.name)", FileDownloads.reason(error))
        }
        guard stat.exists, stat.error == nil else {
            return .note("No longer on \(hostLabel)", stat.error ?? "It was moved or deleted.")
        }
        guard !stat.dir else { return .note("\(f.name) is a folder", "Open it in the web app's Files tab.") }
        if let m = memo[f.id], m.sig == stat.signature { return m.content }
        let kind = FileKind.classify(path: f.path, size: stat.size)
        let cap = kind.isWeb ? Self.textCap : Self.fileCap
        guard stat.size <= cap else {
            return .note("Too large to preview", "\(Self.size(stat.size)). Click to open it.")
        }
        if kind == .av, !FileKind.playable(f.path) {
            return keep(f, stat, .note("No preview", "macOS can't play .\((f.path as NSString).pathExtension.lowercased()) files."))
        }
        let dest = cache.version(host: f.host, path: f.path, mtime: stat.mtime, size: stat.size)
        do {
            if !FileManager.default.fileExists(atPath: dest.path) {
                try await source.download(host: f.host, path: f.path, to: dest) { _, _ in }
                cache.keepOnly(dest)
            }
        } catch {
            return .note("Couldn't preview \(f.name)", FileDownloads.reason(error))
        }
        guard !Task.isCancelled else { return .loading }
        return keep(f, stat, await render(dest, kind: kind))
    }

    private func keep(_ f: RecentFile, _ stat: FileStatEntry, _ c: FilePreviewContent) -> FilePreviewContent {
        memo[f.id] = (stat.signature, c)
        memoOrder.removeAll { $0 == f.id }
        memoOrder.append(f.id)
        while memoOrder.count > memoMax { memo[memoOrder.removeFirst()] = nil }
        return c
    }

    /// The file, as the preview draws it.
    func render(_ url: URL, kind: FileKind) async -> FilePreviewContent {
        switch kind {
        case .markdown:
            guard let (s, cut) = Self.text(url, limit: Self.markdownCap) else { return Self.binary }
            return .markdown(s, cut: cut)
        case .svg:
            if let img = NSImage(contentsOf: url), img.isValid { return .image(img) }
            fallthrough
        case .text, .html:
            // HTML shows its source: rendering it is the window's, under its
            // confinement and content rules (DocPage).
            guard let (s, cut) = Self.text(url, limit: Int(Self.textCap)) else { return Self.binary }
            return .text(s, cut: cut)
        case .image:
            if let img = NSImage(contentsOf: url), img.isValid { return .image(img) }
            return await thumbnail(url)
        case .pdf, .av, .quicklook:
            return await thumbnail(url)
        }
    }

    private static let binary = FilePreviewContent.note("No preview", "It isn't text. Click to open it.")

    /// Quick Look's picture of the file, as Finder draws one.
    private func thumbnail(_ url: URL) async -> FilePreviewContent {
        let r = QLThumbnailGenerator.Request(fileAt: url, size: thumbnailSize, scale: 2, representationTypes: .thumbnail)
        guard let rep = try? await QLThumbnailGenerator.shared.generateBestRepresentation(for: r) else {
            return .note("No preview", "Quick Look can't draw this file. Click to open it.")
        }
        return .image(rep.nsImage)
    }

    /// The first `lineCap` lines of a text file, nil when it is not text
    /// (a NUL in its first 8 KiB, as git decides).
    static func text(_ url: URL, limit: Int) -> (String, Bool)? {
        guard let h = try? FileHandle(forReadingFrom: url) else { return nil }
        defer { try? h.close() }
        let data = (try? h.read(upToCount: limit + 1)) ?? Data()
        if data.prefix(8192).contains(0) { return nil }
        var cut = data.count > limit
        var s = String(decoding: data.prefix(limit), as: UTF8.self)
        let lines = s.split(separator: "\n", omittingEmptySubsequences: false)
        if lines.count > lineCap {
            s = lines.prefix(lineCap).joined(separator: "\n")
            cut = true
        }
        return (s, cut)
    }

    static func size(_ bytes: Int64) -> String { ByteCountFormatter.string(fromByteCount: bytes, countStyle: .file) }
}

/// One preview: the file, where it is, and what of it shows.
@MainActor
public final class FilePreviewModel: ObservableObject {
    public let file: RecentFile
    public let hostLabel: String
    /// Its window is open now.
    public let windowOpen: Bool
    @Published public internal(set) var content: FilePreviewContent = .loading
    let now: () -> Date
    var onOpen: () -> Void = {}

    public init(file: RecentFile, hostLabel: String, windowOpen: Bool = false, now: @escaping () -> Date = Date.init) {
        self.file = file
        self.hostLabel = hostLabel
        self.windowOpen = windowOpen
        self.now = now
    }

    /// "opened by reviewer · 4m ago", or why it never opened.
    public var line: String {
        let age = relativeAge(Int64(file.at.timeIntervalSince1970 * 1000), now: now()) ?? ""
        let who = file.from.flatMap { $0.isEmpty ? nil : $0 } ?? "an agent"
        return file.opened ? "opened by \(who) · \(age)" : "\(who) opened it while you were away · \(age)"
    }
}

struct FilePreviewView: View {
    @ObservedObject var model: FilePreviewModel

    private var inset: CGFloat { Theme.Panel.inset.pt }

    var body: some View {
        let f = model.file
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: Theme.space("m")) {
                LucideIcon(f.kind.icon, size: Theme.Rail.icon.number("size") - 3).foregroundStyle(Theme.Text.secondary.color)
                VStack(alignment: .leading, spacing: 0) {
                    HStack(spacing: Theme.space("xs")) {
                        Text(f.name).font(Theme.Fonts.label.font).foregroundStyle(Theme.Text.primary.color)
                            .lineLimit(1).truncationMode(.middle)
                        if model.windowOpen { OpenDot() }
                    }
                    Text("\(model.hostLabel) · \(f.path)").font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color)
                        .lineLimit(1).truncationMode(.middle)
                }
            }
            .padding(inset)
            .parityAnchor("file.preview.title")
            Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair"))
            content
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .clipped()
                .parityAnchor("file.preview.body")
            Rectangle().fill(Theme.Panel.divider.color).frame(height: Theme.space("hair"))
            HStack(spacing: Theme.space("m")) {
                Text(model.line).font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color).lineLimit(1)
                Spacer(minLength: Theme.space("m"))
                PillButton(title: "Open", primary: true, label: "Open \(f.name)") { model.onOpen() }
                    .fixedSize()
                    .parityAnchor("file.preview.open")
            }
            .padding(.horizontal, inset)
            .padding(.vertical, Theme.space("l"))
        }
        .contentShape(Rectangle())
        .onTapGesture { model.onOpen() }
        .panelChrome(Theme.Agent.preview.size2("size"), anchor: "file.preview")
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Preview of \(f.name)")
    }

    @ViewBuilder private var content: some View {
        switch model.content {
        case .loading:
            WaitingRow(text: "Reading \(model.file.name)")
        case .text(let s, let cut):
            ScrollView(.vertical) {
                VStack(alignment: .leading, spacing: Theme.space("l")) {
                    Text(s).font(Theme.Fonts.small.font.monospaced()).foregroundStyle(Theme.Text.primary.color)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    if cut { more }
                }
                .padding(inset)
            }
        case .markdown(let s, let cut):
            ScrollView(.vertical) {
                VStack(alignment: .leading, spacing: Theme.space("l")) {
                    MarkdownView(text: s, font: Theme.Fonts.body.font)
                    if cut { more }
                }
                .padding(inset)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
        case .image(let img):
            Image(nsImage: img).resizable().interpolation(.high).aspectRatio(contentMode: .fit).padding(inset)
                .accessibilityLabel(model.file.name)
        case .note(let title, let detail):
            VStack(spacing: Theme.space("m")) {
                Text(title).font(Theme.Fonts.title.font).foregroundStyle(Theme.Text.primary.color).multilineTextAlignment(.center)
                Text(detail).font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color).multilineTextAlignment(.center)
            }
            .padding(inset)
        }
    }

    private var more: some View {
        Text("Open it to see the rest.").font(Theme.Fonts.small.font).foregroundStyle(Theme.Text.secondary.color)
    }
}

/// The accent dot a file wears while its window is open.
struct OpenDot: View {
    var body: some View {
        Circle().fill(Theme.accent.color)
            .frame(width: Theme.space("s"), height: Theme.space("s"))
            .accessibilityLabel("Its window is open")
    }
}

@MainActor
final class FilePreviewHolder: ObservableObject {
    @Published var model: FilePreviewModel?
}

struct FilePreviewHost: View {
    @ObservedObject var presence: CardPresence
    @ObservedObject var holder: FilePreviewHolder

    var body: some View {
        Group { if let m = holder.model { FilePreviewView(model: m) } }
            .opacity(presence.shown ? 1 : 0)
            .offset(x: presence.shown ? 0 : Theme.Motion.cardOpen.number("slide"))
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }
}

/// The preview's panel, left of the Files column with its top on the hovered
/// row, shown without the keyboard, as the agents' preview is
/// (`ConversationPreviewController`).
@MainActor
final class FilePreviewController {
    private(set) var panel: RailPanel
    private let presence = CardPresence()
    private let holder = FilePreviewHolder()
    let loader: FilePreviewLoader
    /// The preview of a row's key, nil when the row is gone.
    var make: (String) -> FilePreviewModel? = { _ in nil }
    private var loading: Task<Void, Never>?

    init(loader: FilePreviewLoader) {
        self.loader = loader
        let size = Theme.Agent.preview.size2("size")
        let host = NSHostingView(rootView: FilePreviewHost(presence: presence, holder: holder))
        host.frame = NSRect(origin: .zero, size: size)
        panel = RailPanel(content: host, size: size)
        panel.hasShadow = true
        panel.onKey = { [weak self] e in
            guard Keys.esc.matches(e) else { return false }
            self?.close()
            return true
        }
    }

    var isOpen: Bool { presence.shown }
    /// The row the preview shows, while it is open.
    private(set) var key: String?
    var showing: String? { isOpen ? key : nil }
    var model: FilePreviewModel? { holder.model }

    /// Show `key`'s preview beside `column`, top level with `row` (screen rects).
    func show(_ key: String, beside column: NSRect, row: NSRect) {
        if self.key != key || holder.model == nil {
            loading?.cancel()
            guard let m = make(key) else { return }
            holder.model = m
            self.key = key
            loading = Task { [weak self, loader] in
                let c = await loader.load(m.file, hostLabel: m.hostLabel)
                guard !Task.isCancelled, let self, self.holder.model === m else { return }
                m.content = c
            }
        }
        let size = Theme.Agent.preview.size2("size")
        let vis = Screens.visibleFrame(for: column)
        let x = max(vis.minX, column.minX - Theme.Agent.preview.number("gap") - size.width)
        let y = min(max(vis.minY, row.maxY - size.height), vis.maxY - size.height)
        panel.setFrameOrigin(NSPoint(x: x.rounded(), y: y.rounded()))
        // In from hidden: the content is in place first and the window fades
        // (see ConversationPreviewController.show for why not SwiftUI's).
        let fading = !presence.shown || !panel.isVisible
        presence.shown = true
        if fading { panel.alphaValue = 0 }
        panel.orderIn(takeKey: false) { panel = $0 }
        if fading {
            let panel = panel
            NSAnimationContext.runAnimationGroup { c in
                c.duration = Theme.Motion.cardOpen.value["duration"]?.double ?? 0
                c.timingFunction = CAMediaTimingFunction(name: .easeOut)
                panel.animator().alphaValue = 1
            }
        }
    }

    func close() {
        guard presence.shown else { return }
        loading?.cancel()
        if panel.isKeyWindow { panel.resignKey() }
        withAnimationToken(Theme.Motion.cardClose) { presence.shown = false }
        let delay = Theme.Motion.cardClose.value["duration"]?.double ?? 0
        Task { @MainActor [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(delay * 1e9) + 20_000_000)
            guard let self, !self.presence.shown else { return }
            self.panel.orderOut(nil)
            self.holder.model = nil
            self.key = nil
        }
    }
}
