import AppKit
import Combine
import SwiftUI

/// Keycaps are hidden (the references show none) and appear while the
/// reveal modifier (Theme.revealKeys, ⌘) is held, over the control they
/// drive. The parity harness turns them on to check legend against binding.
@MainActor
public final class KeyHints: ObservableObject {
    public static let shared = KeyHints()
    @Published public var visible = false
    private var monitor: Any?

    public func start() {
        guard monitor == nil else { return }
        monitor = NSEvent.addLocalMonitorForEvents(matching: .flagsChanged) { [weak self] e in
            MainActor.assumeIsolated { self?.visible = e.modifierFlags.contains(.command) }
            return e
        }
    }
}

/// The keys a surface shows without ⌘ held: a card with the keyboard shows
/// the keys that work right now (PLAN 13.7a), so every button and move says
/// its key. nil shows them only while ⌘ is held.
struct ShownKeysKey: EnvironmentKey {
    static let defaultValue: Set<String>? = nil
}

extension EnvironmentValues {
    var shownKeys: Set<String>? {
        get { self[ShownKeysKey.self] }
        set { self[ShownKeysKey.self] = newValue }
    }
}

/// The control under the pointer, by its key: a keycap shows only there, so
/// the card stays quiet (Stephan: hints on hover, not all at once). One value
/// for the whole app, since the pointer is over one thing.
@MainActor
final class HoveredKey: ObservableObject {
    static let shared = HoveredKey()
    @Published var id: String?
}

struct KeyHint: ViewModifier {
    let binding: KeyBinding
    var alignment: Alignment = .bottomTrailing
    @ObservedObject var hints = KeyHints.shared
    @ObservedObject var hovered = HoveredKey.shared
    @Environment(\.shownKeys) private var shown

    func body(content: Content) -> some View {
        content
            .onHover { inside in
                if inside { hovered.id = binding.id } else if hovered.id == binding.id { hovered.id = nil }
            }
            .overlay(alignment: alignment) {
            if hints.visible || (shown?.contains(binding.id) == true && hovered.id == binding.id) {
                Keycap(binding).offset(x: Theme.space("s"), y: Theme.space("s"))
            }
        }
    }
}

/// A keycap set in a row (not over a control), for keys that act on the
/// row: shown while the pointer is on that row (`hovering`), or with ⌘ held.
struct InlineKey: View {
    let binding: KeyBinding
    var hovering = false
    @ObservedObject var hints = KeyHints.shared
    @Environment(\.shownKeys) private var shown

    var body: some View {
        if hints.visible || (hovering && shown?.contains(binding.id) == true) { Keycap(binding) }
    }
}

extension View {
    func keyHint(_ b: KeyBinding, _ alignment: Alignment = .bottomTrailing) -> some View {
        modifier(KeyHint(binding: b, alignment: alignment))
    }
}

/// A keycap: drawn from a KeyBinding, never from a string, so its legend is
/// the key the handler matches.
public struct Keycap: View {
    let binding: KeyBinding

    public init(_ binding: KeyBinding) { self.binding = binding }

    public var body: some View {
        let min = Theme.Card.keycapMinSize.size
        Text(binding.legend)
            .font(Theme.Card.keycapFont.font)
            .foregroundStyle(Theme.Text.primary.color)
            .padding(.horizontal, Theme.Card.keycapPadH.pt)
            .frame(minWidth: min.width, minHeight: min.height)
            .background(RoundedRectangle(cornerRadius: Theme.Card.keycapRadius.pt).fill(Theme.Card.keycapFill.color))
            .parityAnchor("keycap[\(binding.id)]")
            .accessibilityHidden(true)
    }
}

/// The panel every surface beside the rail sits on.
struct PanelBackground: View {
    var body: some View {
        let shape = RoundedRectangle(cornerRadius: Theme.Panel.radius.pt, style: .continuous)
        shape.fill(Theme.Panel.surface.color)
            .overlay(shape.strokeBorder(Theme.Panel.border.color, lineWidth: 1))
    }
}

extension View {
    /// Size, surface, border and clipping of a side panel.
    func panelChrome(_ size: CGSize, anchor: String) -> some View {
        frame(width: size.width, height: size.height)
            .background(PanelBackground())
            .clipShape(RoundedRectangle(cornerRadius: Theme.Panel.radius.pt, style: .continuous))
            .parityAnchor(anchor)
    }
}

/// A round icon button (close, paging chevrons).
struct RoundButton: View {
    let symbol: String
    var enabled = true
    var size: CGFloat = Theme.Control.round.number("size")
    var fill: Color = Theme.Control.round.color("fill")
    let label: String
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Image(systemName: symbol)
                .font(.system(size: size * Theme.icon.number("round_ratio"), weight: .semibold))
                .foregroundStyle(enabled ? Theme.Text.primary.color : Theme.Text.secondary.color)
                .frame(width: size, height: size)
                .background(Circle().fill(enabled ? fill : Theme.Control.round.color("disabled")))
                .contentShape(Circle())
        }
        .buttonStyle(.plain)
        .disabled(!enabled)
        .accessibilityLabel(label)
    }
}

/// A capsule text button: the footer's Kill agent / Acknowledge / Retry.
struct PillButton: View {
    let title: String
    var primary = false
    var textColor: Color = Theme.Text.primary.color
    let label: String
    let action: () -> Void

    var body: some View {
        let p = Theme.Control.pill
        Button(action: action) {
            Text(title)
                .font(Theme.Fonts.label.font)
                .foregroundStyle(primary ? Theme.Control.pillPrimary.color("text") : textColor)
                .padding(.horizontal, p.number("pad_h"))
                .frame(height: p.number("height"))
                .background(Capsule().fill(primary ? Theme.Control.pillPrimary.color("fill") : p.color("fill")))
                .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(label)
    }
}

/// The composer row (ref a2/a5): a round camera button, the field, a round
/// send button.
struct ComposerRow<Model: ComposerModel>: View {
    @ObservedObject var model: Model
    let attach: () -> Void

    var body: some View {
        let c = Theme.Control.composer
        let h = c.number("height")
        let canSend = !model.composer.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        HStack(spacing: c.number("gap")) {
            RoundButton(symbol: "paperclip", size: h, fill: c.color("fill"), label: "Attach a file (A)", action: attach)
                .keyHint(Keys.attach)
            ComposerField(model: model)
                .padding(.leading, c.number("field_pad_l"))
                .padding(.trailing, c.number("field_pad_r"))
                .frame(height: h)
                .background(Capsule().fill(c.color("fill")))
                .parityAnchor("composer.field")
            RoundButton(
                symbol: "arrow.up", enabled: canSend, size: h, fill: c.color("fill"), label: "Send (⏎)",
                action: model.send
            )
            .keyHint(Keys.send)
        }
        .parityAnchor("composer")
    }
}

/// A one-line text field that takes the cursor as it appears, with its text
/// selected (a rename in place). ⏎ submits and esc cancels, unless the
/// panel's own key handler takes them first. A click anywhere outside it, or
/// its window losing the keyboard, submits too (as Finder's rename does).
struct InlineField: NSViewRepresentable {
    @Binding var text: String
    let placeholder: String
    let label: String
    var font: NSFont = Theme.Fonts.name.nsFont
    var onSubmit: () -> Void = {}
    var onCancel: () -> Void = {}

    func makeCoordinator() -> Coordinator { Coordinator(text: $text, submit: onSubmit, cancel: onCancel) }

    static func dismantleNSView(_ f: NSTextField, coordinator: Coordinator) { coordinator.stopWatching() }

    func makeNSView(context: Context) -> NSTextField {
        let f = NSTextField(string: text)
        f.isBordered = false
        f.isBezeled = false
        f.drawsBackground = false
        f.focusRingType = .none
        f.font = font
        f.textColor = Theme.Text.primary.nsColor
        f.placeholderAttributedString = NSAttributedString(
            string: placeholder, attributes: [.foregroundColor: Theme.Text.placeholder.nsColor, .font: font])
        f.delegate = context.coordinator
        f.setAccessibilityLabel(label)
        DispatchQueue.main.async {
            f.window?.makeKeyAndOrderFront(nil)
            f.window?.makeFirstResponder(f)
            f.currentEditor()?.selectAll(nil)
            context.coordinator.watch(f)
        }
        return f
    }

    func updateNSView(_ f: NSTextField, context: Context) {
        if f.stringValue != text { f.stringValue = text }
    }

    @MainActor
    final class Coordinator: NSObject, NSTextFieldDelegate {
        var text: Binding<String>
        let submit: () -> Void
        let cancel: () -> Void
        init(text: Binding<String>, submit: @escaping () -> Void, cancel: @escaping () -> Void) {
            self.text = text
            self.submit = submit
            self.cancel = cancel
        }
        func controlTextDidChange(_ n: Notification) {
            if let f = n.object as? NSTextField { text.wrappedValue = f.stringValue }
        }
        func control(_ control: NSControl, textView: NSTextView, doCommandBy selector: Selector) -> Bool {
            switch selector {
            case #selector(NSResponder.insertNewline(_:)): submit()
            case #selector(NSResponder.cancelOperation(_:)): cancel()
            default: return false
            }
            return true
        }

        private var monitor: Any?
        private var resign: NSObjectProtocol?

        /// End the edit on a click outside the field, in its window or any
        /// other of the app's, and when its window loses the keyboard (a click
        /// in another app). Submitting twice is harmless: the models guard it.
        func watch(_ f: NSTextField) {
            stopWatching()
            monitor = NSEvent.addLocalMonitorForEvents(matching: [.leftMouseDown, .rightMouseDown]) { [weak self, weak f] e in
                if let f, e.window !== f.window || !f.bounds.contains(f.convert(e.locationInWindow, from: nil)) {
                    self?.end(f)
                }
                return e
            }
            if let w = f.window {
                resign = NotificationCenter.default.addObserver(forName: NSWindow.didResignKeyNotification, object: w, queue: .main) {
                    [weak self, weak f] _ in
                    MainActor.assumeIsolated { if let f { self?.end(f) } }
                }
            }
        }

        private func end(_ f: NSTextField) {
            stopWatching()
            f.window?.makeFirstResponder(nil)
            submit()
        }

        func stopWatching() {
            if let m = monitor { NSEvent.removeMonitor(m) }
            if let r = resign { NotificationCenter.default.removeObserver(r) }
            monitor = nil
            resign = nil
        }
    }
}

/// The pointing hand over a renamable title: pushed once, popped once, however
/// the hover ends (a click swaps the title for a field under the pointer).
@MainActor
enum TitleCursor {
    private static var pushed = false

    static func hover(_ inside: Bool) {
        if inside, !pushed {
            NSCursor.pointingHand.push()
            pushed = true
        } else if !inside, pushed {
            NSCursor.pop()
            pushed = false
        }
    }
}

/// An agent's name, as a window or card shows it. A click (or right-click,
/// Rename…) turns it into a field: ⏎ keeps, esc cancels, empty clears the
/// label. The pointer is a pointing hand over a name that can be renamed.
struct AgentTitle: View {
    @ObservedObject var model: CardModel
    var font: Token = Theme.Fonts.title
    var anchor = "agent.title"

    var body: some View {
        let p = model.current
        if model.renaming {
            InlineField(
                text: $model.renameDraft, placeholder: p.displayName, label: "New name for \(p.displayName)", font: font.nsFont,
                onSubmit: { model.commitRename() }, onCancel: { model.cancelRename() }
            )
            .padding(.horizontal, Theme.space("s"))
            .background(RoundedRectangle(cornerRadius: Theme.Row.radius.pt).strokeBorder(Theme.accent.color, lineWidth: 1))
            .parityAnchor(anchor)
        } else {
            Text(p.displayName)
                .font(font.font)
                .foregroundStyle(Theme.Text.primary.color)
                .lineLimit(1)
                .contentShape(Rectangle())
                .onHover { TitleCursor.hover($0 && model.canRename) }
                .onDisappear { TitleCursor.hover(false) }
                .onTapGesture {
                    TitleCursor.hover(false)
                    model.startRename()
                }
                .contextMenu {
                    if model.canRename { Button("Rename…") { model.startRename() } }
                }
                .accessibilityAction(named: "Rename") { model.startRename() }
                .parityAnchor(anchor)
        }
    }
}
