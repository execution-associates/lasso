import AppKit
import ApplicationServices

/// The keyboard shortcut that opens the inbox, kept in UserDefaults
/// `inboxShortcut`: a double tap of one modifier (the default is ⌥⌥), a
/// recorded key combination, or off.
public enum InboxShortcutChoice: Equatable, Sendable {
    case doubleTap(Modifier)
    /// A key with modifiers; `display` is what Settings shows ("⌃⌥Space").
    case combo(keyCode: UInt16, modifiers: UInt, display: String)
    case off

    public enum Modifier: String, CaseIterable, Sendable {
        case option, command, control, shift

        var flag: NSEvent.ModifierFlags {
            switch self {
            case .option: .option
            case .command: .command
            case .control: .control
            case .shift: .shift
            }
        }

        public var symbol: String {
            switch self {
            case .option: "⌥"
            case .command: "⌘"
            case .control: "⌃"
            case .shift: "⇧"
            }
        }
    }

    public static let key = "inboxShortcut"
    /// ⌥Space: a combination needs no permission, unlike a double tap.
    public static let fallback = InboxShortcutChoice.combo(
        keyCode: 49, modifiers: NSEvent.ModifierFlags.option.rawValue, display: "⌥Space")

    public static var stored: InboxShortcutChoice {
        get { UserDefaults.standard.string(forKey: key).flatMap(InboxShortcutChoice.init(stored:)) ?? fallback }
        set { UserDefaults.standard.set(newValue.stored, forKey: key) }
    }

    init?(stored s: String) {
        let parts = s.split(separator: ":", maxSplits: 3).map(String.init)
        switch parts.first {
        case "off": self = .off
        case "double": guard parts.count == 2, let m = Modifier(rawValue: parts[1]) else { return nil }; self = .doubleTap(m)
        case "combo":
            guard parts.count == 4, let k = UInt16(parts[1]), let m = UInt(parts[2]) else { return nil }
            self = .combo(keyCode: k, modifiers: m, display: parts[3])
        default: return nil
        }
    }

    var stored: String {
        switch self {
        case .off: "off"
        case .doubleTap(let m): "double:\(m.rawValue)"
        case .combo(let k, let m, let d): "combo:\(k):\(m):\(d)"
        }
    }

    public var label: String {
        switch self {
        case .off: "Off"
        case .doubleTap(let m): "\(m.symbol)\(m.symbol)  (double \(m.rawValue))"
        case .combo(_, _, let d): d
        }
    }

    /// A double tap is read from every app's modifier keys, which macOS
    /// allows only with Accessibility; a combination needs nothing.
    public var needsAccessibility: Bool {
        if case .doubleTap = self { return true }
        return false
    }

    /// A key event as a combination: at least one of ⌘⌥⌃ (⇧ alone would eat
    /// a capital letter), shown the way macOS menus show shortcuts.
    public static func combo(from e: NSEvent) -> InboxShortcutChoice? {
        let mods = e.modifierFlags.intersection([.command, .option, .control, .shift])
        guard !mods.intersection([.command, .option, .control]).isEmpty else { return nil }
        var d = ""
        if mods.contains(.control) { d += "⌃" }
        if mods.contains(.option) { d += "⌥" }
        if mods.contains(.shift) { d += "⇧" }
        if mods.contains(.command) { d += "⌘" }
        let key: String =
            switch Int(e.keyCode) {
            case 49: "Space"
            case 36: "↩"
            case 48: "⇥"
            case 53: "⎋"
            default: (e.charactersIgnoringModifiers ?? "?").uppercased()
            }
        // ":" separates the stored fields.
        return .combo(keyCode: e.keyCode, modifiers: mods.rawValue, display: (d + key).replacingOccurrences(of: ":", with: ";"))
    }
}

/// Watches for the inbox shortcut and calls `action`.
@MainActor
public final class InboxShortcut {
    private let action: () -> Void
    private var monitors: [Any] = []
    private var hotKey: HotKey?
    private var target: NSEvent.ModifierFlags?
    /// When the modifier went down alone, and when the last tap ended.
    private var down: Date?
    private var lastTap: Date?

    public init(action: @escaping () -> Void) { self.action = action }

    public func apply(_ choice: InboxShortcutChoice) {
        stop()
        switch choice {
        case .off: break
        case .combo(let k, let m, _):
            hotKey = HotKey(keyCode: UInt32(k), modifiers: NSEvent.ModifierFlags(rawValue: m), action: action)
        case .doubleTap(let m):
            target = m.flag
            let mask: NSEvent.EventTypeMask = [.flagsChanged, .keyDown]
            if let g = NSEvent.addGlobalMonitorForEvents(matching: mask, handler: { [weak self] e in
                MainActor.assumeIsolated { self?.handle(e) }
            }) { monitors.append(g) }
            if let l = NSEvent.addLocalMonitorForEvents(matching: mask, handler: { [weak self] e in
                MainActor.assumeIsolated { self?.handle(e) }
                return e
            }) { monitors.append(l) }
        }
    }

    public func stop() {
        monitors.forEach(NSEvent.removeMonitor)
        monitors = []
        hotKey?.unregister()
        hotKey = nil
        target = nil
        down = nil
        lastTap = nil
    }

    /// Two taps of the target modifier alone, each short, close together.
    /// Any other key or modifier in between starts over.
    private func handle(_ e: NSEvent) {
        guard let target else { return }
        let t = Theme.inboxDoubleTap
        let now = Date()
        guard e.type == .flagsChanged else {
            down = nil
            lastTap = nil
            return
        }
        let mods = e.modifierFlags.intersection([.command, .option, .control, .shift, .function])
        if mods == target {
            down = now
        } else if mods.isEmpty, let d = down {
            down = nil
            guard now.timeIntervalSince(d) * 1000 <= t.number("tap_ms") else {
                lastTap = nil
                return
            }
            if let last = lastTap, now.timeIntervalSince(last) * 1000 <= t.number("gap_ms") {
                lastTap = nil
                action()
            } else {
                lastTap = now
            }
        } else {
            down = nil
            lastTap = nil
        }
    }
}
