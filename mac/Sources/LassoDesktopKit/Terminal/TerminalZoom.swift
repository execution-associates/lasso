import AppKit
import Foundation

// The herdr window's text size (PLAN 22): ⌘= (or ⌘+) bigger, ⌘- smaller, ⌘0
// back to lasso's own. It is xterm's fontSize, the option the web app's
// Settings › Terminal text sets, so herdr reflows on the new columns and rows
// as it does for a browser tab: never a page zoom. One size for the app,
// kept across windows and launches.

public enum TerminalZoom {
    public enum Step: Equatable, Sendable {
        case bigger
        case smaller
        case actual
    }

    /// The bounds and step, from `terminal.zoom`.
    public struct Scale: Equatable, Sendable {
        public var min: Int
        public var max: Int
        public var step: Int
        /// lasso's own size (ttyd's `-t fontSize=`): what ⌘0 returns to.
        public var standard: Int

        public init(min: Int, max: Int, step: Int, standard: Int) {
            self.min = min
            self.max = max
            self.step = step
            self.standard = standard
        }

        public init(_ t: Token = Theme.Terminal.zoom) {
            self.init(
                min: Int(t.number("min")), max: Int(t.number("max")), step: Int(t.number("step")),
                standard: Int(t.number("default")))
        }

        public func clamp(_ size: Int) -> Int { Swift.min(max, Swift.max(min, size)) }

        /// The size after `s`, from `size`.
        public func apply(_ s: Step, to size: Int) -> Int {
            switch s {
            case .bigger: clamp(size + step)
            case .smaller: clamp(size - step)
            case .actual: standard
            }
        }
    }

    static let defaultsKey = "terminalFontSize"

    /// The size the window shows: the one last chosen, else lasso's own.
    public static func stored(_ d: UserDefaults = .standard, scale: Scale = Scale()) -> Int {
        let v = d.integer(forKey: defaultsKey)
        return v > 0 ? scale.clamp(v) : scale.standard
    }

    /// Keep `size`; lasso's own size is no choice, so it is kept as none.
    public static func store(_ size: Int, _ d: UserDefaults = .standard, scale: Scale = Scale()) {
        if size == scale.standard { d.removeObject(forKey: defaultsKey) } else { d.set(size, forKey: defaultsKey) }
    }

    /// The step a key asks for: ⌘= and ⌘+ (shifted = on a US layout, a key
    /// of its own on others, or the keypad's), ⌘- and ⌘0. Never with Option
    /// or Control, which are the terminal's.
    public static func step(chars: String?, flags: NSEvent.ModifierFlags) -> Step? {
        let mods = flags.intersection([.command, .shift, .option, .control])
        guard mods.contains(.command), mods.isDisjoint(with: [.option, .control]) else { return nil }
        switch chars {
        case "=", "+": return .bigger
        case "-": return .smaller
        case "0": return .actual
        default: return nil
        }
    }

    public static func step(_ e: NSEvent) -> Step? {
        guard e.type == .keyDown else { return nil }
        return step(chars: e.charactersIgnoringModifiers, flags: e.modifierFlags)
    }

    /// The page's query with its xterm fontSize: ttyd merges the query over
    /// the options lasso started it with, on every (re)connect, and refits.
    static func query(_ query: String, fontSize: Int) -> String {
        var c = URLComponents()
        c.percentEncodedQuery = query.isEmpty ? nil : query
        var items = (c.percentEncodedQueryItems ?? []).filter { $0.name != "fontSize" }
        items.append(URLQueryItem(name: "fontSize", value: String(fontSize)))
        c.percentEncodedQueryItems = items
        return c.percentEncodedQuery ?? ""
    }
}
