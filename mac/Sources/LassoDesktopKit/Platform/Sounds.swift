import AppKit

/// The inbox's arrival sound: one of the bundled sounds (Resources/Sounds),
/// or none. The choice is this Mac's, in UserDefaults `inboxSound`.
@MainActor
public enum InboxSound {
    public static let key = "inboxSound"
    /// Every bundled sound, in the order Settings lists them, with its name.
    public static let all: [(id: String, name: String)] = [
        ("whoosh", "Whoosh"), ("swoosh", "Swoosh"), ("chime", "Chime"), ("marimba", "Marimba"), ("bubble", "Bubble"),
        ("lasso", "Lasso"),
    ]
    public static let none = "none"
    public static let fallback = "whoosh"

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
        if let s = cache[id] { return s }
        guard let url = Bundle.main.resourceURL?.appendingPathComponent("Sounds/\(id).mp3"),
              let s = NSSound(contentsOf: url, byReference: true)
        else { return nil }
        cache[id] = s
        return s
    }
}
