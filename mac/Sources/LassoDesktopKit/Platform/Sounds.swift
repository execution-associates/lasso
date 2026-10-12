import AppKit

/// The inbox's arrival sound: one of the bundled sounds (Resources/Sounds),
/// or none. The choice is this Mac's, in UserDefaults `inboxSound`.
@MainActor
public enum InboxSound {
    public static let key = "inboxSound"
    /// Every bundled sound, in the order Settings lists them, with its name.
    public static let all: [(id: String, name: String)] = [("whoosh", "Whoosh"), ("marimba", "Marimba"), ("lasso", "Lasso")]
    public static let none = "none"
    public static let fallback = "whoosh"
    /// The sound uploaded to lasso (CustomSound), when one is cached here.
    public static let custom = "custom"

    public static var current: String {
        get { UserDefaults.standard.string(forKey: key) ?? fallback }
        set { UserDefaults.standard.set(newValue, forKey: key) }
    }

    private static var cache: [String: NSSound] = [:]
    private static var lastPlayed = Date.distantPast

    /// Play `id` (the chosen one by default). Arrivals closer together than
    /// Theme.Live.arrivalSound's gap make one sound, not a burst.
    public static func play(_ id: String? = nil, force: Bool = false) {
        let id = id ?? current
        guard id != none else { return }
        let now = Date()
        if !force, now.timeIntervalSince(lastPlayed) * 1000 < Theme.Live.arrivalSound.number("gap_ms") { return }
        lastPlayed = now
        guard let s = sound(id) else { return }
        s.stop()
        s.play()
    }

    private static func sound(_ id: String) -> NSSound? {
        if id == custom {
            // The upload, from this Mac's cache; until it is here, the default.
            guard let url = CustomSound.cached, let s = NSSound(contentsOf: url, byReference: false) else {
                return sound(fallback)
            }
            return s
        }
        if let s = cache[id] { return s }
        guard let url = Bundle.main.resourceURL?.appendingPathComponent("Sounds/\(id).mp3"),
              let s = NSSound(contentsOf: url, byReference: true)
        else { return nil }
        cache[id] = s
        return s
    }
}

/// The arrival sound uploaded to lasso, cached on this Mac. lasso keeps the
/// file (PUT /api/desktop/sound) and says what it is in ui_state.desktop_sound;
/// the app downloads it only when that record's sha256 is not the one cached,
/// so playing it never touches the network.
@MainActor
public enum CustomSound {
    static var dir: URL {
        FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("LassoDesktop/Sounds", isDirectory: true)
    }

    /// The cached file, named by its hash.
    public static var cached: URL? {
        try? FileManager.default.contentsOfDirectory(at: dir, includingPropertiesForKeys: nil)
            .first { $0.lastPathComponent.hasPrefix("custom-") }
    }

    static func file(_ sha: String) -> URL { dir.appendingPathComponent("custom-\(sha)") }

    /// Make the cache match lasso's record: fetch a new sound, drop a
    /// removed one. A failed fetch leaves the old file to try again next time.
    public static func sync(_ meta: DesktopSound?, client: LassoClient) async {
        guard let meta else {
            clear()
            return
        }
        if cached == file(meta.sha256) { return }
        var req = client.request("/api/desktop/sound")
        req.setValue("audio/*", forHTTPHeaderField: "Accept")
        guard let (data, code) = try? await client.raw(req), code == 200, !data.isEmpty else { return }
        clear()
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        try? data.write(to: file(meta.sha256), options: .atomic)
    }

    static func clear() {
        for f in (try? FileManager.default.contentsOfDirectory(at: dir, includingPropertiesForKeys: nil)) ?? []
        where f.lastPathComponent.hasPrefix("custom-") {
            try? FileManager.default.removeItem(at: f)
        }
    }

    /// Upload `file` as lasso's arrival sound, named by its file name.
    public static func upload(_ file: URL, client: LassoClient) async throws -> DesktopSound {
        let data = try Data(contentsOf: file)
        guard CGFloat(data.count) <= Theme.Live.arrivalSound.number("max_bytes") else { throw UploadError.tooBig }
        var req = client.request(
            "/api/desktop/sound", query: ["name": file.deletingPathExtension().lastPathComponent], method: "PUT", body: data)
        req.setValue("application/octet-stream", forHTTPHeaderField: "Content-Type")
        let meta: DesktopSound = try await client.send(req)
        clear()
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        try? data.write(to: Self.file(meta.sha256), options: .atomic)
        return meta
    }

    public static func remove(client: LassoClient) async throws {
        let req = client.request("/api/desktop/sound", method: "DELETE")
        let (_, code) = try await client.raw(req)
        guard (200..<300).contains(code) else { throw LassoError.http(code, "could not remove the sound") }
        clear()
    }

    public enum UploadError: Error, CustomStringConvertible {
        case tooBig
        public var description: String { "That file is over 1 MB; pick a short sound." }
    }
}
