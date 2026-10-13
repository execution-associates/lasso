import Foundation

// A file window live-updates when its file changes on the machine it lives
// on, lasso's own or one lasso drives over SFTP (PLAN 26). lasso has no
// change feed for files, so the app asks: one POST /api/file-stat per machine
// per tick, covering every window on it, each window's document and every
// file its page has loaded through the bridge (a markdown's images), at most
// `file.watch`'s max_paths per window. A window on screen is asked about
// every visible_s, one collapsed or covered every hidden_s, and one that is
// expanded again at once.

/// What the watcher asks of one window, and tells it.
@MainActor
protocol FileWatched: AnyObject {
    var watchHost: String { get }
    var watchDoc: String { get }
    /// Files the page loaded besides the document, oldest first.
    var watchAssets: [String] { get }
    /// On screen: open, expanded and not entirely covered.
    var watchVisible: Bool { get }
    /// The document changed on disk (and is there).
    func docChanged(_ e: FileStatEntry)
    /// The document is gone.
    func docDeleted()
    /// One or more of the page's other files changed.
    func assetsChanged()
}

@MainActor
final class FileWatcher {
    let source: FileSource
    private final class Entry {
        weak var window: (any FileWatched)?
        /// Each path's last signature: the first reading is the baseline.
        var seen: [String: String] = [:]
        var polled = Date.distantPast

        init(_ w: any FileWatched) { window = w }
    }

    private var entries: [ObjectIdentifier: Entry] = [:]
    private var loop: Task<Void, Never>?
    private var ticking = false
    private var again = false
    var now: () -> Date = Date.init
    let visibleEvery: TimeInterval
    let hiddenEvery: TimeInterval
    let maxPaths: Int

    init(source: FileSource) {
        self.source = source
        let t = Theme.File.watch
        visibleEvery = t.number("visible_s")
        hiddenEvery = t.number("hidden_s")
        maxPaths = Int(t.number("max_paths"))
    }

    /// Watch a window. `baseline`: the document as it was when the window
    /// opened, so a change before the first tick is still a change.
    func add(_ w: any FileWatched, baseline: FileStatEntry? = nil) {
        let e = Entry(w)
        if let baseline { e.seen[w.watchDoc] = baseline.signature }
        entries[ObjectIdentifier(w)] = e
        start()
    }

    func remove(_ w: any FileWatched) {
        entries[ObjectIdentifier(w)] = nil
        if entries.isEmpty { stop() }
    }

    /// A window came into view (expanded, shown): ask about it now.
    func poke(_ w: any FileWatched) {
        entries[ObjectIdentifier(w)]?.polled = .distantPast
        Task { await tick() }
    }

    func stop() {
        loop?.cancel()
        loop = nil
    }

    private func start() {
        guard loop == nil else { return }
        loop = Task { [weak self] in
            while !Task.isCancelled {
                guard let self else { return }
                await self.tick()
                try? await Task.sleep(nanoseconds: UInt64(self.visibleEvery * 1e9))
            }
        }
    }

    /// One pass: every window that is due, one request per machine (in
    /// slices of max_paths, the server's cap).
    func tick() async {
        if ticking {
            again = true
            return
        }
        ticking = true
        defer { ticking = false }
        repeat {
            again = false
            await pass()
        } while again
    }

    private func pass() async {
        entries = entries.filter { $0.value.window != nil }
        let t = now()
        let due = entries.values.filter { e in
            guard let w = e.window else { return false }
            return t.timeIntervalSince(e.polled) >= (w.watchVisible ? visibleEvery : hiddenEvery) - 0.05
        }
        guard !due.isEmpty else { return }
        var byHost: [String: [String]] = [:]
        for e in due {
            guard let w = e.window else { continue }
            e.polled = t
            var list = byHost[w.watchHost] ?? []
            for p in [w.watchDoc] + w.watchAssets.prefix(maxPaths - 1) where !list.contains(p) { list.append(p) }
            byHost[w.watchHost] = list
        }
        var got: [String: FileStatEntry] = [:]
        for (host, paths) in byHost {
            var i = 0
            while i < paths.count {
                let slice = Array(paths[i..<min(paths.count, i + maxPaths)])
                i += maxPaths
                guard let files = try? await source.stat(host: host, paths: slice) else { continue }
                for f in files where f.error == nil { got[FileHost.key(host: host, path: f.path)] = f }
            }
        }
        for e in due {
            guard let w = e.window else { continue }
            if let d = got[FileHost.key(host: w.watchHost, path: w.watchDoc)], note(e, d) {
                if d.exists { w.docChanged(d) } else { w.docDeleted() }
            }
            var assets = false
            for p in w.watchAssets.prefix(maxPaths - 1) where p != w.watchDoc {
                if let a = got[FileHost.key(host: w.watchHost, path: p)], note(e, a) { assets = true }
            }
            if assets { w.assetsChanged() }
        }
    }

    /// Record a reading; true when it differs from one before it.
    private func note(_ e: Entry, _ f: FileStatEntry) -> Bool {
        let was = e.seen[f.path]
        e.seen[f.path] = f.signature
        return was != nil && was != f.signature
    }
}
