import Carbon.HIToolbox
import Foundation

/// A system-wide hotkey through Carbon's RegisterEventHotKey, which needs no
/// Accessibility or Input Monitoring permission (unlike a global NSEvent
/// monitor). Used for Theme.summonKey.
@MainActor
public final class HotKey {
    private var ref: EventHotKeyRef?
    private var handler: EventHandlerRef?
    private let action: () -> Void

    /// `token` is {key, modifiers:[command|option|control|shift]}.
    public init?(token: Token, action: @escaping () -> Void) {
        self.action = action
        guard let key = token.value["key"]?.string, let code = HotKey.keyCodes[key] else { return nil }
        var mods: UInt32 = 0
        for m in token.value["modifiers"]?.array?.compactMap(\.string) ?? [] {
            switch m {
            case "command": mods |= UInt32(cmdKey)
            case "option": mods |= UInt32(optionKey)
            case "control": mods |= UInt32(controlKey)
            case "shift": mods |= UInt32(shiftKey)
            default: return nil
            }
        }
        var spec = EventTypeSpec(eventClass: OSType(kEventClassKeyboard), eventKind: UInt32(kEventHotKeyPressed))
        let me = Unmanaged.passUnretained(self).toOpaque()
        let status = InstallEventHandler(
            GetApplicationEventTarget(),
            { _, _, user in
                guard let user else { return noErr }
                let hk = Unmanaged<HotKey>.fromOpaque(user).takeUnretainedValue()
                MainActor.assumeIsolated { hk.action() }
                return noErr
            }, 1, &spec, me, &handler)
        guard status == noErr else { return nil }
        let id = EventHotKeyID(signature: OSType(0x4C_53_44_4B), id: 1)  // "LSDK"
        guard RegisterEventHotKey(code, mods, id, GetApplicationEventTarget(), 0, &ref) == noErr else { return nil }
    }

    private static let keyCodes: [String: UInt32] = [
        "a": UInt32(kVK_ANSI_A), "l": UInt32(kVK_ANSI_L), "k": UInt32(kVK_ANSI_K), "space": UInt32(kVK_Space),
    ]
}
