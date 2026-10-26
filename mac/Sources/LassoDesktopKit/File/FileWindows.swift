import AppKit
import Foundation

// Every file window, keyed by machine and path (PLAN 26): a file opened again
// brings its window forward (expanded, at the line named) rather than opening
// a second; new ones cascade from the last; past `file.window`'s max the
// oldest window without unsaved edits gives its place to the new one. And the
// gate an agent's `open-file` passes before anything opens.

@MainActor
public final class FileWindows {
    /// Why an agent's file opened nothing.
    public enum Skip: Equatable, Sendable {
        /// Settings › Open files agents show you is off.
        case off
        /// It is a folder (the web app's Files tab shows those).
        case directory
        /// Nobody is at this Mac: another Mac's person, maybe.
        case away
        /// This lasso predates /api/file-stat, and with it everything the
        /// window needs (`within`, `base_mtime`).
        case unsupported
        case missing
    }

    let client: LassoClient
    let source: FileSource
    let presence: UserPresence
    let watcher: FileWatcher
    var cache = FileCache.standard
    /// The setting (UserDefaults `openAgentFiles`, absent = on).
    var enabled: () -> Bool = { SettingsModel.openAgentFilesOn() }
    /// A machine's name for the bar ("local" is lasso's own hostname).
    var hostLabel: (String) -> String? = { _ in nil }
    private(set) var windows: [String: FileWindowController] = [:]
    private weak var newest: FileWindowController?
    var cap: Int { Int(Theme.File.window.number("max")) }

    init(client: LassoClient, source: FileSource, presence: UserPresence) {
        self.client = client
        self.source = source
        self.presence = presence
        watcher = FileWatcher(source: source)
    }

    /// An agent opened a file for the human: a window here, if this is the
    /// Mac they are at. Returns why not otherwise. The window never takes
    /// the keyboard.
    @discardableResult
    public func opened(_ o: FileOpen) async -> Skip? {
        guard enabled() else { return .off }
        guard o.dir != true else { return .directory }
        guard presence.isPresent else { return .away }
        return await reopen(o)
    }

    /// The person picked a file in the Files column: its window, whoever is
    /// at the Mac and whatever the setting (they asked for it). Returns why
    /// not when it cannot open.
    @discardableResult
    public func reopen(_ o: FileOpen) async -> Skip? {
        guard o.dir != true else { return .directory }
        // The file's size picks its kind, and the answer itself says this
        // lasso has the route: an older one answers its web app with a 200,
        // which LassoClient.send takes for "no such route".
        let stat: FileStatEntry
        do {
            guard let s = try await source.stat(host: o.hostKey, paths: [o.path]).first else { return .unsupported }
            stat = s
        } catch {
            return .unsupported
        }
        guard stat.exists, stat.error == nil else { return .missing }
        guard !stat.dir else { return .directory }
        open(o, stat: stat)
        return nil
    }

    /// A file's window, while it is open.
    func window(_ key: String) -> FileWindowController? { windows[key].flatMap { $0.isOpen ? $0 : nil } }

    /// The files with a window open, by key.
    public var openKeys: Set<String> { Set(windows.filter { $0.value.isOpen }.keys) }

    /// A window opened or closed (the Files column marks open ones).
    var onChange: () -> Void = {}

    /// The window for `o`: its own, brought forward, or a new one.
    @discardableResult
    func open(_ o: FileOpen, stat: FileStatEntry?) -> FileWindowController {
        if let w = windows[o.key], w.isOpen {
            w.reopen(o)
            Trace.event("file window shown again")
            return w
        }
        var frame = cascade()
        if windows.count >= cap, let oldest = windows.values.filter({ !$0.dirty }).min(by: { $0.shownAt < $1.shownAt }) {
            frame = oldest.panel.frame
            oldest.close()
        }
        let kind = FileKind.classify(path: o.path, size: stat?.size)
        let w = FileWindowController(
            open: o, kind: kind, stat: stat, hostLabel: hostLabel(o.hostKey), client: client, source: source, frame: frame, cache: cache)
        w.watcher = watcher
        let key = o.key
        w.onClosed = { [weak self] closed in
            guard let self, self.windows[key] === closed else { return }
            self.windows[key] = nil
            self.onChange()
        }
        windows[key] = w
        newest = w
        watcher.add(w, baseline: stat)
        w.show()
        onChange()
        Trace.event("file window opened (\(kind.rawValue))")
        return w
    }

    /// Down and right of the newest window still open; the first opens where
    /// the last one closed (PageWindow's remembered frame).
    private func cascade() -> NSRect? {
        guard let n = newest, n.isOpen else { return nil }
        let step = Theme.File.window.number("cascade")
        return n.panel.frame.offsetBy(dx: step, dy: -step)
    }

    /// Every window goes, unsaved edits or not: the lasso they belong to is
    /// no longer the one in use, so a save could land on another.
    public func closeAll() {
        for w in Array(windows.values) { w.close() }
        windows.removeAll()
        watcher.stop()
        onChange()
    }
}
