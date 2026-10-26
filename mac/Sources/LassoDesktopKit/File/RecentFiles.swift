import Combine
import Foundation

// The files agents showed the human on this Mac (PLAN 27): what the rail's
// Files column lists. One list per lasso, kept on this Mac only (UserDefaults
// `recentFiles`), newest first, at most `cap`. A file that arrived while
// nobody was at the Mac, or with Settings › Open files agents show you off,
// is listed too, marked as not opened, and lights the rail's Files button
// until the column has been opened.

/// One file an agent opened for the human here.
public struct RecentFile: Codable, Sendable, Equatable, Identifiable {
    public var path: String
    /// The machine, as requests name it ("local" for lasso's own).
    public var host: String
    /// Who opened it: the agent's name.
    public var from: String?
    /// When it was last opened for the human.
    public var at: Date
    /// It opened in a window here. False while it has only ever arrived
    /// when nobody was at this Mac, or with the setting off.
    public var opened: Bool

    public init(path: String, host: String = "local", from: String? = nil, at: Date, opened: Bool) {
        self.path = path
        self.host = FileHost.normal(host)
        self.from = from
        self.at = at
        self.opened = opened
    }

    /// One entry per file per machine, as one window per file per machine.
    public var id: String { FileHost.key(host: host, path: path) }
    public var name: String { (path as NSString).lastPathComponent }
    /// What its window is opened from.
    public var open: FileOpen { FileOpen(path: path, host: host, from: from, app: "mac") }
    /// Its kind by name alone (the size is not known until it is asked).
    public var kind: FileKind { FileKind.classify(path: path, size: nil) }

    /// The folder it is in, a home folder written `~`: lasso's machines are
    /// Linux and Macs, so `/home/<user>` and `/Users/<user>` are homes.
    public var folder: String { Self.abbreviated((path as NSString).deletingLastPathComponent) }

    static func abbreviated(_ p: String) -> String {
        let parts = p.split(separator: "/", omittingEmptySubsequences: false)
        guard parts.count >= 3, parts[0].isEmpty, parts[1] == "home" || parts[1] == "Users", !parts[2].isEmpty else { return p }
        let rest = parts.dropFirst(3).joined(separator: "/")
        return rest.isEmpty ? "~" : "~/" + rest
    }
}

@MainActor
public final class RecentFiles: ObservableObject {
    @Published public private(set) var items: [RecentFile] = []
    /// A file came that did not open (nobody at this Mac, or the setting
    /// off) since the column was last opened: the rail's Files button wears a dot.
    @Published public private(set) var unseen = false

    public static let cap = 50
    static let key = "recentFiles"
    private let defaults: UserDefaults
    /// Off for fixtures and the parity harness: nothing is read or written.
    private let persist: Bool
    /// The lasso the list belongs to (its address).
    public private(set) var scope: String

    private struct Stored: Codable {
        var items: [RecentFile]
        var unseen: Bool
    }

    public init(scope: String, defaults: UserDefaults = .standard, persist: Bool = true) {
        self.scope = scope
        self.defaults = defaults
        self.persist = persist
        load()
    }

    /// Another lasso's list.
    public func use(scope: String) {
        guard scope != self.scope else { return }
        self.scope = scope
        load()
    }

    /// What an agent's `open-file` came to (`FileWindows.opened`'s answer):
    /// opened, or held back because nobody was at this Mac or the setting is
    /// off. A folder, a file that is not there and a lasso too old to open
    /// files are not listed: there is nothing to reopen.
    public func note(_ o: FileOpen, skip: FileWindows.Skip?, at: Date = Date()) {
        switch skip {
        case nil: add(o, opened: true, at: at)
        case .away?, .off?: add(o, opened: false, at: at)
        case .directory?, .missing?, .unsupported?: return
        }
    }

    /// A file to the top of the list. Once opened it stays opened.
    public func add(_ o: FileOpen, opened: Bool, at: Date = Date()) {
        guard !o.webOnly, o.dir != true else { return }
        let old = items.first { $0.id == o.key }
        let from = o.from?.isEmpty == false ? o.from : old?.from
        let f = RecentFile(path: o.path, host: o.hostKey, from: from, at: at, opened: opened || old?.opened == true)
        items.removeAll { $0.id == f.id }
        items.insert(f, at: 0)
        if items.count > Self.cap { items.removeLast(items.count - Self.cap) }
        if !opened { unseen = true }
        save()
    }

    /// The person opened it from the column: it is opened now.
    public func markOpened(_ id: String) {
        guard let i = items.firstIndex(where: { $0.id == id }), !items[i].opened else { return }
        items[i].opened = true
        save()
    }

    /// Off the list; the file itself is untouched (Hide, and after Delete).
    public func remove(_ id: String) {
        guard items.contains(where: { $0.id == id }) else { return }
        items.removeAll { $0.id == id }
        save()
    }

    public func clear() {
        items = []
        unseen = false
        save()
    }

    /// The column opened: what came while away has been seen.
    public func markSeen() {
        guard unseen else { return }
        unseen = false
        save()
    }

    /// A fixture's list, as it stands.
    func replace(_ list: [RecentFile], unseen: Bool = false) {
        items = Array(list.prefix(Self.cap))
        self.unseen = unseen
    }

    private func load() {
        guard persist else { return }
        let all = defaults.dictionary(forKey: Self.key) ?? [:]
        let stored = (all[scope] as? Data).flatMap { try? JSONDecoder().decode(Stored.self, from: $0) }
        items = stored?.items ?? []
        unseen = stored?.unseen ?? false
    }

    private func save() {
        guard persist, let data = try? JSONEncoder().encode(Stored(items: items, unseen: unseen)) else { return }
        var all = defaults.dictionary(forKey: Self.key) ?? [:]
        all[scope] = data
        defaults.set(all, forKey: Self.key)
    }
}
