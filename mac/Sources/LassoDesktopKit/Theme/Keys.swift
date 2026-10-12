import AppKit

/// The key vocabulary (brief A.3). The keys work but are not drawn: every
/// handler matches events against these values, and labels name the key
/// through `legend`, so a label and its binding cannot disagree.
public struct KeyBinding: Hashable, Sendable, Identifiable {
    public let id: String
    /// The key as a label names it ("A", "⌘.").
    public let legend: String
    /// What the key does, for accessibility labels.
    public let action: String
    let keyCode: UInt16?
    let character: String?
    /// Pressed with ⌘ (a menu-style key such as ⌘. for Stop).
    var command = false

    public func matches(_ e: NSEvent) -> Bool {
        let mods = e.modifierFlags.intersection([.command, .control, .option])
        guard mods == (command ? [.command] : []) else { return false }
        if let k = keyCode { return e.keyCode == k }
        return e.charactersIgnoringModifiers?.lowercased() == character
    }

    /// A key-down this binding matches: the parity harness's routing check
    /// and the tests press keys with it.
    func event() -> NSEvent? {
        NSEvent.keyEvent(
            with: .keyDown, location: .zero, modifierFlags: command ? .command : [], timestamp: 0, windowNumber: 0,
            context: nil, characters: character ?? "", charactersIgnoringModifiers: character ?? "", isARepeat: false,
            keyCode: keyCode ?? 0)
    }
}

public enum Keys {
    public static let esc = KeyBinding(id: "esc", legend: "esc", action: "Close", keyCode: 53, character: nil)
    /// The paperclip: U for upload, since A is Acknowledge.
    public static let attach = KeyBinding(
        id: "u", legend: "U", action: "Attach a file", keyCode: nil, character: "u")
    public static let send = KeyBinding(id: "enter", legend: "⏎", action: "Send", keyCode: 36, character: nil)
    public static let kill = KeyBinding(id: "x", legend: "X", action: "Kill agent", keyCode: nil, character: "x")
    public static let acknowledge = KeyBinding(
        id: "a", legend: "A", action: "Acknowledge", keyCode: nil, character: "a")
    /// Turn the agent's name into a field, as a click on it does.
    public static let rename = KeyBinding(id: "r", legend: "R", action: "Rename", keyCode: nil, character: "r")
    /// Put the cursor in the reply composer.
    public static let reply = KeyBinding(id: "l", legend: "L", action: "Reply", keyCode: nil, character: "l")
    public static let next = KeyBinding(id: "j", legend: "J", action: "Next card", keyCode: nil, character: "j")
    public static let previous = KeyBinding(
        id: "k", legend: "K", action: "Previous card", keyCode: nil, character: "k")
    /// Put the highlighted next step into the composer to edit (PLAN Q1).
    public static let editStep = KeyBinding(
        id: "tab", legend: "↳", action: "Edit this step", keyCode: 48, character: nil)
    /// Take the highlighted next step: send it (PLAN Q1).
    public static let takeStep = KeyBinding(
        id: "right", legend: "→", action: "Take this step", keyCode: 124, character: nil)

    /// Interrupt the agent's turn: ⌘., the Mac's "stop", which a text field
    /// would otherwise take as esc, so every panel reads it before its field.
    public static let stop = KeyBinding(
        id: "stop", legend: "⌘.", action: "Stop", keyCode: 47, character: nil, command: true)

    /// Show the agent in lasso's herdr: the terminal window, its pane focused.
    public static let herdr = KeyBinding(
        id: "t", legend: "T", action: "Show in herdr", keyCode: nil, character: "t")

    public static let all: [KeyBinding] = [
        esc, attach, send, reply, rename, stop, kill, acknowledge, next, previous, editStep, takeStep, herdr,
    ]

    /// 1-9 highlight a next step.
    public static func digit(_ e: NSEvent) -> Int? {
        guard e.modifierFlags.intersection([.command, .control, .option]).isEmpty,
              let c = e.charactersIgnoringModifiers, c.count == 1, let n = Int(c), (1...9).contains(n)
        else { return nil }
        return n
    }
}
