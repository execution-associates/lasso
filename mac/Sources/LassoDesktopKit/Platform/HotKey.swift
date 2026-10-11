import AppKit
import Carbon.HIToolbox
import Foundation

/// A system-wide hotkey through Carbon's RegisterEventHotKey, which needs no
/// Accessibility or Input Monitoring permission (unlike a global NSEvent
/// monitor). Used for Theme.summonKey and a recorded inbox shortcut.
@MainActor
public final class HotKey {
    private var ref: EventHotKeyRef?
    private var handler: EventHandlerRef?
    private let id: UInt32
    private let action: () -> Void
    private static var nextID: UInt32 = 1

    /// `token` is {key, modifiers:[command|option|control|shift]}.
    public convenience init?(token: Token, action: @escaping () -> Void) {
        guard let key = token.value["key"]?.string, let code = HotKey.keyCodes[key] else { return nil }
        var mods: NSEvent.ModifierFlags = []
        for m in token.value["modifiers"]?.array?.compactMap(\.string) ?? [] {
            switch m {
            case "command": mods.insert(.command)
            case "option": mods.insert(.option)
            case "control": mods.insert(.control)
            case "shift": mods.insert(.shift)
            default: return nil
            }
        }
        self.init(keyCode: code, modifiers: mods, action: action)
    }

    public init?(keyCode: UInt32, modifiers: NSEvent.ModifierFlags, action: @escaping () -> Void) {
        id = HotKey.nextID
        HotKey.nextID += 1
        self.action = action
        var mods: UInt32 = 0
        if modifiers.contains(.command) { mods |= UInt32(cmdKey) }
        if modifiers.contains(.option) { mods |= UInt32(optionKey) }
        if modifiers.contains(.control) { mods |= UInt32(controlKey) }
        if modifiers.contains(.shift) { mods |= UInt32(shiftKey) }
        var spec = EventTypeSpec(eventClass: OSType(kEventClassKeyboard), eventKind: UInt32(kEventHotKeyPressed))
        let me = Unmanaged.passUnretained(self).toOpaque()
        let status = InstallEventHandler(
            GetApplicationEventTarget(),
            { _, event, user in
                guard let user, let event else { return OSStatus(eventNotHandledErr) }
                var hk = EventHotKeyID()
                GetEventParameter(
                    event, EventParamName(kEventParamDirectObject), EventParamType(typeEventHotKeyID), nil,
                    MemoryLayout<EventHotKeyID>.size, nil, &hk)
                let me = Unmanaged<HotKey>.fromOpaque(user).takeUnretainedValue()
                // Every HotKey's handler sees every hotkey: act on our own only.
                guard hk.id == me.id else { return OSStatus(eventNotHandledErr) }
                MainActor.assumeIsolated { me.action() }
                return noErr
            }, 1, &spec, me, &handler)
        guard status == noErr else { return nil }
        let hid = EventHotKeyID(signature: OSType(0x4C_53_44_4B), id: id)  // "LSDK"
        guard RegisterEventHotKey(keyCode, mods, hid, GetApplicationEventTarget(), 0, &ref) == noErr else {
            unregister()
            return nil
        }
    }

    /// Release the key and the handler; call before letting go of it.
    public func unregister() {
        if let ref { UnregisterEventHotKey(ref) }
        if let handler { RemoveEventHandler(handler) }
        ref = nil
        handler = nil
    }

    private static let keyCodes: [String: UInt32] = [
        "a": UInt32(kVK_ANSI_A), "l": UInt32(kVK_ANSI_L), "k": UInt32(kVK_ANSI_K), "space": UInt32(kVK_Space),
    ]
}
